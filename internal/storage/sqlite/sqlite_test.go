package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"dbvfs/internal/cursor"
	"dbvfs/internal/storage"
)

const fixtureSchema = `
CREATE TABLE items (
	id INTEGER PRIMARY KEY,
	note TEXT,
	payload BLOB,
	big INTEGER,
	ratio REAL,
	label TEXT,
	doubled INTEGER GENERATED ALWAYS AS (big * 2) VIRTUAL
);
INSERT INTO items (id, note, payload, big, ratio, label) VALUES (1, NULL, x'00ff', 9223372036854775807, 1.25, 'a/b %7E ~e ñ');
INSERT INTO items (id, note, payload, big, ratio, label) VALUES (2, 'two', NULL, 42, 2.5, NULL);
INSERT INTO items (id, note, payload, big, ratio, label) VALUES (3, 'three', NULL, 43, 3.5, NULL);
CREATE TABLE fragile (id INTEGER PRIMARY KEY, value REAL);
INSERT INTO fragile VALUES (1, 9e999);
CREATE TABLE plain (a TEXT, b INTEGER);
INSERT INTO plain VALUES ('z', 2), ('a', 1);
CREATE TABLE pairs (a TEXT, b INTEGER, v TEXT, PRIMARY KEY (a, b));
INSERT INTO pairs VALUES ('a', 1, 'x'), ('a', 2, 'y'), ('b', 1, 'z');
CREATE TABLE guarded (id INTEGER PRIMARY KEY, v TEXT);
CREATE TRIGGER guarded_ins AFTER INSERT ON guarded BEGIN SELECT 1; END;
`

// openFixture creates a database with the test schema and returns its path plus
// a writable handle the tests use to change data behind the store.
func openFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	return path, db
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(Options{Path: path, Mode: "ro"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func readRow(t *testing.T, st *Store, table string, id int64) storage.Row {
	t.Helper()
	var row storage.Row
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		var err error
		row, err = rd.Get(context.Background(), table, storage.Key{{Kind: "integer", Data: strconv.FormatInt(id, 10)}})
		return err
	})
	if err != nil {
		t.Fatalf("Get %s %d: %v", table, id, err)
	}
	return row
}

func valueOf(row storage.Row, name string) storage.Value {
	for _, f := range row.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return storage.Value{Kind: "missing"}
}

func TestReadPreservesTypesAndPrecision(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	row := readRow(t, st, "items", 1)

	tests := []struct {
		column string
		want   storage.Value
	}{
		{"note", storage.Value{Kind: "null", Data: ""}},
		{"payload", storage.Value{Kind: "blob", Data: "AP8="}},
		{"big", storage.Value{Kind: "integer", Data: "9223372036854775807"}},
		{"ratio", storage.Value{Kind: "real", Data: "1.25"}},
		{"label", storage.Value{Kind: "text", Data: "a/b %7E ~e ñ"}},
	}
	for _, tc := range tests {
		if got := valueOf(row, tc.column); got != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.column, got, tc.want)
		}
	}
	if len(row.Key) != 1 || row.Key[0] != (storage.Value{Kind: "integer", Data: "1"}) {
		t.Errorf("key = %+v, want the integer primary key 1", row.Key)
	}
	if len(row.ETag) != len("sha256:")+64 || row.ETag[:7] != "sha256:" {
		t.Errorf("ETag = %q, want sha256:<64 hex>", row.ETag)
	}
}

func TestGeneratedColumnIsRead(t *testing.T) {
	path, _ := openFixture(t)
	row := readRow(t, openStore(t, path), "items", 3)
	if got := valueOf(row, "doubled"); got != (storage.Value{Kind: "integer", Data: "86"}) {
		t.Errorf("generated column = %+v, want integer 86", got)
	}
}

