package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"dbvfs/internal/catalog"
	"dbvfs/internal/cursor"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

// FindData is the shape find and export --format json share: the queried path
// plus the row objects from DESIGN §10.
type FindData struct {
	Path string    `json:"path"`
	Rows []RowData `json:"rows"`
}

// GrepEntry reports one literal match: the field it was found in, its row, and
// a 1-based character position, matching what SQLite's instr returns.
type GrepEntry struct {
	Path     string `json:"path"`
	Row      string `json:"row"`
	Column   string `json:"column"`
	Position int    `json:"position"`
}

type GrepData struct {
	Path    string      `json:"path"`
	Entries []GrepEntry `json:"entries"`
}

// CountData is the --count payload: the size of the filtered set, with no rows
// and no cursor (REF-03).
type CountData struct {
	Count int64 `json:"count"`
}

// page is one bounded query result before rendering.
type page struct {
	table     storage.Table
	rows      []storage.Row
	fields    []string // validated projection, nil when --fields was absent
	truncated bool
	next      string
}

// Find filters a table with typed --where expressions (FR-05, D-11), orders it
// with --order when given, and projects the rendered rows when fields is
// non-empty.
func (s Service) Find(ctx context.Context, p vfs.Path, where, fields []string, order string, limit int, token string) (FindData, bool, string, error) {
	pg, err := s.queryPage(ctx, p, "find", where, fields, order, nil, limit, token)
	if err != nil {
		return FindData{}, false, "", err
	}
	rows, err := s.renderRows(p.Alias, pg.table, pg.rows, pg.fields)
	if err != nil {
		return FindData{}, false, "", err
	}
	path, err := pathString(p)
	if err != nil {
		return FindData{}, false, "", err
	}
	return FindData{Path: path, Rows: rows}, pg.truncated, pg.next, nil
}

// Export returns the same selection as Find; the CLI decides whether to wrap
// it in one JSON object or emit one row per JSONL line (FR-16, D-2).
func (s Service) Export(ctx context.Context, p vfs.Path, where []string, limit int, token string) ([]RowData, bool, string, error) {
	pg, err := s.queryPage(ctx, p, "export", where, nil, "", nil, limit, token)
	if err != nil {
		return nil, false, "", err
	}
	rows, err := s.renderRows(p.Alias, pg.table, pg.rows, nil)
	if err != nil {
		return nil, false, "", err
	}
	return rows, pg.truncated, pg.next, nil
}

// Grep searches one literal substring across the table's TEXT columns (FR-05).
func (s Service) Grep(ctx context.Context, p vfs.Path, text string, limit int, token string) (GrepData, bool, string, error) {
	if text == "" {
		return GrepData{}, false, "", fmt.Errorf("%w: grep needs a non-empty literal", storage.ErrInvalid)
	}
	search := &text
	pg, err := s.queryPage(ctx, p, "grep", nil, nil, "", search, limit, token)
	if err != nil {
		return GrepData{}, false, "", err
	}
	entries := []GrepEntry{}
	for _, row := range pg.rows {
		rowPath, err := rowPath(p.Alias, pg.table, row)
		if err != nil {
			return GrepData{}, false, "", err
		}
		for _, c := range pg.table.Columns {
			if catalog.Affinity(c.DeclaredType) != "TEXT" {
				continue
			}
			v, ok := fieldValue(row, c.Name)
			if !ok || v.Kind != "text" {
				continue
			}
			at := strings.Index(v.Data, text)
			if at < 0 {
				continue
			}
			fieldPath := rowPath
			if pg.table.Navigable {
				fieldPath, err = pathString(vfs.Path{Kind: vfs.KindField, Alias: p.Alias, Table: pg.table.Name, Key: row.Key, Field: c.Name})
				if err != nil {
					return GrepData{}, false, "", err
				}
			}
			entries = append(entries, GrepEntry{
				Path:     fieldPath,
				Row:      rowPath,
				Column:   c.Name,
				Position: utf8.RuneCountInString(v.Data[:at]) + 1,
			})
		}
	}
	path, err := pathString(p)
	if err != nil {
		return GrepData{}, false, "", err
	}
	return GrepData{Path: path, Entries: entries}, pg.truncated, pg.next, nil
}

