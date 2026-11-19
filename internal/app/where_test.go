package app

import (
	"errors"
	"testing"

	"dbvfs/internal/storage"
)

func whereTable() storage.Table {
	return storage.Table{
		Name: "tasks",
		Columns: []storage.Column{
			{Name: "id", DeclaredType: "INTEGER", PKOrder: 1},
			{Name: "title", DeclaredType: "TEXT"},
			{Name: "note", DeclaredType: "TEXT"},
			{Name: "ratio", DeclaredType: "REAL"},
		},
	}
}

func TestParseWhereValues(t *testing.T) {
	tests := []struct {
		expr string
		want storage.Filter
	}{
		{"title=pending", storage.Filter{Column: "title", Op: "=", Value: storage.Value{Kind: "text", Data: "pending"}}},
		{"id=1", storage.Filter{Column: "id", Op: "=", Value: storage.Value{Kind: "integer", Data: "1"}}},
		{`id="1"`, storage.Filter{Column: "id", Op: "=", Value: storage.Value{Kind: "text", Data: "1"}}},
		{"ratio=1.5", storage.Filter{Column: "ratio", Op: "=", Value: storage.Value{Kind: "real", Data: "1.5"}}},
		{"id=true", storage.Filter{Column: "id", Op: "=", Value: storage.Value{Kind: "integer", Data: "1"}}},
		{"id=false", storage.Filter{Column: "id", Op: "=", Value: storage.Value{Kind: "integer", Data: "0"}}},
		{"title=a=b", storage.Filter{Column: "title", Op: "=", Value: storage.Value{Kind: "text", Data: "a=b"}}},
		{"title=", storage.Filter{Column: "title", Op: "=", Value: storage.Value{Kind: "text"}}},
		{"id!=2", storage.Filter{Column: "id", Op: "!=", Value: storage.Value{Kind: "integer", Data: "2"}}},
		{"id<=2", storage.Filter{Column: "id", Op: "<=", Value: storage.Value{Kind: "integer", Data: "2"}}},
		{"id>=2", storage.Filter{Column: "id", Op: ">=", Value: storage.Value{Kind: "integer", Data: "2"}}},
		{"id<2", storage.Filter{Column: "id", Op: "<", Value: storage.Value{Kind: "integer", Data: "2"}}},
		{"id>2", storage.Filter{Column: "id", Op: ">", Value: storage.Value{Kind: "integer", Data: "2"}}},
		{"note is-null", storage.Filter{Column: "note", Op: "is-null"}},
		{"note=is-null", storage.Filter{Column: "note", Op: "is-null"}},
		{"note not-null", storage.Filter{Column: "note", Op: "not-null"}},
		{"note=null", storage.Filter{Column: "note", Op: "is-null"}},
		{"note!=null", storage.Filter{Column: "note", Op: "not-null"}},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := parseWhere([]string{tc.expr}, whereTable())
			if err != nil {
				t.Fatalf("parseWhere(%q): %v", tc.expr, err)
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("parseWhere(%q) = %+v, want %+v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestParseWhereMultipleAreAND(t *testing.T) {
	got, err := parseWhere([]string{"id>=2", "note is-null"}, whereTable())
	if err != nil {
		t.Fatalf("parseWhere: %v", err)
	}
	if len(got) != 2 || got[0].Op != ">=" || got[1].Op != "is-null" {
		t.Errorf("filters = %+v, want both in order", got)
	}
}

func TestParseWhereRejects(t *testing.T) {
	tests := []string{
		"no-operator",
		"bogus=1",
		"=1",
		"id=[1]",
		"id={\"a\":1}",
		"note<null",
	}
	for _, expr := range tests {
		t.Run(expr, func(t *testing.T) {
			_, err := parseWhere([]string{expr}, whereTable())
			if !errors.Is(err, storage.ErrInvalid) {
				t.Errorf("parseWhere(%q) error = %v, want storage.ErrInvalid", expr, err)
			}
		})
	}
}