func TestETagIsStableThenTracksContent(t *testing.T) {
	path, db := openFixture(t)
	st := openStore(t, path)

	first := readRow(t, st, "items", 2).ETag
	second := readRow(t, st, "items", 2).ETag
	if first != second {
		t.Fatalf("ETag changed between identical reads: %q vs %q", first, second)
	}

	if _, err := db.Exec(`UPDATE items SET note = 'changed' WHERE id = 2`); err != nil {
		t.Fatalf("update fixture: %v", err)
	}
	third := readRow(t, st, "items", 2).ETag
	if third == first {
		t.Errorf("ETag did not change after the row changed (%q)", third)
	}
}

func TestGetMissingRow(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		_, err := rd.Get(context.Background(), "items", storage.Key{{Kind: "integer", Data: "999"}})
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(999) error = %v, want storage.ErrNotFound", err)
	}
}

func TestListOrdersByPrimaryKeyAndHonoursLimit(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	var rows []storage.Row
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		var err error
		rows, _, err = rd.List(context.Background(), storage.Query{Table: "items", Limit: 2})
		return err
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(rows))
	}
	if got := rows[0].Key[0].Data; got != "1" {
		t.Errorf("first key = %q, want 1", got)
	}
	if got := rows[1].Key[0].Data; got != "2" {
		t.Errorf("second key = %q, want 2", got)
	}
}

func TestListFiltersSearchAndKeyset(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	ctx := context.Background()

	atLeastTwo := []storage.Filter{{Column: "id", Op: ">=", Value: storage.Value{Kind: "integer", Data: "2"}}}
	var first []storage.Row
	err := st.Read(ctx, func(rd storage.Reader) error {
		var err error
		first, _, err = rd.List(ctx, storage.Query{Table: "items", Filters: atLeastTwo, Limit: 1})
		return err
	})
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if len(first) != 1 || first[0].Key[0].Data != "2" {
		t.Fatalf("first filtered page = %+v, want row 2", first)
	}

	position, err := cursor.EncodeKey(first[0].Key)
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}
	var second []storage.Row
	err = st.Read(ctx, func(rd storage.Reader) error {
		var err error
		second, _, err = rd.List(ctx, storage.Query{Table: "items", Filters: atLeastTwo, Limit: 1, Cursor: position})
		return err
	})
	if err != nil {
		t.Fatalf("List with cursor: %v", err)
	}
	if len(second) != 1 || second[0].Key[0].Data != "3" {
		t.Fatalf("second filtered page = %+v, want row 3", second)
	}

	var found []storage.Row
	err = st.Read(ctx, func(rd storage.Reader) error {
		var err error
		found, _, err = rd.List(ctx, storage.Query{Table: "items", Search: &storage.Search{Text: "tw", Columns: []string{"note"}}})
		return err
	})
	if err != nil {
		t.Fatalf("List search: %v", err)
	}
	if len(found) != 1 || found[0].Key[0].Data != "2" {
		t.Errorf("search tw = %+v, want row 2", found)
	}

	var none []storage.Row
	err = st.Read(ctx, func(rd storage.Reader) error {
		var err error
		none, _, err = rd.List(ctx, storage.Query{Table: "items", Filters: []storage.Filter{
			{Column: "note", Op: "=", Value: storage.Value{Kind: "text", Data: "x' OR 1=1 --"}},
		}})
		return err
	})
	if err != nil {
		t.Fatalf("List injection attempt: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("injection attempt returned %d rows, want 0", len(none))
	}

	if _, _, err := listWith(st, ctx, storage.Query{Table: "items", Filters: []storage.Filter{{Column: "bogus", Op: "="}}}); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("unknown filter column error = %v, want storage.ErrInvalid", err)
	}
}