// Count reports how many rows find (search nil) or grep (search non-nil) would
// return under the same filters and the same table policy. It runs one
// COUNT(*) through the adapter: no row is materialized and --limit and
// --cursor play no part (REF-03). A given order or fields list is validated
// against the catalog so that a typo cannot hide behind an ignored flag, but
// the count itself is order- and projection-independent.
func (s Service) Count(ctx context.Context, p vfs.Path, op string, where []string, order string, fields []string, search *string) (CountData, error) {
	if p.Kind != vfs.KindTable {
		return CountData{}, fmt.Errorf("%w: %s needs a table path /ALIAS/TABLE", storage.ErrUnsupported, op)
	}
	if search != nil && *search == "" {
		return CountData{}, fmt.Errorf("%w: grep needs a non-empty literal", storage.ErrInvalid)
	}
	m, err := s.mount(p.Alias)
	if err != nil {
		return CountData{}, err
	}
	tablePath, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: p.Table})
	if err != nil {
		return CountData{}, err
	}
	// The same grant a read needs: counting rows is reading their existence.
	if !fieldAllowed(m, op, tablePath) {
		return CountData{}, policyDenied(op, "text", tablePath)
	}
	st, err := s.open(p.Alias)
	if err != nil {
		return CountData{}, err
	}
	defer st.Close()

	var n int64
	err = st.Read(ctx, func(rd storage.Reader) error {
		t, err := findTable(ctx, rd, p.Table)
		if err != nil {
			return err
		}
		filters, err := parseWhere(where, t)
		if err != nil {
			return err
		}
		if order != "" {
			if _, err := parseOrder(order, t); err != nil {
				return err
			}
		}
		if len(fields) > 0 {
			if _, err := parseFields(fields, t); err != nil {
				return err
			}
		}
		var searchSpec *storage.Search
		if search != nil {
			searchSpec, err = newSearchSpec(t, *search)
			if err != nil {
				return err
			}
		}
		n, err = rd.Count(ctx, storage.Query{Table: t.Name, Filters: filters, Search: searchSpec})
		return err
	})
	return CountData{Count: n}, err
}

// queryPage is the shared planner for find, grep and export: catalog lookup,
// typed filters, policy check, deterministic order, keyset continuation and one
// bounded read. A table without a stable identity is limited to its first page
// (D-12) and never receives a cursor.
func (s Service) queryPage(ctx context.Context, p vfs.Path, op string, where, fields []string, order string, search *string, limit int, token string) (page, error) {
	if p.Kind != vfs.KindTable {
		return page{}, fmt.Errorf("%w: %s needs a table path /ALIAS/TABLE", storage.ErrUnsupported, op)
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	m, err := s.mount(p.Alias)
	if err != nil {
		return page{}, err
	}
	tablePath, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: p.Table})
	if err != nil {
		return page{}, err
	}
	// A table-wide query needs one grant covering the table; a deeper (row or
	// field) grant cannot authorize it without exposing other rows. Deny before
	// opening the database: policy needs only the mount and the path, and a
	// denied query must not touch SQLite.
	if !fieldAllowed(m, op, tablePath) {
		return page{}, policyDenied(op, "text", tablePath)
	}
	st, err := s.open(p.Alias)
	if err != nil {
		return page{}, err
	}
	defer st.Close()

	var pg page
	searchText := ""
	if search != nil {
		searchText = *search
	}
	err = st.Read(ctx, func(rd storage.Reader) error {
		t, err := findTable(ctx, rd, p.Table)
		if err != nil {
			return err
		}
		pg.table = t
		pg.fields, err = parseFields(fields, t)
		if err != nil {
			return err
		}

		filters, err := parseWhere(where, t)
		if err != nil {
			return err
		}
		orderSpec := storage.DefaultOrder(t)
		if order != "" {
			user, err := parseOrder(order, t)
			if err != nil {
				return err
			}
			orderSpec = resolveOrder(user, t)
		}

		var searchSpec *storage.Search
		if search != nil {
			searchSpec, err = newSearchSpec(t, searchText)
			if err != nil {
				return err
			}
		}

		q := storage.Query{
			Table:   t.Name,
			Filters: filters,
			Order:   orderSpec,
			Limit:   limit + 1,
			Search:  searchSpec,
		}
		if token != "" {
			if !t.Navigable {
				return fmt.Errorf("%w: table %q has no stable identity, so cursors are unavailable", storage.ErrUnsupported, t.Name)
			}
			last, err := verifyCursor(s.ConfigDir, token, cursorExpectation{
				mount:   p.Alias,
				table:   t.Name,
				filters: filters,
				order:   orderSpec,
				search:  searchText,
				fields:  pg.fields,
			})
			if err != nil {
				return err
			}
			q.Cursor, err = cursor.EncodeKey(last)
			if err != nil {
				return err
			}
		}

		rows, _, err := rd.List(ctx, q)
		if err != nil {
			return err
		}
		pg.truncated = len(rows) > limit
		if pg.truncated {
			rows = rows[:limit]
		}
		pg.rows = rows
		if pg.truncated && t.Navigable {
			pg.next, err = signCursor(s.ConfigDir, cursorExpectation{
				mount:   p.Alias,
				table:   t.Name,
				filters: filters,
				order:   orderSpec,
				search:  searchText,
				fields:  pg.fields,
			}, orderKey(rows[len(rows)-1], orderSpec))
			if err != nil {
				return err
			}
		}
		return nil
	})
	return pg, err
}

