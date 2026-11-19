// Package catalog reads SQLite's own metadata (DESIGN §5) into a deterministic
// model: tables, columns, primary keys, indexes, foreign keys and triggers.
// It is the only place that learns identifiers from the engine, so the SQL
// adapters can quote names from here instead of trusting paths.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"dbvfs/internal/storage"
)

// Querier is the subset of database/sql both *sql.DB and *sql.Tx satisfy, so a
// catalog can be loaded inside the same read transaction as the data.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Index is one index as reported by index_list and index_xinfo.
type Index struct {
	Name       string   `json:"name"`
	Unique     bool     `json:"unique"`
	Origin     string   `json:"origin"`
	Partial    bool     `json:"partial"`
	Columns    []string `json:"columns"`
	Expression bool     `json:"expression,omitempty"`
}

// ForeignKey is one outgoing reference of a table.
type ForeignKey struct {
	Columns    []string `json:"columns"`
	Table      string   `json:"table"`
	RefColumns []string `json:"ref_columns"`
	OnUpdate   string   `json:"on_update"`
	OnDelete   string   `json:"on_delete"`
}

// Reference is an incoming foreign key: Table holds the columns that point at
// the described table.
type Reference struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// Table is a catalog table. Embedded storage.Table keeps the port's capability
// shape; the extra fields exist only for describe.
type Table struct {
	storage.Table
	Indexes     []Index
	ForeignKeys []ForeignKey
	Incoming    []Reference
	Triggers    []string
	Limitations []string
}

// Catalog is one consistent snapshot of the schema.
type Catalog struct {
	SchemaVersion int64
	Tables        []Table
}

// PrimaryKey comes from the embedded storage.Table: the key-order definition
// lives in the port, not here.

// Affinity returns SQLite's declared-type affinity (INTEGER, TEXT, REAL, BLOB
// or NUMERIC), which decides whether a key segment can be typed from a path
// (DESIGN §4).
func Affinity(declared string) string {
	u := strings.ToUpper(declared)
	switch {
	case strings.Contains(u, "INT"):
		return "INTEGER"
	case strings.Contains(u, "CHAR"), strings.Contains(u, "CLOB"), strings.Contains(u, "TEXT"):
		return "TEXT"
	case strings.Contains(u, "BLOB"), u == "":
		return "BLOB"
	case strings.Contains(u, "REAL"), strings.Contains(u, "FLOA"), strings.Contains(u, "DOUB"):
		return "REAL"
	}
	return "NUMERIC"
}

// Load reads the whole catalog. Views and virtual tables stay out of the MVP
// (D-20, PRD §1), as do SQLite's internal tables.
func Load(ctx context.Context, q Querier) (*Catalog, error) {
	var version int64
	if err := q.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&version); err != nil {
		return nil, fmt.Errorf("catalog schema version: %w", err)
	}

	tables, withoutRowID, err := loadTableList(ctx, q)
	if err != nil {
		return nil, err
	}

	for i := range tables {
		t := &tables[i]
		if err := loadColumns(ctx, q, t); err != nil {
			return nil, err
		}
		if err := loadIndexes(ctx, q, t); err != nil {
			return nil, err
		}
		if err := loadForeignKeys(ctx, q, t); err != nil {
			return nil, err
		}
		if err := loadTriggers(ctx, q, t); err != nil {
			return nil, err
		}
		assess(t, withoutRowID[i])
	}

	byName := make(map[string]int, len(tables))
	for i := range tables {
		byName[tables[i].Name] = i
	}
	for i := range tables {
		for _, fk := range tables[i].ForeignKeys {
			j, ok := byName[fk.Table]
			if !ok {
				continue // reference outside the exposed schema
			}
			tables[j].Incoming = append(tables[j].Incoming, Reference{
				Table:   tables[i].Name,
				Columns: fk.Columns,
			})
		}
	}

	return &Catalog{SchemaVersion: version, Tables: tables}, nil
}

