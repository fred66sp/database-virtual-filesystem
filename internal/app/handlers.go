package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dbvfs/internal/catalog"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

const (
	defaultLimit = 1000
	// defaultTreeDepth is the documented depth of `tree` without --depth: two
	// levels below PATH. At the root that is mounts→tables, and under a mount
	// it is tables→rows, which keeps the default useful without dumping every
	// field of every row.
	defaultTreeDepth = 2
)

type entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
}

type listData struct {
	Path    string  `json:"path"`
	Entries []entry `json:"entries"`
}

// treeEntry is one node of `tree`. Children is nil when --depth stopped the
// expansion and non-nil (possibly empty) when the node was expanded: that
// distinguishes "deeper levels not requested" from "container with no
// children", such as a table without a navigable key (DESIGN §7).
type treeEntry struct {
	Name     string       `json:"name"`
	Type     string       `json:"type"`
	Path     string       `json:"path"`
	Children *[]treeEntry `json:"children,omitempty"`
}

type treeData struct {
	Path    string      `json:"path"`
	Entries []treeEntry `json:"entries"`
}

type rowData struct {
	Path   string                   `json:"path"`
	Fields map[string]storage.Value `json:"fields"`
	ETag   string                   `json:"etag"`
}

type statData struct {
	Type      string                   `json:"type"`
	Path      string                   `json:"path"`
	Columns   []string                 `json:"columns,omitempty"`
	Keys      map[string]storage.Value `json:"keys,omitempty"`
	Navigable *bool                    `json:"navigable,omitempty"`
}

type describeColumn struct {
	Name         string `json:"name"`
	DeclaredType string `json:"declared_type"`
	PrimaryKey   int    `json:"primary_key,omitempty"`
	Nullable     bool   `json:"nullable"`
	Generated    bool   `json:"generated"`
}

type capabilities struct {
	Readable  bool `json:"readable"`
	Writable  bool `json:"writable"`
	Navigable bool `json:"navigable"`
}

type describeData struct {
	Path         string               `json:"path"`
	Table        string               `json:"table"`
	Columns      []describeColumn     `json:"columns"`
	PrimaryKey   []string             `json:"primary_key"`
	Indexes      []catalog.Index      `json:"indexes"`
	ForeignKeys  []catalog.ForeignKey `json:"foreign_keys"`
	Incoming     []catalog.Reference  `json:"incoming_references"`
	Triggers     []string             `json:"triggers"`
	Capabilities capabilities         `json:"capabilities"`
	Limitations  []string             `json:"limitations"`
}

