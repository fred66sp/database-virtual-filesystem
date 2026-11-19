// Package storage defines the storage port: the contracts the CLI uses to read
// and mutate data, independent of any SQLite adapter. The shape follows
// DESIGN §6 verbatim; implementations live in later work packages.
package storage

import "context"

// Value is a typed value. Kind is one of null, integer, real, text or blob and
// Data is its canonical representation (DESIGN §10).
//
// D-WP01: JSON tags are added so the value serializes as the {"kind","data"}
// wrapper DESIGN §10 requires in output and in composite keys; the Go field
// names stay as in DESIGN §6.
type Value struct {
	Kind string `json:"kind"`
	Data string `json:"data"`
}

// Key is a row identity in primary-key order.
type Key []Value

// Field is one column value.
type Field struct {
	Name  string
	Value Value
}

// Row is a record. Fields keeps catalog order because a Go map is not
// deterministic; ETag is the SHA-256 content tag from DESIGN §8.
type Row struct {
	Fields []Field
	Key    Key
	ETag   string
}

// Column is a catalog column. PKOrder is 1-based and 0 for non-primary-key
// columns.
type Column struct {
	Name, DeclaredType  string
	PKOrder             int
	Generated, Nullable bool
}

// Table is a catalog table with its capabilities.
type Table struct {
	Name                          string
	Columns                       []Column
	Readable, Writable, Navigable bool
}

// Filter is one comparison against a column.
type Filter struct {
	Column, Op string
	Value      Value
}

// OrderSpec is one ordering key.
type OrderSpec struct {
	Column string
	Desc   bool
}

// Query describes a bounded read.
type Query struct {
	Table      string
	Filters    []Filter
	Projection []string
	Order      []OrderSpec
	Limit      int
	Cursor     string
}

// Mutation describes a single write.
type Mutation struct {
	Kind, Table, IfMatch string
	Key, Destination     Key
	Fields               map[string]Value
}

// Reader is the read side of the store.
type Reader interface {
	Catalog(ctx context.Context) ([]Table, error)
	Get(ctx context.Context, table string, key Key) (Row, error)
	List(ctx context.Context, q Query) ([]Row, string, error)
}

// Tx is a Reader that can also apply mutations.
type Tx interface {
	Reader
	Apply(ctx context.Context, m Mutation) (Row, error)
}

// Store is a storage backend: reads and writes run inside caller-scoped
// functions so transactions stay in the adapter.
type Store interface {
	Read(ctx context.Context, fn func(Reader) error) error
	Write(ctx context.Context, fn func(Tx) error) error
	Close() error
}