// newSearchSpec is the one grep predicate definition: every TEXT column,
// matched literally. find/grep pages and counts share it.
func newSearchSpec(t storage.Table, text string) (*storage.Search, error) {
	columns := []string{}
	for _, c := range t.Columns {
		if catalog.Affinity(c.DeclaredType) == "TEXT" {
			columns = append(columns, c.Name)
		}
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("%w: table %q has no TEXT columns to grep", storage.ErrUnsupported, t.Name)
	}
	return &storage.Search{Text: text, Columns: columns}, nil
}

type cursorExpectation struct {
	mount   string
	table   string
	filters []storage.Filter
	order   []storage.OrderSpec
	search  string
	fields  []string
}

func verifyCursor(configDir, token string, want cursorExpectation) (storage.Key, error) {
	key, err := state.CursorKey(configDir)
	if err != nil {
		return nil, err
	}
	got, err := cursor.Verify(key, token)
	if err != nil {
		return nil, err
	}
	if got.Mount != want.mount || got.Table != want.table || got.Search != want.search ||
		!slices.Equal(got.Filters, want.filters) || !slices.Equal(got.Order, want.order) ||
		!slices.Equal(got.Fields, want.fields) {
		return nil, fmt.Errorf("%w: cursor does not belong to this query", storage.ErrInvalid)
	}
	if len(got.LastKey) != len(want.order) {
		return nil, fmt.Errorf("%w: cursor does not match the query order", storage.ErrInvalid)
	}
	return got.LastKey, nil
}

func signCursor(configDir string, e cursorExpectation, last storage.Key) (string, error) {
	key, err := state.CursorKey(configDir)
	if err != nil {
		return "", err
	}
	return cursor.Sign(key, cursor.State{
		Mount:   e.mount,
		Table:   e.table,
		Filters: e.filters,
		Order:   e.order,
		Search:  e.search,
		Fields:  e.fields,
		LastKey: last,
	})
}

// renderRows renders rows with the shared {path, fields, etag} shape. A row of a
// table without a stable identity has no address, so its path is the table's.
func (s Service) renderRows(alias string, t storage.Table, rows []storage.Row, fields []string) ([]RowData, error) {
	out := make([]RowData, 0, len(rows))
	for _, row := range rows {
		path, err := rowPath(alias, t, row)
		if err != nil {
			return nil, err
		}
		out = append(out, RowData{Path: path, Fields: rowFields(row, t, fields), ETag: row.ETag})
	}
	return out, nil
}