func TestListDescendingKeyset(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	ctx := context.Background()
	desc := []storage.OrderSpec{{Column: "id", Desc: true}}

	first, _, err := listWith(st, ctx, storage.Query{Table: "items", Order: desc, Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 1 || first[0].Key[0].Data != "3" {
		t.Fatalf("first descending page = %+v, want row 3", first)
	}

	last := first[0]
	for i, want := range []string{"2", "1"} {
		position, err := cursor.EncodeKey(last.Key)
		if err != nil {
			t.Fatalf("EncodeKey: %v", err)
		}
		next, _, err := listWith(st, ctx, storage.Query{Table: "items", Order: desc, Limit: 1, Cursor: position})
		if err != nil {
			t.Fatalf("page %d: %v", i+2, err)
		}
		if len(next) != 1 || next[0].Key[0].Data != want {
			t.Fatalf("page %d = %+v, want row %s", i+2, next, want)
		}
		last = next[0]
	}
}

func TestListCompositeKeyKeyset(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	ctx := context.Background()

	first, _, err := listWith(st, ctx, storage.Query{Table: "pairs", Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 1 || first[0].Key[0].Data != "a" || first[0].Key[1].Data != "1" {
		t.Fatalf("first pair = %+v, want (a,1)", first)
	}
	position, err := cursor.EncodeKey(first[0].Key)
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}
	second, _, err := listWith(st, ctx, storage.Query{Table: "pairs", Limit: 1, Cursor: position})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 1 || second[0].Key[0].Data != "a" || second[0].Key[1].Data != "2" {
		t.Fatalf("second pair = %+v, want (a,2)", second)
	}
	position, err = cursor.EncodeKey(second[0].Key)
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}
	third, _, err := listWith(st, ctx, storage.Query{Table: "pairs", Limit: 1, Cursor: position})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(third) != 1 || third[0].Key[0].Data != "b" || third[0].Key[1].Data != "1" {
		t.Fatalf("third pair = %+v, want (b,1)", third)
	}
}

func TestListNonNavigableOrdersEveryColumn(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	rows, _, err := listWith(st, context.Background(), storage.Query{Table: "plain", Limit: 10})
	if err != nil {
		t.Fatalf("List(plain): %v", err)
	}
	if len(rows) != 2 || valueOf(rows[0], "a").Data != "a" || valueOf(rows[1], "a").Data != "z" {
		t.Errorf("plain rows = %+v, want a then z by every column", rows)
	}
}

func listWith(st *Store, ctx context.Context, q storage.Query) ([]storage.Row, string, error) {
	var rows []storage.Row
	var next string
	err := st.Read(ctx, func(rd storage.Reader) error {
		var err error
		rows, next, err = rd.List(ctx, q)
		return err
	})
	return rows, next, err
}

func TestCatalogRevalidatesAfterSchemaChange(t *testing.T) {
	path, db := openFixture(t)
	st := openStore(t, path)

	var columns []string
	readColumns := func() []string {
		var out []string
		err := st.Read(context.Background(), func(rd storage.Reader) error {
			tables, err := rd.Catalog(context.Background())
			if err != nil {
				return err
			}
			for _, table := range tables {
				if table.Name == "items" {
					for _, c := range table.Columns {
						out = append(out, c.Name)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Catalog: %v", err)
		}
		return out
	}

	columns = readColumns()
	for _, c := range columns {
		if c == "extra" {
			t.Fatal("extra column exists before ALTER")
		}
	}

	if _, err := db.Exec(`ALTER TABLE items ADD COLUMN extra TEXT`); err != nil {
		t.Fatalf("alter fixture: %v", err)
	}

	columns = readColumns()
	found := false
	for _, c := range columns {
		if c == "extra" {
			found = true
		}
	}
	if !found {
		t.Errorf("columns after ALTER = %v, want extra; catalog cache was not revalidated", columns)
	}
}

func TestNonFiniteRealIsUnsupported(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		_, err := rd.Get(context.Background(), "fragile", storage.Key{{Kind: "integer", Data: "1"}})
		return err
	})
	if !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("non-finite REAL error = %v, want storage.ErrUnsupported", err)
	}
}

func TestOpenNeverCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	for _, mode := range []string{"ro", "rw"} {
		if _, err := Open(Options{Path: path, Mode: mode}); err == nil {
			t.Errorf("Open(%s) on a missing file succeeded", mode)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Open(%s) created %s (err %v)", mode, path, err)
		}
	}
}

// TestWriteMutatesInsideImmediateTransaction covers touch and write through the
// storage port: explicit INSERT, ETag-checked UPDATE, rollback on a pre-commit
// error, and the conservative refusals from DESIGN §8.
func TestWriteMutatesInsideImmediateTransaction(t *testing.T) {
	path, _ := openFixture(t)
	st, err := Open(Options{Path: path, Mode: "rw"})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	// touch: omitted INTEGER PRIMARY KEY comes from the rowid alias.
	var inserted storage.Row
	err = st.Write(ctx, func(tx storage.Tx) error {
		var err error
		inserted, err = tx.Apply(ctx, storage.Mutation{
			Kind:   "touch",
			Table:  "items",
			Fields: map[string]storage.Value{"note": {Kind: "text", Data: "new"}},
		})
		return err
	})
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if len(inserted.Key) != 1 || inserted.Key[0].Data != "4" {
		t.Errorf("inserted key = %+v, want integer 4", inserted.Key)
	}
	if got := valueOf(inserted, "note"); got != (storage.Value{Kind: "text", Data: "new"}) {
		t.Errorf("inserted note = %+v, want new", got)
	}

	// write: a partial UPDATE with the current ETag.
	before := readRow(t, st, "items", 2)
	var updated storage.Row
	err = st.Write(ctx, func(tx storage.Tx) error {
		var err error
		updated, err = tx.Apply(ctx, storage.Mutation{
			Kind: "write", Table: "items", Key: before.Key,
			Fields:  map[string]storage.Value{"note": {Kind: "text", Data: "changed"}},
			IfMatch: before.ETag,
		})
		return err
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := valueOf(updated, "note"); got != (storage.Value{Kind: "text", Data: "changed"}) {
		t.Errorf("updated note = %+v, want changed", got)
	}
	if updated.ETag == before.ETag {
		t.Error("ETag did not change after the update")
	}
	if got := valueOf(readRow(t, st, "items", 2), "big"); got != (storage.Value{Kind: "integer", Data: "42"}) {
		t.Errorf("untouched column big = %+v, want 42", got)
	}

	// An obsolete ETag is E_CONFLICT and leaves the row untouched.
	err = st.Write(ctx, func(tx storage.Tx) error {
		_, err := tx.Apply(ctx, storage.Mutation{
			Kind: "write", Table: "items", Key: before.Key,
			Fields:  map[string]storage.Value{"note": {Kind: "text", Data: "late"}},
			IfMatch: before.ETag,
		})
		return err
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale ETag error = %v, want storage.ErrConflict", err)
	}
	if got := valueOf(readRow(t, st, "items", 2), "note"); got != (storage.Value{Kind: "text", Data: "changed"}) {
		t.Errorf("row changed after the conflict: note = %+v", got)
	}

	// A failure after Apply still rolls the whole transaction back.
	boom := errors.New("abort after apply")
	err = st.Write(ctx, func(tx storage.Tx) error {
		if _, err := tx.Apply(ctx, storage.Mutation{
			Kind:   "touch",
			Table:  "items",
			Fields: map[string]storage.Value{"note": {Kind: "text", Data: "rolled back"}},
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback error = %v, want the caller error", err)
	}
	assertNoRow(t, st, "items", storage.Key{{Kind: "integer", Data: "5"}})
}

func TestWriteInsertWithDefaults(t *testing.T) {
	path, _ := openFixture(t)
	st, err := Open(Options{Path: path, Mode: "rw"})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	var row storage.Row
	err = st.Write(ctx, func(tx storage.Tx) error {
		var err error
		row, err = tx.Apply(ctx, storage.Mutation{Kind: "touch", Table: "items"})
		return err
	})
	if err != nil {
		t.Fatalf("touch with defaults: %v", err)
	}
	if len(row.Key) != 1 || row.Key[0].Data != "4" {
		t.Errorf("key = %+v, want integer 4 from the rowid alias", row.Key)
	}
	if got := valueOf(row, "note"); got != (storage.Value{Kind: "null"}) {
		t.Errorf("note = %+v, want the native default NULL", got)
	}
}

func TestWriteRefusesUnsafeTargets(t *testing.T) {
	path, _ := openFixture(t)
	st, err := Open(Options{Path: path, Mode: "rw"})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	row := readRow(t, st, "items", 2)
	tests := []struct {
		name string
		m    storage.Mutation
		want error
	}{
		{
			name: "generated column",
			m:    storage.Mutation{Kind: "write", Table: "items", Key: row.Key, IfMatch: row.ETag, Fields: map[string]storage.Value{"doubled": {Kind: "integer", Data: "1"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "primary key update",
			m:    storage.Mutation{Kind: "write", Table: "items", Key: row.Key, IfMatch: row.ETag, Fields: map[string]storage.Value{"id": {Kind: "integer", Data: "9"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "non-navigable table",
			m:    storage.Mutation{Kind: "touch", Table: "plain", Fields: map[string]storage.Value{"a": {Kind: "text", Data: "x"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "write trigger",
			m:    storage.Mutation{Kind: "touch", Table: "guarded", Fields: map[string]storage.Value{"v": {Kind: "text", Data: "x"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "unknown column",
			m:    storage.Mutation{Kind: "touch", Table: "items", Fields: map[string]storage.Value{"bogus": {Kind: "text", Data: "x"}}},
			want: storage.ErrInvalid,
		},
		{
			name: "missing ETag",
			m:    storage.Mutation{Kind: "write", Table: "items", Key: row.Key, Fields: map[string]storage.Value{"note": {Kind: "text", Data: "x"}}},
			want: storage.ErrInvalid,
		},
		{
			name: "constraint violation",
			m:    storage.Mutation{Kind: "touch", Table: "items", Fields: map[string]storage.Value{"id": {Kind: "integer", Data: "1"}}},
			want: storage.ErrConstraint,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := st.Write(ctx, func(tx storage.Tx) error {
				_, err := tx.Apply(ctx, tc.m)
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Apply error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestWriteRefusesCascadingForeignKeys is DESIGN §8: an FK action would rewrite
// rows outside the plan, so it is denied conservatively. NO ACTION and RESTRICT
// stay allowed.
func TestWriteRefusesCascadingForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	const schema = `
CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO parents (id, name) VALUES (1, 'root');
CREATE TABLE children (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO children (id, parent_id) VALUES (1, 1);
CREATE TABLE pinned (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE RESTRICT ON UPDATE RESTRICT
);
INSERT INTO pinned (id, parent_id) VALUES (1, 1);
CREATE TABLE softened (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER DEFAULT NULL REFERENCES parents(id) ON DELETE SET NULL ON UPDATE SET DEFAULT
);
INSERT INTO softened (id, parent_id) VALUES (1, 1);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	st, err := Open(Options{Path: path, Mode: "rw"})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	children := readRow(t, st, "children", 1)
	pinned := readRow(t, st, "pinned", 1)
	parents := readRow(t, st, "parents", 1)

	tests := []struct {
		name string
		m    storage.Mutation
		want error
	}{
		{
			name: "touch a child with a cascading foreign key",
			m:    storage.Mutation{Kind: "touch", Table: "children", Fields: map[string]storage.Value{"parent_id": {Kind: "integer", Data: "1"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "write a child with a cascading foreign key",
			m:    storage.Mutation{Kind: "write", Table: "children", Key: children.Key, IfMatch: children.ETag, Fields: map[string]storage.Value{"parent_id": {Kind: "integer", Data: "1"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "write a table referenced with cascade",
			m:    storage.Mutation{Kind: "write", Table: "parents", Key: parents.Key, IfMatch: parents.ETag, Fields: map[string]storage.Value{"name": {Kind: "text", Data: "changed"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "touch a table referenced with cascade",
			m:    storage.Mutation{Kind: "touch", Table: "parents", Fields: map[string]storage.Value{"name": {Kind: "text", Data: "second"}}},
		},
		{
			name: "touch a child with a set null/default foreign key",
			m:    storage.Mutation{Kind: "touch", Table: "softened", Fields: map[string]storage.Value{"parent_id": {Kind: "integer", Data: "1"}}},
			want: storage.ErrUnsupported,
		},
		{
			name: "touch a child with a restrict foreign key",
			m:    storage.Mutation{Kind: "touch", Table: "pinned", Fields: map[string]storage.Value{"parent_id": {Kind: "integer", Data: "1"}}},
		},
		{
			name: "write a child with a restrict foreign key",
			m:    storage.Mutation{Kind: "write", Table: "pinned", Key: pinned.Key, IfMatch: pinned.ETag, Fields: map[string]storage.Value{"parent_id": {Kind: "integer", Data: "1"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := st.Write(ctx, func(tx storage.Tx) error {
				_, err := tx.Apply(ctx, tc.m)
				return err
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Apply error = %v, want success", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Apply error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestForeignKeysEnabledOnEveryConnection checks DESIGN §8: the pragma is on for
// the read transaction and for the dedicated write connection the mutation uses.
func TestForeignKeysEnabledOnEveryConnection(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)
	ctx := context.Background()

	pragma := func(t *testing.T, q queryExecer) int {
		t.Helper()
		var n int
		if err := q.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&n); err != nil {
			t.Fatalf("read PRAGMA foreign_keys: %v", err)
		}
		return n
	}

	var read int
	if err := st.Read(ctx, func(rd storage.Reader) error {
		read = pragma(t, rd.(*reader).q)
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	rw, err := Open(Options{Path: path, Mode: "rw"})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { rw.Close() })
	var write int
	if err := rw.Write(ctx, func(tx storage.Tx) error {
		write = pragma(t, tx.(*reader).q)
		return nil
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if read != 1 || write != 1 {
		t.Errorf("foreign_keys read/write connections = %d/%d, want 1/1", read, write)
	}
}

func assertNoRow(t *testing.T, st *Store, table string, key storage.Key) {
	t.Helper()
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		_, err := rd.Get(context.Background(), table, key)
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get %s %+v error = %v, want storage.ErrNotFound", table, key, err)
	}
}

func TestDSNCarriesModePragmasAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		wantQuery  bool
		wantPolicy string
	}{{"ro", true, "query_only(1)"}, {"rw", false, ""}} {
		u, err := url.Parse(dsn("/tmp/x.db", tc.mode, 5*time.Second))
		if err != nil {
			t.Fatalf("parse dsn: %v", err)
		}
		if got := u.Query().Get("mode"); got != tc.mode {
			t.Errorf("mode = %q, want %q", got, tc.mode)
		}
		pragmas := u.Query()["_pragma"]
		joined := strings.Join(pragmas, "&")
		if !strings.Contains(joined, "busy_timeout(5000)") {
			t.Errorf("pragmas = %v, want busy_timeout(5000) from the 5s timeout", pragmas)
		}
		if !strings.Contains(joined, "foreign_keys(1)") {
			t.Errorf("pragmas = %v, want foreign_keys(1)", pragmas)
		}
		if got := strings.Contains(joined, tc.wantPolicy); tc.wantPolicy != "" && !got {
			t.Errorf("pragmas = %v, want %s", pragmas, tc.wantPolicy)
		}
		if tc.mode == "rw" && strings.Contains(joined, "query_only") {
			t.Errorf("rw pragmas = %v, must not force query_only", pragmas)
		}
	}
}
