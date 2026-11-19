package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"dbvfs/internal/storage"
)

// wp05Schema is the adversarial fixture for cp, mv and rm: a generated column,
// a UNIQUE constraint, a composite key, a trigger, incoming references with and
// without cascading actions and an outgoing cascading foreign key.
const wp05Schema = `
CREATE TABLE notes (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	body TEXT,
	tag TEXT UNIQUE,
	twice INTEGER GENERATED ALWAYS AS (id * 2) VIRTUAL
);
INSERT INTO notes (id, title, body, tag) VALUES (1, 'one', 'uno', 't1');
INSERT INTO notes (id, title, body, tag) VALUES (2, 'two', 'dos', 't2');
CREATE TABLE docs (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	body TEXT,
	twice INTEGER GENERATED ALWAYS AS (id * 2) VIRTUAL
);
INSERT INTO docs (id, title, body) VALUES (1, 'one', 'uno');
CREATE TABLE pairs (a TEXT NOT NULL, b INTEGER NOT NULL, v TEXT, PRIMARY KEY (a, b));
INSERT INTO pairs VALUES ('a', 1, 'x');
CREATE TABLE plain (x TEXT);
INSERT INTO plain VALUES ('a');
CREATE TABLE guarded (id INTEGER PRIMARY KEY, v TEXT);
INSERT INTO guarded VALUES (1, 'x');
CREATE TRIGGER guarded_ins AFTER INSERT ON guarded BEGIN SELECT 1; END;
CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO parents VALUES (1, 'one');
CREATE TABLE cascading_children (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO cascading_children VALUES (1, 1);
CREATE TABLE restrict_children (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE RESTRICT ON UPDATE RESTRICT
);
INSERT INTO restrict_children VALUES (1, 1);
`

func openMutationFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(wp05Schema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	st, err := Open(Options{Path: path, Mode: "rw", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func integerKey(n int64) storage.Key {
	return storage.Key{{Kind: "integer", Data: strconv.FormatInt(n, 10)}}
}

func applyOne(t *testing.T, st *Store, m storage.Mutation) (storage.Row, error) {
	t.Helper()
	ctx := context.Background()
	var row storage.Row
	err := st.Write(ctx, func(tx storage.Tx) error {
		var err error
		row, err = tx.Apply(ctx, m)
		return err
	})
	return row, err
}

func rowExists(t *testing.T, st *Store, table string, key storage.Key) bool {
	t.Helper()
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		_, err := rd.Get(context.Background(), table, key)
		return err
	})
	return err == nil
}

func readKey(t *testing.T, st *Store, table string, key storage.Key) storage.Row {
	t.Helper()
	var row storage.Row
	err := st.Read(context.Background(), func(rd storage.Reader) error {
		var err error
		row, err = rd.Get(context.Background(), table, key)
		return err
	})
	if err != nil {
		t.Fatalf("Get %s %+v: %v", table, key, err)
	}
	return row
}

func TestApplyCopy(t *testing.T) {
	st, _ := openMutationFixture(t)
	src := readRow(t, st, "docs", 1)

	copied, err := applyOne(t, st, storage.Mutation{
		Kind: "cp", Table: "docs", Key: src.Key, Destination: integerKey(3), IfMatch: src.ETag,
	})
	if err != nil {
		t.Fatalf("cp: %v", err)
	}
	if len(copied.Key) != 1 || copied.Key[0].Data != "3" {
		t.Errorf("copied key = %+v, want integer 3", copied.Key)
	}
	for _, name := range []string{"title", "body"} {
		if got, want := valueOf(copied, name), valueOf(src, name); got != want {
			t.Errorf("copied %s = %+v, want %+v", name, got, want)
		}
	}
	// Generated columns are recomputed by the engine, never copied.
	if got := valueOf(copied, "twice"); got != (storage.Value{Kind: "integer", Data: "6"}) {
		t.Errorf("copied generated column = %+v, want 6", got)
	}

	// A stale ETag conflicts and creates nothing.
	if _, err := applyOne(t, st, storage.Mutation{
		Kind: "cp", Table: "docs", Key: src.Key, Destination: integerKey(4), IfMatch: "sha256:stale",
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cp with a stale ETag = %v, want storage.ErrConflict", err)
	}
	if rowExists(t, st, "docs", integerKey(4)) {
		t.Error("a conflicting cp created the destination row")
	}

	// Never overwrite an existing destination.
	if _, err := applyOne(t, st, storage.Mutation{
		Kind: "cp", Table: "docs", Key: src.Key, Destination: integerKey(3), IfMatch: src.ETag,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cp onto an existing row = %v, want storage.ErrConflict", err)
	}

	// A UNIQUE column still speaks: tag 't1' is already taken by row 1.
	unique := readRow(t, st, "notes", 1)
	if _, err := applyOne(t, st, storage.Mutation{
		Kind: "cp", Table: "notes", Key: unique.Key, Destination: integerKey(9), IfMatch: unique.ETag,
	}); !errors.Is(err, storage.ErrConstraint) {
		t.Fatalf("cp violating UNIQUE = %v, want storage.ErrConstraint", err)
	}
	if rowExists(t, st, "notes", integerKey(9)) {
		t.Error("a cp that violated UNIQUE left a row behind")
	}

	// A composite destination key is explicit and allowed.
	pair := readKey(t, st, "pairs", storage.Key{{Kind: "text", Data: "a"}, {Kind: "integer", Data: "1"}})
	compositeDest := storage.Key{{Kind: "text", Data: "b"}, {Kind: "integer", Data: "1"}}
	pairCopy, err := applyOne(t, st, storage.Mutation{
		Kind: "cp", Table: "pairs", Key: pair.Key, Destination: compositeDest, IfMatch: pair.ETag,
	})
	if err != nil {
		t.Fatalf("cp with a composite key: %v", err)
	}
	if got := valueOf(pairCopy, "v"); got != (storage.Value{Kind: "text", Data: "x"}) {
		t.Errorf("composite copy v = %+v, want x", got)
	}

	tests := []struct {
		name string
		m    storage.Mutation
		want error
	}{
		{
			name: "non-navigable source",
			m:    storage.Mutation{Kind: "cp", Table: "plain", Key: integerKey(1), Destination: integerKey(2), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "triggered table",
			m:    storage.Mutation{Kind: "cp", Table: "guarded", Key: integerKey(1), Destination: integerKey(2), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "outgoing cascade",
			m:    storage.Mutation{Kind: "cp", Table: "cascading_children", Key: integerKey(1), Destination: integerKey(2), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "missing ETag",
			m:    storage.Mutation{Kind: "cp", Table: "notes", Key: src.Key, Destination: integerKey(5)},
			want: storage.ErrInvalid,
		},
		{
			name: "missing destination",
			m:    storage.Mutation{Kind: "cp", Table: "notes", Key: src.Key, IfMatch: src.ETag},
			want: storage.ErrNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyOne(t, st, tc.m); !errors.Is(err, tc.want) {
				t.Fatalf("cp error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestApplyRename(t *testing.T) {
	st, _ := openMutationFixture(t)
	row := readRow(t, st, "notes", 1)

	moved, err := applyOne(t, st, storage.Mutation{
		Kind: "mv", Table: "notes", Key: row.Key, Destination: integerKey(10), IfMatch: row.ETag,
	})
	if err != nil {
		t.Fatalf("mv: %v", err)
	}
	if moved.Key[0].Data != "10" || !rowExists(t, st, "notes", integerKey(10)) {
		t.Errorf("mv key = %+v, want the row at key 10", moved.Key)
	}
	if rowExists(t, st, "notes", integerKey(1)) {
		t.Error("mv left the source key behind")
	}
	if got := valueOf(moved, "tag"); got != (storage.Value{Kind: "text", Data: "t1"}) {
		t.Errorf("mv changed an ordinary column: tag = %+v", got)
	}

	// A stale ETag conflicts and leaves the row where it was.
	if _, err := applyOne(t, st, storage.Mutation{
		Kind: "mv", Table: "notes", Key: integerKey(10), Destination: integerKey(11), IfMatch: row.ETag,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("mv with a stale ETag = %v, want storage.ErrConflict", err)
	}
	if !rowExists(t, st, "notes", integerKey(10)) || rowExists(t, st, "notes", integerKey(11)) {
		t.Error("a conflicting mv changed the row identity")
	}

	// The destination must not exist.
	current := readRow(t, st, "notes", 10)
	if _, err := applyOne(t, st, storage.Mutation{
		Kind: "mv", Table: "notes", Key: current.Key, Destination: integerKey(2), IfMatch: current.ETag,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("mv onto an existing row = %v, want storage.ErrConflict", err)
	}

	pair := readKey(t, st, "pairs", storage.Key{{Kind: "text", Data: "a"}, {Kind: "integer", Data: "1"}})
	parent := readRow(t, st, "parents", 1)
	tests := []struct {
		name string
		m    storage.Mutation
		want error
	}{
		{
			name: "composite primary key",
			m:    storage.Mutation{Kind: "mv", Table: "pairs", Key: pair.Key, Destination: storage.Key{{Kind: "text", Data: "b"}, {Kind: "integer", Data: "1"}}, IfMatch: pair.ETag},
			want: storage.ErrUnsupported,
		},
		{
			name: "incoming references",
			m:    storage.Mutation{Kind: "mv", Table: "parents", Key: parent.Key, Destination: integerKey(2), IfMatch: parent.ETag},
			want: storage.ErrUnsupported,
		},
		{
			name: "triggered table",
			m:    storage.Mutation{Kind: "mv", Table: "guarded", Key: integerKey(1), Destination: integerKey(2), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "missing ETag",
			m:    storage.Mutation{Kind: "mv", Table: "notes", Key: current.Key, Destination: integerKey(12)},
			want: storage.ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyOne(t, st, tc.m); !errors.Is(err, tc.want) {
				t.Fatalf("mv error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestApplyRemove(t *testing.T) {
	st, _ := openMutationFixture(t)
	row := readRow(t, st, "notes", 2)

	removed, err := applyOne(t, st, storage.Mutation{
		Kind: "rm", Table: "notes", Key: row.Key, IfMatch: row.ETag,
	})
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if got := valueOf(removed, "title"); got != (storage.Value{Kind: "text", Data: "two"}) {
		t.Errorf("rm returned %+v, want the deleted row", got)
	}
	if rowExists(t, st, "notes", integerKey(2)) {
		t.Error("rm left the row behind")
	}

	parent := readRow(t, st, "parents", 1)
	tests := []struct {
		name string
		m    storage.Mutation
		want error
	}{
		{
			name: "stale ETag",
			m:    storage.Mutation{Kind: "rm", Table: "notes", Key: integerKey(1), IfMatch: "sha256:stale"},
			want: storage.ErrConflict,
		},
		{
			name: "incoming references",
			m:    storage.Mutation{Kind: "rm", Table: "parents", Key: parent.Key, IfMatch: parent.ETag},
			want: storage.ErrUnsupported,
		},
		{
			name: "outgoing cascade",
			m:    storage.Mutation{Kind: "rm", Table: "cascading_children", Key: integerKey(1), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "triggered table",
			m:    storage.Mutation{Kind: "rm", Table: "guarded", Key: integerKey(1), IfMatch: "sha256:x"},
			want: storage.ErrUnsupported,
		},
		{
			name: "missing ETag",
			m:    storage.Mutation{Kind: "rm", Table: "notes", Key: integerKey(1)},
			want: storage.ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyOne(t, st, tc.m); !errors.Is(err, tc.want) {
				t.Fatalf("rm error = %v, want %v", err, tc.want)
			}
		})
	}
	if !rowExists(t, st, "notes", integerKey(1)) {
		t.Error("a failed rm deleted the row")
	}
}

// TestWriteReportsUnknownCommit forces COMMIT to fail by holding a read
// transaction open on a second connection: the write transaction starts, the
// mutation applies, and only the commit is blocked. The outcome is unknown by
// construction and the message must ask the caller to reconcile by reading.
func TestWriteReportsUnknownCommit(t *testing.T) {
	st, path := openMutationFixture(t)

	blocker, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	t.Cleanup(func() { blocker.Close() })
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count); err != nil {
		t.Fatalf("hold a shared lock: %v", err)
	}

	_, err = applyOne(t, st, storage.Mutation{
		Kind: "touch", Table: "notes",
		Fields: map[string]storage.Value{"title": {Kind: "text", Data: "blocked"}},
	})
	if !errors.Is(err, storage.ErrCommitUnknown) {
		t.Fatalf("commit under a competing reader = %v, want storage.ErrCommitUnknown", err)
	}
	if !strings.Contains(err.Error(), "reconcile") {
		t.Errorf("unknown commit message = %q, want a reconciliation signal", err)
	}
}
