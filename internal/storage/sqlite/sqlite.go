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

	_ "modernc.org/sqlite"

	"dbvfs/internal/catalog"
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

// Write is not part of WP-02; mutations arrive with WP-05.
func (s *Store) Write(context.Context, func(storage.Tx) error) error {
	return fmt.Errorf("%w: mutations are not implemented until WP-05", storage.ErrUnsupported)
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

type reader struct {
	store   *Store
	q       *sql.Tx
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
	if len(q.Filters) > 0 {
		return nil, "", fmt.Errorf("%w: filters are not implemented until WP-03", storage.ErrUnsupported)
	}
	if q.Cursor != "" {
		return nil, "", fmt.Errorf("%w: cursors are not implemented until WP-03", storage.ErrUnsupported)
	}
	if len(q.Projection) > 0 {
		return nil, "", fmt.Errorf("%w: projections are not implemented until WP-03", storage.ErrUnsupported)
	}

	order := q.Order
	if len(order) == 0 {
		for _, c := range t.PrimaryKey() {
			order = append(order, storage.OrderSpec{Column: c.Name})
		}
	}
	if len(order) == 0 {
		return nil, "", fmt.Errorf("%w: table %q has no stable order", storage.ErrUnsupported, q.Table)
	}
	orderBy, err := orderClause(order, t)
	if err != nil {
		return nil, "", err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	query := "SELECT " + columnList(t.Columns) + " FROM " + quoteIdent(t.Name) + " ORDER BY " + orderBy + " LIMIT ?"
	rows, err := r.q.QueryContext(ctx, query, limit)
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
	// WP-02 has no keyset cursor; an empty next cursor means "no token to
	// resume from", and callers detect truncation by asking for limit+1.
	return out, "", nil
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
	for i, spec := range order {
		found := false
		for _, c := range t.Columns {
			if c.Name == spec.Column {
				found = true
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
		parts[i] = quoteIdent(spec.Column) + dir
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
