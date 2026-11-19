// Package sqlite implements the storage port over an existing SQLite file. It
// opens the file read-only or read-write (never creating it), keeps SQL values
// typed, and resolves every identifier through internal/catalog (NFR-02).
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbvfs/internal/catalog"
	"dbvfs/internal/cursor"
	"dbvfs/internal/storage"
)

// Options configures an open store. Timeout becomes the connection
// busy_timeout and the caller sets the matching context deadline.
type Options struct {
	Path    string
	Mode    string // "ro" or "rw"
	Timeout time.Duration
}

// Store is one SQLite connection pool, capped at one connection so PRAGMAs and
// transactions stay on the same handle.
type Store struct {
	db *sql.DB

	mu            sync.Mutex
	loaded        bool
	schemaVersion int64
	tables        []catalog.Table
}

// Open validates and opens an existing SQLite file. mode=rw never creates a
// missing file; the caller checks existence first so the error is E_NOT_FOUND.
func Open(o Options) (*Store, error) {
	if o.Mode != "ro" && o.Mode != "rw" {
		return nil, fmt.Errorf("%w: access mode %q", storage.ErrUnsupported, o.Mode)
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	db, err := sql.Open("sqlite", dsn(o.Path, o.Mode, timeout))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{db: db}, nil
}

// Read runs fn inside one read transaction: catalog and rows come from the same
// snapshot, and the catalog is reloaded when schema_version changes (DESIGN §5).
func (s *Store) Read(ctx context.Context, fn func(storage.Reader) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	r := &reader{store: s, q: tx}
	if err := r.refreshCatalog(ctx); err != nil {
		return err
	}
	if err := fn(r); err != nil {
		return err
	}
	return tx.Commit()
}

// Write runs fn inside one explicit BEGIN IMMEDIATE transaction on a dedicated
// connection, as DESIGN §8 requires. It never uses sql.Tx: mixing database/sql
// transactions with manual BEGIN/COMMIT would let the driver commit behind the
// caller's back. The catalog is revalidated inside the transaction and the
// foreign_keys guard is verified before it starts.
func (s *Store) Write(ctx context.Context, fn func(storage.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return classify(fmt.Errorf("acquire write connection: %w", err))
	}
	// The transaction outcome is already decided when the connection closes;
	// a close error cannot change it.
	defer func() { _ = conn.Close() }()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("check foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("%w: foreign_keys is not enabled on the write connection", storage.ErrUnsupported)
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(fmt.Errorf("begin immediate: %w", err))
	}
	committed := false
	// Rollback is best effort by design: if it fails the transaction is gone
	// with the connection, and the pre-commit error is the one to report.
	defer func() {
		if committed {
			return
		}
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}()

	r := &reader{store: s, q: conn}
	if err := r.refreshCatalog(ctx); err != nil {
		return err
	}
	if err := fn(r); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("%w: %v; read the database to reconcile", storage.ErrCommitUnknown, err)
	}
	committed = true
	return nil
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// queryExecer is the subset of *sql.Tx and *sql.Conn the reader needs. Reads
// pass a transaction; writes pass the dedicated connection.
type queryExecer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type reader struct {
	store   *Store
	q       queryExecer
	version int64
}

func (r *reader) refreshCatalog(ctx context.Context) error {
	var version int64
	if err := r.q.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || s.schemaVersion != version {
		c, err := catalog.Load(ctx, r.q)
		if err != nil {
			return err
		}
		s.tables, s.schemaVersion, s.loaded = c.Tables, c.SchemaVersion, true
	}
	r.version = s.schemaVersion
	return nil
}

func (r *reader) Catalog(context.Context) ([]storage.Table, error) {
	tables := r.store.snapshot()
	out := make([]storage.Table, len(tables))
	for i := range tables {
		out[i] = tables[i].Table
	}
	return out, nil
}

func (r *reader) table(name string) (*catalog.Table, error) {
	tables := r.store.snapshot()
	for i := range tables {
		if tables[i].Name == name {
			return &tables[i], nil
		}
	}
	return nil, fmt.Errorf("%w: table %q", storage.ErrNotFound, name)
}

func (r *reader) Get(ctx context.Context, table string, key storage.Key) (storage.Row, error) {
	t, err := r.table(table)
	if err != nil {
		return storage.Row{}, err
	}
	if !t.Navigable {
		return storage.Row{}, fmt.Errorf("%w: table %q has no navigable primary key", storage.ErrUnsupported, table)
	}
	pk := t.PrimaryKey()
	if len(key) != len(pk) {
		return storage.Row{}, fmt.Errorf("%w: key has %d values, table %q needs %d", storage.ErrNotFound, len(key), table, len(pk))
	}
	args, err := bindKey(key)
	if err != nil {
		return storage.Row{}, err
	}
	query := "SELECT " + columnList(t.Columns) + " FROM " + quoteIdent(t.Name) + " WHERE " + keyConditions(pk)
	fields, err := scanFields(r.q.QueryRowContext(ctx, query, args...).Scan, t.Columns)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Row{}, fmt.Errorf("%w: table %q has no row with that key", storage.ErrNotFound, table)
	}
	if err != nil {
		return storage.Row{}, fmt.Errorf("read %q: %w", table, err)
	}
	rowKey, err := fieldsKey(fields, t.Columns)
	if err != nil {
		return storage.Row{}, err
	}
	return storage.Row{Fields: fields, Key: rowKey, ETag: etag(t.Name, rowKey, r.version, fields)}, nil
}

