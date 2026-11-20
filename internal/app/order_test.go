package app

import (
	"errors"
	"strings"
	"testing"

	"dbvfs/internal/storage"
)

func orderTable() storage.Table {
	return storage.Table{
		Name:      "events",
		Navigable: true,
		Columns: []storage.Column{
			{Name: "id", DeclaredType: "INTEGER", PKOrder: 1},
			{Name: "seq", DeclaredType: "INTEGER", PKOrder: 2},
			{Name: "kind", DeclaredType: "TEXT"},
			{Name: "score", DeclaredType: "INTEGER"},
		},
	}
}

func TestParseOrderGrammar(t *testing.T) {
	tests := []struct {
		raw  string
		want []storage.OrderSpec
	}{
		{"id", []storage.OrderSpec{{Column: "id", NullsLast: true}}},
		{"kind", []storage.OrderSpec{{Column: "kind", NullsLast: true}}},
		{"kind:asc", []storage.OrderSpec{{Column: "kind", NullsLast: true}}},
		{"kind:desc", []storage.OrderSpec{{Column: "kind", Desc: true, NullsLast: true}}},
		{"kind:desc,score", []storage.OrderSpec{
			{Column: "kind", Desc: true, NullsLast: true},
			{Column: "score", NullsLast: true},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseOrder(tc.raw, orderTable())
			if err != nil {
				t.Fatalf("parseOrder(%q): %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseOrder(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseOrder(%q)[%d] = %+v, want %+v", tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseOrderRejects(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"bogus", `unknown column "bogus"`},
		{"1", `unknown column "1"`},
		{"", `unknown column ""`},
		{"kind:sideways", `unknown order direction "sideways"`},
		{"kind:ASC", `unknown order direction "ASC"`},
		{"kind:desc:extra", `unknown order direction "desc:extra"`},
		{"kind:asc,kind:desc", `column "kind" is ordered more than once`},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := parseOrder(tc.raw, orderTable())
			if !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("parseOrder(%q) error = %v, want storage.ErrInvalid", tc.raw, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseOrder(%q) message = %q, want it to contain %q", tc.raw, err, tc.want)
			}
		})
	}
}

func TestResolveOrderAppendsTheRestOfTheKey(t *testing.T) {
	table := orderTable()
	tests := []struct {
		name string
		user []storage.OrderSpec
		want []storage.OrderSpec
	}{
		{
			name: "single non-key column",
			user: []storage.OrderSpec{{Column: "score", Desc: true, NullsLast: true}},
			want: []storage.OrderSpec{
				{Column: "score", Desc: true, NullsLast: true},
				{Column: "id", NullsLast: true},
				{Column: "seq", NullsLast: true},
			},
		},
		{
			name: "part of the key in a different order",
			user: []storage.OrderSpec{{Column: "seq", Desc: true, NullsLast: true}},
			want: []storage.OrderSpec{
				{Column: "seq", Desc: true, NullsLast: true},
				{Column: "id", NullsLast: true},
			},
		},
		{
			name: "the whole key needs no tie-break",
			user: []storage.OrderSpec{{Column: "id", Desc: true, NullsLast: true}, {Column: "seq", NullsLast: true}},
			want: []storage.OrderSpec{{Column: "id", Desc: true, NullsLast: true}, {Column: "seq", NullsLast: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOrder(tc.user, table)
			if len(got) != len(tc.want) {
				t.Fatalf("resolveOrder = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("resolveOrder[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestResolveOrderWithoutPrimaryKeyUsesDefaultOrder(t *testing.T) {
	table := storage.Table{
		Name:    "plain",
		Columns: []storage.Column{{Name: "x", DeclaredType: "TEXT"}, {Name: "y", DeclaredType: "INTEGER"}},
	}
	got := resolveOrder([]storage.OrderSpec{{Column: "y", Desc: true, NullsLast: true}}, table)
	want := []storage.OrderSpec{{Column: "y", Desc: true, NullsLast: true}, {Column: "x", NullsLast: true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("resolveOrder = %+v, want %+v", got, want)
	}
}
