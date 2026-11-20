package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"dbvfs/internal/cursor"
	"dbvfs/internal/storage"
)

// ref02Schema exercises REF-02: duplicate user-column values (the tie-break is
// what makes the order total), NULLs in both directions, and a column with no
// declared type so it holds SQLite's mixed storage classes.
const ref02Schema = `
CREATE TABLE events (
	id INTEGER PRIMARY KEY,
	kind TEXT,
	score INTEGER,
	mixed
);
INSERT INTO events (id, kind, score, mixed) VALUES
	(1, 'a', 10, 10),
	(2, 'a', 10, 2.5),
	(3, 'b', 7, '3'),
	(4, 'b', NULL, x'00'),
	(5, NULL, 10, NULL),
	(6, NULL, 7, 7),
	(7, 'a', NULL, 'x'),
	(8, 'b', 7, NULL);
`

func openRef02Fixture(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ref02.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(ref02Schema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	return openStore(t, path)
}

// orderTuple is what the application signs as last_key: every resolved order
// column, not just the primary key.
func orderTuple(row storage.Row, order []storage.OrderSpec) storage.Key {
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

func rowIDs(rows []storage.Row) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.Key[0].Data
	}
	return ids
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestListExplicitOrderNullsLastAndMixedTypes(t *testing.T) {
	st := openRef02Fixture(t)
	nl := func(col string, desc bool) storage.OrderSpec {
		return storage.OrderSpec{Column: col, Desc: desc, NullsLast: true}
	}
	// The tie-break (id) is what resolveOrder appends for a table with a key.
	tests := []struct {
		name  string
		order []storage.OrderSpec
		want  []string
	}{
		{"kind asc, NULLs last", []storage.OrderSpec{nl("kind", false), nl("id", false)}, []string{"1", "2", "7", "3", "4", "8", "5", "6"}},
		{"kind desc, NULLs last", []storage.OrderSpec{nl("kind", true), nl("id", false)}, []string{"3", "4", "8", "1", "2", "7", "5", "6"}},
		{"score asc, NULLs last", []storage.OrderSpec{nl("score", false), nl("id", false)}, []string{"3", "6", "8", "1", "2", "5", "4", "7"}},
		{"score desc, NULLs last", []storage.OrderSpec{nl("score", true), nl("id", false)}, []string{"1", "2", "5", "3", "6", "8", "4", "7"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, _, err := listWith(st, context.Background(), storage.Query{Table: "events", Order: tc.order, Limit: 100})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := rowIDs(rows); !equalIDs(got, tc.want) {
				t.Errorf("order %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}

	// Mixed storage classes in one column: SQLite's native order is
	// NULL < INTEGER/REAL < TEXT < BLOB, reversed by DESC, and NULL stays last.
	// This test documents the observed result; --order never casts.
	mixed := []struct {
		name string
		desc bool
		want []string
	}{
		{"mixed asc", false, []string{"2", "6", "1", "3", "7", "4", "5", "8"}},
		{"mixed desc", true, []string{"4", "7", "3", "1", "6", "2", "5", "8"}},
	}
	for _, tc := range mixed {
		t.Run(tc.name, func(t *testing.T) {
			order := []storage.OrderSpec{nl("mixed", tc.desc), nl("id", false)}
			rows, _, err := listWith(st, context.Background(), storage.Query{Table: "events", Order: order, Limit: 100})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := rowIDs(rows); !equalIDs(got, tc.want) {
				t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestListKeysetAcrossNullsHasNoGapsOrRepeats(t *testing.T) {
	st := openRef02Fixture(t)
	nl := func(col string, desc bool) storage.OrderSpec {
		return storage.OrderSpec{Column: col, Desc: desc, NullsLast: true}
	}
	tests := []struct {
		name  string
		order []storage.OrderSpec
		want  []string
	}{
		{"kind asc", []storage.OrderSpec{nl("kind", false), nl("id", false)}, []string{"1", "2", "7", "3", "4", "8", "5", "6"}},
		{"kind desc", []storage.OrderSpec{nl("kind", true), nl("id", false)}, []string{"3", "4", "8", "1", "2", "7", "5", "6"}},
		{"score desc", []storage.OrderSpec{nl("score", true), nl("id", false)}, []string{"1", "2", "5", "3", "6", "8", "4", "7"}},
		{"kind asc then score desc", []storage.OrderSpec{nl("kind", false), nl("score", true), nl("id", false)}, []string{"1", "2", "7", "3", "8", "4", "5", "6"}},
		{"mixed asc", []storage.OrderSpec{nl("mixed", false), nl("id", false)}, []string{"2", "6", "1", "3", "7", "4", "5", "8"}},
	}
	for _, tc := range tests {
		for _, limit := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s limit %d", tc.name, limit), func(t *testing.T) {
				got := paginateOrder(t, st, tc.order, limit)
				if !equalIDs(got, tc.want) {
					t.Errorf("pages of %s (limit %d) = %v, want %v", tc.name, limit, got, tc.want)
				}
				seen := make(map[string]bool, len(got))
				for _, id := range got {
					if seen[id] {
						t.Errorf("row %s appeared twice in %v", id, got)
					}
					seen[id] = true
				}
			})
		}
	}
}

// paginateOrder mirrors queryPage: LIMIT n+1, truncate to n, and continue from
// every resolved order column of the last emitted row.
func paginateOrder(t *testing.T, st *Store, order []storage.OrderSpec, limit int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	token := ""
	for page := 0; page < 100; page++ {
		q := storage.Query{Table: "events", Order: order, Limit: limit + 1, Cursor: token}
		rows, _, err := listWith(st, ctx, q)
		if err != nil {
			t.Fatalf("List page %d: %v", page+1, err)
		}
		truncated := len(rows) > limit
		if truncated {
			rows = rows[:limit]
		}
		ids = append(ids, rowIDs(rows)...)
		if !truncated {
			return ids
		}
		position, err := cursor.EncodeKey(orderTuple(rows[len(rows)-1], order))
		if err != nil {
			t.Fatalf("EncodeKey page %d: %v", page+1, err)
		}
		token = position
	}
	t.Fatalf("pagination did not finish; ids so far %v", ids)
	return nil
}

// TestListKeysetAfterNullOrderValue is the REF-02 critical case: the cursor's
// last value at the leading column is NULL. Under NULLS LAST the next rows have
// that column NULL too, so equality is IS NULL and the remaining key follows.
func TestListKeysetAfterNullOrderValue(t *testing.T) {
	st := openRef02Fixture(t)
	ctx := context.Background()
	tests := []struct {
		name    string
		order   []storage.OrderSpec
		lastKey storage.Key
		want    []string
	}{
		{
			name:    "asc after a NULL kind",
			order:   []storage.OrderSpec{{Column: "kind", NullsLast: true}, {Column: "id", NullsLast: true}},
			lastKey: storage.Key{{Kind: "null"}, {Kind: "integer", Data: "5"}},
			want:    []string{"6"},
		},
		{
			name:    "desc after a NULL kind",
			order:   []storage.OrderSpec{{Column: "kind", Desc: true, NullsLast: true}, {Column: "id", NullsLast: true}},
			lastKey: storage.Key{{Kind: "null"}, {Kind: "integer", Data: "5"}},
			want:    []string{"6"},
		},
		{
			name:    "asc after a NULL tuple with a non-NULL prefix",
			order:   []storage.OrderSpec{{Column: "kind", NullsLast: true}, {Column: "score", Desc: true, NullsLast: true}, {Column: "id", NullsLast: true}},
			lastKey: storage.Key{{Kind: "text", Data: "a"}, {Kind: "null"}, {Kind: "integer", Data: "7"}},
			want:    []string{"3", "8", "4", "5", "6"},
		},
		{
			name:    "asc after the maximal NULL tuple",
			order:   []storage.OrderSpec{{Column: "kind", NullsLast: true}, {Column: "id", NullsLast: true}},
			lastKey: storage.Key{{Kind: "null"}, {Kind: "integer", Data: "6"}},
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			position, err := cursor.EncodeKey(tc.lastKey)
			if err != nil {
				t.Fatalf("EncodeKey: %v", err)
			}
			rows, _, err := listWith(st, ctx, storage.Query{Table: "events", Order: tc.order, Limit: 10, Cursor: position})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := rowIDs(rows); !equalIDs(got, tc.want) {
				t.Errorf("rows after %+v = %v, want %v", tc.lastKey, got, tc.want)
			}
		})
	}
}