// loadTableList uses PRAGMA table_list, which classifies tables, views, virtual
// tables and shadow tables in one query.
func loadTableList(ctx context.Context, q Querier) ([]Table, []bool, error) {
	rows, err := q.QueryContext(ctx, "PRAGMA table_list")
	if err != nil {
		return nil, nil, fmt.Errorf("catalog table list: %w", err)
	}
	defer rows.Close()

	type entry struct {
		name         string
		withoutRowID bool
	}
	var entries []entry
	for rows.Next() {
		var schema, name, typ string
		var ncol, wr, strict int
		if err := rows.Scan(&schema, &name, &typ, &ncol, &wr, &strict); err != nil {
			return nil, nil, fmt.Errorf("catalog table list: %w", err)
		}
		if schema != "main" || typ != "table" || strings.HasPrefix(name, "sqlite_") {
			continue
		}
		entries = append(entries, entry{name: name, withoutRowID: wr != 0})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("catalog table list: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	tables := make([]Table, len(entries))
	withoutRowID := make([]bool, len(entries))
	for i, e := range entries {
		tables[i].Name = e.name
		withoutRowID[i] = e.withoutRowID
	}
	return tables, withoutRowID, nil
}

func loadColumns(ctx context.Context, q Querier, t *Table) error {
	rows, err := q.QueryContext(ctx, "PRAGMA table_xinfo("+quoteIdent(t.Name)+")")
	if err != nil {
		return fmt.Errorf("catalog columns of %q: %w", t.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk, hidden int
		var name, declared string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &declared, &notNull, &dflt, &pk, &hidden); err != nil {
			return fmt.Errorf("catalog columns of %q: %w", t.Name, err)
		}
		// hidden==1 marks virtual-table internals; virtual tables are not
		// exposed, so a row like that cannot be selected or addressed.
		if hidden == 1 {
			t.Limitations = append(t.Limitations, fmt.Sprintf("column %q is hidden and not exposed", name))
			continue
		}
		t.Columns = append(t.Columns, storage.Column{
			Name:         name,
			DeclaredType: declared,
			PKOrder:      pk,
			Generated:    hidden == 2 || hidden == 3,
			Nullable:     notNull == 0,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog columns of %q: %w", t.Name, err)
	}
	return nil
}

func loadIndexes(ctx context.Context, q Querier, t *Table) error {
	rows, err := q.QueryContext(ctx, "PRAGMA index_list("+quoteIdent(t.Name)+")")
	if err != nil {
		return fmt.Errorf("catalog indexes of %q: %w", t.Name, err)
	}
	var indexes []Index
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return fmt.Errorf("catalog indexes of %q: %w", t.Name, err)
		}
		indexes = append(indexes, Index{Name: name, Unique: unique != 0, Origin: origin, Partial: partial != 0})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("catalog indexes of %q: %w", t.Name, err)
	}
	rows.Close()

	for i := range indexes {
		idx := &indexes[i]
		xrows, err := q.QueryContext(ctx, "PRAGMA index_xinfo("+quoteIdent(idx.Name)+")")
		if err != nil {
			return fmt.Errorf("catalog index %q: %w", idx.Name, err)
		}
		for xrows.Next() {
			var seqno, cid, desc, key int
			var name sql.NullString
			var coll string
			if err := xrows.Scan(&seqno, &cid, &name, &desc, &coll, &key); err != nil {
				xrows.Close()
				return fmt.Errorf("catalog index %q: %w", idx.Name, err)
			}
			if key == 0 {
				continue // auxiliary (rowid) column, not part of the key
			}
			if cid < 0 || !name.Valid {
				idx.Expression = true
				continue
			}
			idx.Columns = append(idx.Columns, name.String)
		}
		if err := xrows.Err(); err != nil {
			xrows.Close()
			return fmt.Errorf("catalog index %q: %w", idx.Name, err)
		}
		xrows.Close()
		if idx.Columns == nil {
			idx.Columns = []string{}
		}
	}
	t.Indexes = indexes
	return nil
}

func loadForeignKeys(ctx context.Context, q Querier, t *Table) error {
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteIdent(t.Name)+")")
	if err != nil {
		return fmt.Errorf("catalog foreign keys of %q: %w", t.Name, err)
	}
	defer rows.Close()

	byID := map[int]*ForeignKey{}
	var order []int
	for rows.Next() {
		var id, seq int
		var table, from, onUpdate, onDelete, match string
		var to sql.NullString
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return fmt.Errorf("catalog foreign keys of %q: %w", t.Name, err)
		}
		fk, ok := byID[id]
		if !ok {
			fk = &ForeignKey{Table: table, OnUpdate: onUpdate, OnDelete: onDelete}
			byID[id] = fk
			order = append(order, id)
		}
		fk.Columns = append(fk.Columns, from)
		if to.Valid {
			fk.RefColumns = append(fk.RefColumns, to.String)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog foreign keys of %q: %w", t.Name, err)
	}
	for _, id := range order {
		fk := byID[id]
		if fk.RefColumns == nil {
			fk.RefColumns = []string{}
		}
		t.ForeignKeys = append(t.ForeignKeys, *fk)
	}
	return nil
}

func loadTriggers(ctx context.Context, q Querier, t *Table) error {
	rows, err := q.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'trigger' AND tbl_name = ? ORDER BY name", t.Name)
	if err != nil {
		return fmt.Errorf("catalog triggers of %q: %w", t.Name, err)
	}
	defer rows.Close()
	t.Triggers = []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("catalog triggers of %q: %w", t.Name, err)
		}
		t.Triggers = append(t.Triggers, name)
	}
	return rows.Err()
}

// assess decides whether rows can be addressed by path (DESIGN §4). Anything
// that cannot be proven from the schema is denied rather than guessed.
func assess(t *Table, withoutRowID bool) {
	t.Readable = true
	t.Writable = false
	pk := t.PrimaryKey()
	if len(pk) == 0 {
		t.Navigable = false
		t.Limitations = append(t.Limitations, "no primary key: rows are not addressable by path")
		return
	}

	var reasons []string
	for _, c := range pk {
		switch Affinity(c.DeclaredType) {
		case "INTEGER", "TEXT":
		case "REAL", "BLOB":
			reasons = append(reasons, fmt.Sprintf("primary key column %q has declared type %q, which is not navigable", c.Name, c.DeclaredType))
		default:
			reasons = append(reasons, fmt.Sprintf("primary key column %q has ambiguous affinity, which is not navigable", c.Name))
		}
		if c.Generated {
			reasons = append(reasons, fmt.Sprintf("primary key column %q is generated", c.Name))
		}
	}

	// SQLite lets most primary keys hold NULL; only WITHOUT ROWID tables and a
	// single INTEGER PRIMARY KEY (the rowid alias) guarantee otherwise.
	nullSafe := withoutRowID || len(pk) == 1 && Affinity(pk[0].DeclaredType) == "INTEGER"
	if nullSafe {
		for i := range t.Columns {
			if t.Columns[i].PKOrder > 0 {
				t.Columns[i].Nullable = false
			}
		}
	} else {
		for _, c := range pk {
			if c.Nullable {
				reasons = append(reasons, "primary key columns may contain NULL")
				break
			}
		}
	}

	if len(reasons) > 0 {
		t.Navigable = false
		t.Limitations = append(t.Limitations, reasons...)
		return
	}
	t.Navigable = true
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