// LS lists the direct children of a path: mounts at the root, tables under a
// mount, row keys under a table and fields under a row.
func (s Service) LS(ctx context.Context, p vfs.Path, limit int) (any, bool, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	switch p.Kind {
	case vfs.KindRoot:
		cfg, err := state.Load(s.ConfigDir)
		if err != nil {
			return nil, false, err
		}
		aliases := cfg.Aliases()
		truncated := len(aliases) > limit
		if truncated {
			aliases = aliases[:limit]
		}
		entries := make([]entry, 0, len(aliases))
		for _, alias := range aliases {
			entries = append(entries, entry{Name: alias, Type: "mount", Path: "/" + alias})
		}
		return listData{Path: "/", Entries: entries}, truncated, nil

	case vfs.KindMount:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, false, err
		}
		defer st.Close()
		var data listData
		var truncated bool
		err = st.Read(ctx, func(rd storage.Reader) error {
			tables, err := rd.Catalog(ctx)
			if err != nil {
				return err
			}
			truncated = len(tables) > limit
			if truncated {
				tables = tables[:limit]
			}
			entries := make([]entry, 0, len(tables))
			for _, t := range tables {
				path, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: t.Name})
				if err != nil {
					return err
				}
				entries = append(entries, entry{Name: t.Name, Type: "table", Path: path})
			}
			data = listData{Path: mountPath(p.Alias), Entries: entries}
			return nil
		})
		return data, truncated, err

	case vfs.KindTable:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, false, err
		}
		defer st.Close()
		var data listData
		var truncated bool
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			if !t.Navigable {
				return fmt.Errorf("%w: table %q has no navigable primary key", storage.ErrUnsupported, t.Name)
			}
			rows, _, err := rd.List(ctx, storage.Query{Table: t.Name, Limit: limit + 1})
			if err != nil {
				return err
			}
			truncated = len(rows) > limit
			if truncated {
				rows = rows[:limit]
			}
			entries := make([]entry, 0, len(rows))
			for _, row := range rows {
				path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: t.Name, Key: row.Key})
				if err != nil {
					return err
				}
				entries = append(entries, entry{Name: lastSegment(path), Type: "row", Path: path})
			}
			path, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: p.Table})
			if err != nil {
				return err
			}
			data = listData{Path: path, Entries: entries}
			return nil
		})
		return data, truncated, err

	case vfs.KindRow:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, false, err
		}
		defer st.Close()
		var data listData
		var truncated bool
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			key, err := resolveKey(p.Key, t)
			if err != nil {
				return err
			}
			row, err := rd.Get(ctx, t.Name, key)
			if err != nil {
				return err
			}
			truncated = len(row.Fields) > limit
			fields := row.Fields
			if truncated {
				fields = fields[:limit]
			}
			entries := make([]entry, 0, len(fields))
			rowPath, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: t.Name, Key: row.Key})
			if err != nil {
				return err
			}
			for _, f := range fields {
				path, err := pathString(vfs.Path{Kind: vfs.KindField, Alias: p.Alias, Table: t.Name, Key: row.Key, Field: f.Name})
				if err != nil {
					return err
				}
				entries = append(entries, entry{Name: f.Name, Type: "field", Path: path})
			}
			data = listData{Path: rowPath, Entries: entries}
			return nil
		})
		return data, truncated, err
	}
	return nil, false, fmt.Errorf("%w: fields have no children", storage.ErrUnsupported)
}

// Tree lists the descendants of a path up to depth levels, capping every
// listing at limit records so no level is ever walked without a bound
// (DESIGN §7). The default depth is two levels below PATH (see
// defaultTreeDepth). Every listed entry is expanded only while levels remain;
// a non-navigable table is expanded into an empty container instead of an
// error, as §7 requires.
func (s Service) Tree(ctx context.Context, p vfs.Path, depth, limit int) (any, bool, error) {
	if depth <= 0 {
		depth = defaultTreeDepth
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	var truncated bool
	entries, err := s.treeEntries(ctx, p, depth, limit, &truncated)
	if err != nil {
		return nil, false, err
	}
	path, err := pathString(p)
	if err != nil {
		return nil, false, err
	}
	return treeData{Path: path, Entries: entries}, truncated, nil
}

// treeEntries returns the children of p, each expanded to depth-1 more levels.
func (s Service) treeEntries(ctx context.Context, p vfs.Path, depth, limit int, truncated *bool) ([]treeEntry, error) {
	if depth <= 0 {
		return []treeEntry{}, nil
	}
	switch p.Kind {
	case vfs.KindRoot:
		cfg, err := state.Load(s.ConfigDir)
		if err != nil {
			return nil, err
		}
		aliases := cfg.Aliases()
		if len(aliases) > limit {
			aliases = aliases[:limit]
			*truncated = true
		}
		entries := make([]treeEntry, 0, len(aliases))
		for _, alias := range aliases {
			entry := treeEntry{Name: alias, Type: "mount", Path: mountPath(alias)}
			if depth > 1 {
				kids, err := s.treeMount(ctx, alias, depth-1, limit, truncated)
				if err != nil {
					return nil, err
				}
				entry.Children = &kids
			}
			entries = append(entries, entry)
		}
		return entries, nil

	case vfs.KindMount:
		return s.treeMount(ctx, p.Alias, depth, limit, truncated)

	case vfs.KindTable:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var entries []treeEntry
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			entries, err = treeTable(ctx, rd, p.Alias, t, depth, limit, truncated)
			return err
		})
		return entries, err

	case vfs.KindRow:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var entries []treeEntry
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			key, err := resolveKey(p.Key, t)
			if err != nil {
				return err
			}
			row, err := rd.Get(ctx, t.Name, key)
			if err != nil {
				return err
			}
			entries, err = treeRow(p.Alias, t.Name, row, limit, truncated)
			return err
		})
		return entries, err

	case vfs.KindField:
		return []treeEntry{}, nil
	}
	return nil, fmt.Errorf("%w: unknown path kind", storage.ErrUnsupported)
}

