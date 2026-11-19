package catalog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const fixtureSchema = `
CREATE TABLE authors (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL
);
CREATE TABLE books (
	id INTEGER PRIMARY KEY,
	author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
	title TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	full TEXT GENERATED ALWAYS AS (title || ' ' || slug) STORED,
	UNIQUE (author_id, slug)
);
CREATE INDEX books_title ON books (title);
CREATE TRIGGER books_touch AFTER UPDATE ON books BEGIN SELECT 1; END;
CREATE VIEW books_view AS SELECT id FROM books;
CREATE TABLE nopk (x TEXT);
CREATE TABLE composite (
	a TEXT NOT NULL,
	b TEXT NOT NULL,
	v TEXT,
	PRIMARY KEY (a, b)
) WITHOUT ROWID;
`

func openFixture(t *testing.T) *sql.DB {
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
	return db
}

func loadFixture(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(context.Background(), openFixture(t))
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func table(t *testing.T, c *Catalog, name string) *Table {
	t.Helper()
	for i := range c.Tables {
		if c.Tables[i].Name == name {
			return &c.Tables[i]
		}
	}
	t.Fatalf("table %q missing from catalog %v", name, names(c))
	return nil
}

func names(c *Catalog) []string {
	out := make([]string, len(c.Tables))
	for i := range c.Tables {
		out[i] = c.Tables[i].Name
	}
	return out
}

func TestLoadExposesTablesSortedAndSkipsViews(t *testing.T) {
	c := loadFixture(t)
	got := names(c)
	want := []string{"authors", "books", "composite", "nopk"}
	if len(got) != len(want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tables[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, n := range got {
		if n == "books_view" || n == "sqlite_schema" {
			t.Errorf("table %q must not be exposed", n)
		}
	}
	if c.SchemaVersion <= 0 {
		t.Errorf("SchemaVersion = %d, want positive", c.SchemaVersion)
	}
}

func TestColumnsGeneratedAndPrimaryKey(t *testing.T) {
	books := table(t, loadFixture(t), "books")
	if !books.Navigable || !books.Readable {
		t.Errorf("books capabilities = readable %v navigable %v, want both true", books.Readable, books.Navigable)
	}
	if books.Writable {
		t.Error("books.Writable = true, want false in WP-02")
	}
	pk := books.PrimaryKey()
	if len(pk) != 1 || pk[0].Name != "id" {
		t.Fatalf("primary key = %+v, want [id]", pk)
	}
	if pk[0].Nullable {
		t.Error("INTEGER PRIMARY KEY is a non-null rowid alias but marked nullable")
	}
	for _, col := range books.Columns {
		if col.Name != "full" {
			continue
		}
		if !col.Generated {
			t.Error("generated column full not marked generated")
		}
		if col.DeclaredType != "TEXT" {
			t.Errorf("full declared type = %q, want TEXT", col.DeclaredType)
		}
	}
}

func TestIndexesForeignKeysTriggersAndIncoming(t *testing.T) {
	c := loadFixture(t)
	books := table(t, c, "books")

	var titleIndex, uniqueIndex bool
	for _, idx := range books.Indexes {
		if idx.Name == "books_title" && len(idx.Columns) == 1 && idx.Columns[0] == "title" {
			titleIndex = true
		}
		if idx.Unique && len(idx.Columns) == 1 && idx.Columns[0] == "slug" {
			uniqueIndex = true
		}
	}
	if !titleIndex {
		t.Errorf("indexes = %+v, want books_title(title)", books.Indexes)
	}
	if !uniqueIndex {
		t.Errorf("indexes = %+v, want a unique index on slug", books.Indexes)
	}

	if len(books.ForeignKeys) != 1 {
		t.Fatalf("foreign keys = %+v, want one", books.ForeignKeys)
	}
	fk := books.ForeignKeys[0]
	if fk.Table != "authors" || len(fk.Columns) != 1 || fk.Columns[0] != "author_id" || len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Errorf("foreign key = %+v, want books.author_id -> authors.id", fk)
	}
	if fk.OnDelete != "CASCADE" {
		t.Errorf("on_delete = %q, want CASCADE", fk.OnDelete)
	}

	if len(books.Triggers) != 1 || books.Triggers[0] != "books_touch" {
		t.Errorf("triggers = %v, want [books_touch]", books.Triggers)
	}

	authors := table(t, c, "authors")
	if len(authors.Incoming) != 1 || authors.Incoming[0].Table != "books" {
		t.Errorf("authors incoming = %+v, want the books reference", authors.Incoming)
	}
}

func TestNonNavigableAndCompositeKeys(t *testing.T) {
	c := loadFixture(t)

	nopk := table(t, c, "nopk")
	if nopk.Navigable {
		t.Error("nopk.Navigable = true, want false")
	}
	if len(nopk.Limitations) == 0 {
		t.Error("nopk has no limitation explaining why it is not navigable")
	}

	composite := table(t, c, "composite")
	if !composite.Navigable {
		t.Errorf("composite.Navigable = false, want true (limitations %v)", composite.Limitations)
	}
	pk := composite.PrimaryKey()
	if len(pk) != 2 || pk[0].Name != "a" || pk[1].Name != "b" {
		t.Errorf("composite primary key = %+v, want [a b]", pk)
	}
}

func TestAffinity(t *testing.T) {
	tests := map[string]string{
		"INTEGER":       "INTEGER",
		"BIGINT":        "INTEGER",
		"TEXT":          "TEXT",
		"VARCHAR(20)":   "TEXT",
		"CLOB":          "TEXT",
		"BLOB":          "BLOB",
		"":              "BLOB",
		"REAL":          "REAL",
		"DOUBLE":        "REAL",
		"DECIMAL(10,2)": "NUMERIC",
		"BOOLEAN":       "NUMERIC",
	}
	for declared, want := range tests {
		if got := Affinity(declared); got != want {
			t.Errorf("Affinity(%q) = %q, want %q", declared, got, want)
		}
	}
}
