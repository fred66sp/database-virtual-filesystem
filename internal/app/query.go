package app

import (
	"context"
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

// page is one bounded query result before rendering.
type page struct {
	table     storage.Table
	rows      []storage.Row
	truncated bool
	next      string
}

// Find filters a table with typed --where expressions (FR-05, D-11).
func (s Service) Find(ctx context.Context, p vfs.Path, where []string, limit int, token string) (FindData, bool, string, error) {
	pg, err := s.queryPage(ctx, p, "find", where, nil, limit, token)
	if err != nil {
		return FindData{}, false, "", err
	}
	rows, err := s.renderRows(p.Alias, pg.table, pg.rows)
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
	pg, err := s.queryPage(ctx, p, "export", where, nil, limit, token)
	if err != nil {
		return nil, false, "", err
	}
	rows, err := s.renderRows(p.Alias, pg.table, pg.rows)
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
	pg, err := s.queryPage(ctx, p, "grep", nil, search, limit, token)
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

// queryPage is the shared planner for find, grep and export: catalog lookup,
// typed filters, policy check, deterministic order, keyset continuation and one
// bounded read. A table without a stable identity is limited to its first page
// (D-12) and never receives a cursor.
func (s Service) queryPage(ctx context.Context, p vfs.Path, op string, where []string, search *string, limit int, token string) (page, error) {
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

		filters, err := parseWhere(where, t)
		if err != nil {
			return err
		}
		order := storage.DefaultOrder(t)

		var searchSpec *storage.Search
		if search != nil {
			columns := []string{}
			for _, c := range t.Columns {
				if catalog.Affinity(c.DeclaredType) == "TEXT" {
					columns = append(columns, c.Name)
				}
			}
			if len(columns) == 0 {
				return fmt.Errorf("%w: table %q has no TEXT columns to grep", storage.ErrUnsupported, t.Name)
			}
			searchSpec = &storage.Search{Text: searchText, Columns: columns}
		}

		q := storage.Query{
			Table:   t.Name,
			Filters: filters,
			Order:   order,
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
				order:   order,
				search:  searchText,
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
				order:   order,
				search:  searchText,
			}, rows[len(rows)-1].Key)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return pg, err
}

type cursorExpectation struct {
	mount   string
	table   string
	filters []storage.Filter
	order   []storage.OrderSpec
	search  string
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
		!slices.Equal(got.Filters, want.filters) || !slices.Equal(got.Order, want.order) {
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
		LastKey: last,
	})
}

// renderRows renders rows with the shared {path, fields, etag} shape. A row of a
// table without a stable identity has no address, so its path is the table's.
func (s Service) renderRows(alias string, t storage.Table, rows []storage.Row) ([]RowData, error) {
	out := make([]RowData, 0, len(rows))
	for _, row := range rows {
		path, err := rowPath(alias, t, row)
		if err != nil {
			return nil, err
		}
		out = append(out, RowData{Path: path, Fields: fieldMap(row), ETag: row.ETag})
	}
	return out, nil
}

func rowPath(alias string, t storage.Table, row storage.Row) (string, error) {
	if t.Navigable && len(row.Key) > 0 {
		return pathString(vfs.Path{Kind: vfs.KindRow, Alias: alias, Table: t.Name, Key: row.Key})
	}
	return pathString(vfs.Path{Kind: vfs.KindTable, Alias: alias, Table: t.Name})
}