// treeMount lists a mount's tables and, while levels remain, their rows. The
// store is opened once per mount, not once per descendant.
func (s Service) treeMount(ctx context.Context, alias string, depth, limit int, truncated *bool) ([]treeEntry, error) {
	if depth <= 0 {
		return nil, nil
	}
	st, err := s.open(alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	var entries []treeEntry
	err = st.Read(ctx, func(rd storage.Reader) error {
		tables, err := rd.Catalog(ctx)
		if err != nil {
			return err
		}
		if len(tables) > limit {
			tables = tables[:limit]
			*truncated = true
		}
		entries = make([]treeEntry, 0, len(tables))
		for _, t := range tables {
			path, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: alias, Table: t.Name})
			if err != nil {
				return err
			}
			entry := treeEntry{Name: t.Name, Type: "table", Path: path}
			if depth > 1 {
				kids, err := treeTable(ctx, rd, alias, t, depth-1, limit, truncated)
				if err != nil {
					return err
				}
				entry.Children = &kids
			}
			entries = append(entries, entry)
		}
		return nil
	})
	return entries, err
}

// treeTable returns a table's rows, plus their fields when levels remain.
func treeTable(ctx context.Context, rd storage.Reader, alias string, t storage.Table, depth, limit int, truncated *bool) ([]treeEntry, error) {
	if !t.Navigable {
		return []treeEntry{}, nil
	}
	rows, _, err := rd.List(ctx, storage.Query{Table: t.Name, Limit: limit + 1})
	if err != nil {
		return nil, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		*truncated = true
	}
	entries := make([]treeEntry, 0, len(rows))
	for _, row := range rows {
		path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: alias, Table: t.Name, Key: row.Key})
		if err != nil {
			return nil, err
		}
		entry := treeEntry{Name: lastSegment(path), Type: "row", Path: path}
		if depth > 1 {
			kids, err := treeRow(alias, t.Name, row, limit, truncated)
			if err != nil {
				return nil, err
			}
			entry.Children = &kids
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// treeRow returns a row's fields; fields are leaves, so no level follows them.
func treeRow(alias, table string, row storage.Row, limit int, truncated *bool) ([]treeEntry, error) {
	fields := row.Fields
	if len(fields) > limit {
		fields = fields[:limit]
		*truncated = true
	}
	entries := make([]treeEntry, 0, len(fields))
	for _, f := range fields {
		path, err := pathString(vfs.Path{Kind: vfs.KindField, Alias: alias, Table: table, Key: row.Key, Field: f.Name})
		if err != nil {
			return nil, err
		}
		entries = append(entries, treeEntry{Name: f.Name, Type: "field", Path: path})
	}
	return entries, nil
}

// Stat reports what a path is, using the shapes from DESIGN §10.
func (s Service) Stat(ctx context.Context, p vfs.Path) (any, error) {
	switch p.Kind {
	case vfs.KindRoot:
		return statData{Type: "root", Path: "/"}, nil

	case vfs.KindMount:
		cfg, err := state.Load(s.ConfigDir)
		if err != nil {
			return nil, err
		}
		m, ok := cfg.Mounts[p.Alias]
		if !ok {
			return nil, fmt.Errorf("%w: mount %q is not registered", storage.ErrNotFound, p.Alias)
		}
		// stat describes the resource, not just the registry entry: a file
		// removed or replaced after mount answers here like any read would.
		if err := checkMountFile(m.File); err != nil {
			return nil, err
		}
		return statData{Type: "mount", Path: mountPath(p.Alias)}, nil

	case vfs.KindTable:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var data statData
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			columns := make([]string, len(t.Columns))
			for i, c := range t.Columns {
				columns[i] = c.Name
			}
			path, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: p.Table})
			if err != nil {
				return err
			}
			navigable := t.Navigable
			data = statData{Type: "table", Path: path, Columns: columns, Navigable: &navigable}
			return nil
		})
		return data, err

	case vfs.KindRow:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var data statData
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			key, err := resolveKey(p.Key, t)
			if err != nil {
				return err
			}
			row, err := rd.Get(ctx, t.Name, key)
			if err != nil {
				return err
			}
			path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: t.Name, Key: row.Key})
			if err != nil {
				return err
			}
			data = statData{Type: "row", Path: path, Keys: keyMap(row, t)}
			return nil
		})
		return data, err

	case vfs.KindField:
		st, err := s.open(p.Alias)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var data statData
		err = st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			key, err := resolveKey(p.Key, t)
			if err != nil {
				return err
			}
			row, err := rd.Get(ctx, t.Name, key)
			if err != nil {
				return err
			}
			if _, ok := fieldValue(row, p.Field); !ok {
				return fmt.Errorf("%w: field %q", storage.ErrNotFound, p.Field)
			}
			path, err := pathString(vfs.Path{Kind: vfs.KindField, Alias: p.Alias, Table: t.Name, Key: row.Key, Field: p.Field})
			if err != nil {
				return err
			}
			data = statData{Type: "field", Path: path}
			return nil
		})
		return data, err
	}
	return nil, fmt.Errorf("%w: unknown path kind", storage.ErrUnsupported)
}