func (r *reader) List(ctx context.Context, q storage.Query) ([]storage.Row, string, error) {
	t, err := r.table(q.Table)
	if err != nil {
		return nil, "", err
	}
	if len(q.Projection) > 0 {
		return nil, "", fmt.Errorf("%w: projections are not implemented", storage.ErrUnsupported)
	}

	order := q.Order
	if len(order) == 0 {
		order = storage.DefaultOrder(t.Table)
	}
	if len(order) == 0 {
		return nil, "", fmt.Errorf("%w: table %q has no stable order", storage.ErrUnsupported, q.Table)
	}
	orderBy, err := orderClause(order, t)
	if err != nil {
		return nil, "", err
	}

	var where []string
	var args []any
	for _, f := range q.Filters {
		cond, filterArgs, err := filterCondition(f, t)
		if err != nil {
			return nil, "", err
		}
		where = append(where, cond)
		args = append(args, filterArgs...)
	}
	if q.Search != nil {
		cond, searchArgs, err := searchCondition(q.Search, t)
		if err != nil {
			return nil, "", err
		}
		where = append(where, cond)
		args = append(args, searchArgs...)
	}
	if q.Cursor != "" {
		last, err := cursor.DecodeKey(q.Cursor)
		if err != nil {
			return nil, "", err
		}
		if len(last) != len(order) {
			return nil, "", fmt.Errorf("%w: cursor has %d values for %d order columns", storage.ErrInvalid, len(last), len(order))
		}
		cond, cursorArgs, err := keysetCondition(order, last)
		if err != nil {
			return nil, "", err
		}
		where = append(where, cond)
		args = append(args, cursorArgs...)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	query := "SELECT " + columnList(t.Columns) + " FROM " + quoteIdent(t.Name)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + orderBy + " LIMIT ?"
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list %q: %w", q.Table, err)
	}
	defer rows.Close()

	out := []storage.Row{}
	for rows.Next() {
		fields, err := scanFields(rows.Scan, t.Columns)
		if err != nil {
			return nil, "", fmt.Errorf("list %q: %w", q.Table, err)
		}
		key, err := fieldsKey(fields, t.Columns)
		if err != nil {
			return nil, "", err
		}
		out = append(out, storage.Row{Fields: fields, Key: key, ETag: etag(t.Name, key, r.version, fields)})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list %q: %w", q.Table, err)
	}
	// The next position is not decided here: the application signs a cursor
	// only when it knows the policy allows another page (DESIGN §7).
	return out, "", nil
}