// parseFields validates a --fields list against the catalog. A repeated or
// unknown column is E_USAGE, like --where; nil means no projection. The list
// is canonicalized (sorted) so that two spellings of the same projection
// produce the same cursor payload and pass cursor verification interchangeably.
func parseFields(names []string, t storage.Table) ([]string, error) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] || !t.HasColumn(name) {
			return nil, fmt.Errorf("%w: unknown column %q", storage.ErrInvalid, name)
		}
		seen[name] = true
	}
	fields := slices.Clone(names)
	slices.Sort(fields)
	return fields, nil
}

// parseOrder reads --order's "col[:asc|desc],..." grammar. Only catalog
// identifiers and the two directions are accepted, so no text ever reaches SQL;
// every spec asks for NULLS LAST (REF-02, PO decision).
func parseOrder(raw string, t storage.Table) ([]storage.OrderSpec, error) {
	terms := strings.Split(raw, ",")
	order := make([]storage.OrderSpec, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		column, dir, hasDir := strings.Cut(term, ":")
		if !t.HasColumn(column) {
			return nil, fmt.Errorf("%w: unknown column %q", storage.ErrInvalid, column)
		}
		if seen[column] {
			return nil, fmt.Errorf("%w: column %q is ordered more than once", storage.ErrInvalid, column)
		}
		seen[column] = true
		spec := storage.OrderSpec{Column: column, NullsLast: true}
		if hasDir {
			switch dir {
			case "asc":
			case "desc":
				spec.Desc = true
			default:
				return nil, fmt.Errorf("%w: unknown order direction %q", storage.ErrInvalid, dir)
			}
		}
		order = append(order, spec)
	}
	return order, nil
}

// resolveOrder makes the user's order total: the remaining primary-key columns
// follow in their own order, or the full default order for a table without a
// stable identity (D-12 keeps such a table on its first page).
func resolveOrder(user []storage.OrderSpec, t storage.Table) []storage.OrderSpec {
	order := slices.Clone(user)
	seen := make(map[string]bool, len(order))
	for _, spec := range order {
		seen[spec.Column] = true
	}
	for _, spec := range storage.DefaultOrder(t) {
		if seen[spec.Column] {
			continue
		}
		spec.NullsLast = true
		order = append(order, spec)
	}
	return order
}

// orderKey is the tuple the keyset cursor continues from, in resolved order.
// Row.Key is only the primary key, so a user order that leads with other
// columns needs their values too.
func orderKey(row storage.Row, order []storage.OrderSpec) storage.Key {
	byName := make(map[string]storage.Value, len(row.Fields))
	for _, f := range row.Fields {
		byName[f.Name] = f.Value
	}
	key := make(storage.Key, len(order))
	for i, spec := range order {
		key[i] = byName[spec.Column]
	}
	return key
}

// rowFields is the fields object of a row: the whole row as a map when there is
// no projection (today's contract), or the requested columns plus the key.
func rowFields(row storage.Row, t storage.Table, fields []string) any {
	if len(fields) == 0 {
		return fieldMap(row)
	}
	keep := make(map[string]bool, len(fields))
	for _, name := range fields {
		keep[name] = true
	}
	out := make(projectedFields, 0, len(fields)+1)
	for i, f := range row.Fields {
		if keep[f.Name] || (i < len(t.Columns) && t.Columns[i].PKOrder > 0) {
			out = append(out, f)
		}
	}
	return out
}

// projectedFields is the ordered object of a --fields row. A map would
// serialize in byte order; the contract emits catalog order and always keeps
// the key columns, which the next cursor is built from.
type projectedFields []storage.Field

func (f projectedFields) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, field := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(field.Name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func rowPath(alias string, t storage.Table, row storage.Row) (string, error) {
	if t.Navigable && len(row.Key) > 0 {
		return pathString(vfs.Path{Kind: vfs.KindRow, Alias: alias, Table: t.Name, Key: row.Key})
	}
	return pathString(vfs.Path{Kind: vfs.KindTable, Alias: alias, Table: t.Name})
}