// Describe exposes the catalog facts and the capabilities of a table.
func (s Service) Describe(ctx context.Context, p vfs.Path) (describeData, error) {
	if p.Kind != vfs.KindTable {
		return describeData{}, fmt.Errorf("%w: describe needs a table path /ALIAS/TABLE", storage.ErrUnsupported)
	}
	st, err := s.open(p.Alias)
	if err != nil {
		return describeData{}, err
	}
	defer st.Close()

	t, err := st.Table(ctx, p.Table)
	if err != nil {
		return describeData{}, err
	}

	path, err := pathString(vfs.Path{Kind: vfs.KindTable, Alias: p.Alias, Table: p.Table})
	if err != nil {
		return describeData{}, err
	}
	data := describeData{
		Path:        path,
		Table:       t.Name,
		Columns:     make([]describeColumn, len(t.Columns)),
		PrimaryKey:  make([]string, 0),
		Indexes:     t.Indexes,
		ForeignKeys: t.ForeignKeys,
		Incoming:    t.Incoming,
		Triggers:    t.Triggers,
		Capabilities: capabilities{
			Readable:  t.Readable,
			Writable:  false,
			Navigable: t.Navigable,
		},
		Limitations: append([]string{}, t.Limitations...),
	}
	if data.Indexes == nil {
		data.Indexes = []catalog.Index{}
	}
	if data.ForeignKeys == nil {
		data.ForeignKeys = []catalog.ForeignKey{}
	}
	if data.Incoming == nil {
		data.Incoming = []catalog.Reference{}
	}
	if data.Triggers == nil {
		data.Triggers = []string{}
	}
	for i, c := range t.Columns {
		data.Columns[i] = describeColumn{
			Name:         c.Name,
			DeclaredType: c.DeclaredType,
			PrimaryKey:   c.PKOrder,
			Nullable:     c.Nullable,
			Generated:    c.Generated,
		}
	}
	for _, c := range t.PrimaryKey() {
		data.PrimaryKey = append(data.PrimaryKey, c.Name)
	}
	data.Limitations = append(data.Limitations, "writes are not available in this release")
	return data, nil
}