// filterCondition renders one typed filter with every value bound (NFR-02).
func filterCondition(f storage.Filter, t *catalog.Table) (string, []any, error) {
	if !t.HasColumn(f.Column) {
		return "", nil, fmt.Errorf("%w: unknown filter column %q", storage.ErrInvalid, f.Column)
	}
	col := quoteIdent(f.Column)
	switch f.Op {
	case "is-null":
		return col + " IS NULL", nil, nil
	case "not-null":
		return col + " IS NOT NULL", nil, nil
	case "=", "!=", "<", "<=", ">", ">=":
		if f.Value.Kind == "null" {
			if f.Op == "=" {
				return col + " IS NULL", nil, nil
			}
			if f.Op == "!=" {
				return col + " IS NOT NULL", nil, nil
			}
			return "", nil, fmt.Errorf("%w: operator %q cannot compare against null", storage.ErrInvalid, f.Op)
		}
		arg, err := bindValue(f.Value)
		if err != nil {
			return "", nil, err
		}
		return col + " " + f.Op + " ?", []any{arg}, nil
	}
	return "", nil, fmt.Errorf("%w: unknown filter operator %q", storage.ErrInvalid, f.Op)
}

// searchCondition renders literal substring matching with instr (DESIGN §7).
// It is case-sensitive and uses no FTS tables.
func searchCondition(s *storage.Search, t *catalog.Table) (string, []any, error) {
	if len(s.Columns) == 0 {
		return "", nil, fmt.Errorf("%w: grep needs at least one TEXT column", storage.ErrUnsupported)
	}
	parts := make([]string, len(s.Columns))
	args := make([]any, len(s.Columns))
	for i, c := range s.Columns {
		if !t.HasColumn(c) {
			return "", nil, fmt.Errorf("%w: unknown search column %q", storage.ErrInvalid, c)
		}
		parts[i] = "instr(" + quoteIdent(c) + ", ?) > 0"
		args[i] = s.Text
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}

// keysetCondition is the strict "tuple after last" predicate for keyset
// pagination. It supports mixed directions because OrderSpec carries Desc.
// orderClause has already validated every column.
func keysetCondition(order []storage.OrderSpec, last storage.Key) (string, []any, error) {
	parts := make([]string, 0, len(order))
	var args []any
	for i := range order {
		conds := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			arg, err := bindValue(last[j])
			if err != nil {
				return "", nil, err
			}
			conds = append(conds, quoteIdent(order[j].Column)+" = ?")
			args = append(args, arg)
		}
		arg, err := bindValue(last[i])
		if err != nil {
			return "", nil, err
		}
		cmp := ">"
		if order[i].Desc {
			cmp = "<"
		}
		conds = append(conds, quoteIdent(order[i].Column)+" "+cmp+" ?")
		args = append(args, arg)
		parts = append(parts, "("+strings.Join(conds, " AND ")+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}

func (s *Store) snapshot() []catalog.Table {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tables
}

// Table returns one full catalog entry (indexes, foreign keys, triggers and
// incoming references), which the storage port's Table cannot carry. It runs in
// its own read transaction so describe sees the same snapshot as the data.
func (s *Store) Table(ctx context.Context, name string) (catalog.Table, error) {
	var out catalog.Table
	err := s.Read(ctx, func(rd storage.Reader) error {
		t, err := rd.(*reader).table(name)
		if err != nil {
			return err
		}
		out = *t
		return nil
	})
	return out, err
}

// scanFields converts one row into typed fields in catalog order. Integers stay
// int64 (never float64) and BLOBs become standard base64 (DESIGN §10).
func scanFields(scan func(dest ...any) error, cols []storage.Column) ([]storage.Field, error) {
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := scan(ptrs...); err != nil {
		return nil, err
	}
	fields := make([]storage.Field, len(cols))
	for i, c := range cols {
		v, err := toValue(raw[i])
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", c.Name, err)
		}
		fields[i] = storage.Field{Name: c.Name, Value: v}
	}
	return fields, nil
}

func toValue(raw any) (storage.Value, error) {
	switch v := raw.(type) {
	case nil:
		return storage.Value{Kind: "null"}, nil
	case int64:
		return storage.Value{Kind: "integer", Data: strconv.FormatInt(v, 10)}, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return storage.Value{}, fmt.Errorf("%w: non-finite REAL value", storage.ErrUnsupported)
		}
		return storage.Value{Kind: "real", Data: strconv.FormatFloat(v, 'g', -1, 64)}, nil
	case string:
		return storage.Value{Kind: "text", Data: v}, nil
	case []byte:
		return storage.Value{Kind: "blob", Data: base64.StdEncoding.EncodeToString(v)}, nil
	default:
		return storage.Value{}, fmt.Errorf("%w: unexpected Go value of type %T", storage.ErrUnsupported, raw)
	}
}