// Cat returns a whole row as {path, fields, etag} or a single typed field as
// {kind, data} (DESIGN §10).
func (s Service) Cat(ctx context.Context, p vfs.Path) (any, error) {
	switch p.Kind {
	case vfs.KindRow, vfs.KindField:
	case vfs.KindTable:
		return nil, fmt.Errorf("%w: cat needs a row or field path", storage.ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: cat needs a row or field path", storage.ErrUnsupported)
	}

	st, err := s.open(p.Alias)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	var data any
	err = st.Read(ctx, func(rd storage.Reader) error {
		t, err := findTable(ctx, rd, p.Table)
		if err != nil {
			return err
		}
		key, err := resolveKey(p.Key, t)
		if err != nil {
			return err
		}
		row, err := rd.Get(ctx, t.Name, key)
		if err != nil {
			return err
		}
		if p.Kind == vfs.KindField {
			value, ok := fieldValue(row, p.Field)
			if !ok {
				return fmt.Errorf("%w: field %q", storage.ErrNotFound, p.Field)
			}
			data = value
			return nil
		}
		path, err := pathString(vfs.Path{Kind: vfs.KindRow, Alias: p.Alias, Table: t.Name, Key: row.Key})
		if err != nil {
			return err
		}
		data = rowData{Path: path, Fields: fieldMap(row), ETag: row.ETag}
		return nil
	})
	return data, err
}

// resolveKey types the path key against the declared primary key (DESIGN §4).
// A plain segment is TEXT in the codec; INTEGER affinity columns interpret its
// canonical decimal, everything else cannot be addressed by path.
func resolveKey(key storage.Key, t storage.Table) (storage.Key, error) {
	pk := t.PrimaryKey()
	if len(key) != len(pk) {
		return nil, fmt.Errorf("%w: key does not match the primary key of table %q", storage.ErrNotFound, t.Name)
	}
	out := make(storage.Key, len(pk))
	for i, c := range pk {
		v := key[i]
		switch catalog.Affinity(c.DeclaredType) {
		case "INTEGER":
			n, err := strconv.ParseInt(v.Data, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != v.Data {
				return nil, fmt.Errorf("%w: key %q is not a canonical integer", storage.ErrNotFound, v.Data)
			}
			out[i] = storage.Value{Kind: "integer", Data: strconv.FormatInt(n, 10)}
		case "TEXT":
			if v.Kind != "text" {
				return nil, fmt.Errorf("%w: key kind %q does not match a TEXT primary key", storage.ErrNotFound, v.Kind)
			}
			out[i] = v
		default:
			return nil, fmt.Errorf("%w: primary key column %q is not navigable", storage.ErrUnsupported, c.Name)
		}
	}
	return out, nil
}

func findTable(ctx context.Context, rd storage.Reader, name string) (storage.Table, error) {
	tables, err := rd.Catalog(ctx)
	if err != nil {
		return storage.Table{}, err
	}
	for _, t := range tables {
		if t.Name == name {
			return t, nil
		}
	}
	return storage.Table{}, fmt.Errorf("%w: table %q", storage.ErrNotFound, name)
}

func fieldMap(row storage.Row) map[string]storage.Value {
	fields := make(map[string]storage.Value, len(row.Fields))
	for _, f := range row.Fields {
		fields[f.Name] = f.Value
	}
	return fields
}

func fieldValue(row storage.Row, name string) (storage.Value, bool) {
	for _, f := range row.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return storage.Value{}, false
}

func keyMap(row storage.Row, t storage.Table) map[string]storage.Value {
	keys := make(map[string]storage.Value)
	for i, c := range t.Columns {
		if c.PKOrder > 0 && i < len(row.Fields) {
			keys[c.Name] = row.Fields[i].Value
		}
	}
	return keys
}

func pathString(p vfs.Path) (string, error) {
	s, err := p.Canonical()
	if err != nil {
		return "", fmt.Errorf("render virtual path: %w", err)
	}
	return s, nil
}

func lastSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func mountPath(alias string) string { return "/" + alias }