// fieldsKey extracts the identity from catalog-ordered fields. Data whose
// runtime type cannot be a path segment is refused instead of misaddressed.
func fieldsKey(fields []storage.Field, cols []storage.Column) (storage.Key, error) {
	byName := make(map[string]storage.Value, len(fields))
	for i, c := range cols {
		byName[c.Name] = fields[i].Value
	}
	key := make(storage.Key, 0, len(cols))
	for _, c := range storage.PKColumns(cols) {
		v := byName[c.Name]
		switch v.Kind {
		case "integer", "text":
		case "null":
			return nil, fmt.Errorf("%w: primary key contains NULL", storage.ErrUnsupported)
		default:
			return nil, fmt.Errorf("%w: primary key kind %q is not navigable", storage.ErrUnsupported, v.Kind)
		}
		key = append(key, v)
	}
	return key, nil
}

func bindKey(key storage.Key) ([]any, error) {
	args := make([]any, len(key))
	for i, v := range key {
		arg, err := bindValue(v)
		if err != nil {
			return nil, err
		}
		args[i] = arg
	}
	return args, nil
}

func bindValue(v storage.Value) (any, error) {
	switch v.Kind {
	case "null":
		return nil, nil
	case "integer":
		n, err := strconv.ParseInt(v.Data, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: integer key %q: %v", storage.ErrUnsupported, v.Data, err)
		}
		return n, nil
	case "real":
		f, err := strconv.ParseFloat(v.Data, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: real key %q: %v", storage.ErrUnsupported, v.Data, err)
		}
		return f, nil
	case "text":
		return v.Data, nil
	case "blob":
		b, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: blob key is not base64: %v", storage.ErrUnsupported, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("%w: value kind %q", storage.ErrUnsupported, v.Kind)
}

func orderClause(order []storage.OrderSpec, t *catalog.Table) (string, error) {
	parts := make([]string, len(order))
	// Tables without a primary key have no keyset, so forcing BINARY here
	// cannot disagree with a cursor predicate; it makes their first page
	// independent of any declared column collation (D-12).
	forceBinary := len(t.PrimaryKey()) == 0
	for i, spec := range order {
		found := false
		collate := ""
		for _, c := range t.Columns {
			if c.Name == spec.Column {
				found = true
				if forceBinary && catalog.Affinity(c.DeclaredType) == "TEXT" {
					collate = " COLLATE BINARY"
				}
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: unknown order column %q", storage.ErrUnsupported, spec.Column)
		}
		dir := " ASC"
		if spec.Desc {
			dir = " DESC"
		}
		parts[i] = quoteIdent(spec.Column) + collate + dir
	}
	return strings.Join(parts, ", "), nil
}

func columnList(cols []storage.Column) string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.Name)
	}
	return strings.Join(names, ", ")
}

func keyConditions(pk []storage.Column) string {
	parts := make([]string, len(pk))
	for i, c := range pk {
		parts[i] = quoteIdent(c.Name) + " = ?"
	}
	return strings.Join(parts, " AND ")
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// etag is the SHA-256 of table, key, schema version and the complete typed
// values in catalog order (DESIGN §8). Length prefixes keep the stream
// unambiguous; it represents content, not a monotonic version.
func etag(table string, key storage.Key, schemaVersion int64, fields []storage.Field) string {
	h := sha256.New()
	write := func(s string) {
		fmt.Fprintf(h, "%d:", len(s))
		io.WriteString(h, s)
	}
	write(table)
	fmt.Fprintf(h, "%d:", schemaVersion)
	fmt.Fprintf(h, "%d:", len(key))
	for _, v := range key {
		write(v.Kind)
		write(v.Data)
	}
	fmt.Fprintf(h, "%d:", len(fields))
	for _, f := range fields {
		write(f.Value.Kind)
		write(f.Value.Data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func dsn(path, mode string, timeout time.Duration) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Set("mode", mode)
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", timeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	if mode == "ro" {
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
