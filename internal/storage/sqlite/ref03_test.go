package sqlite

import (
	"context"
	"errors"
	"testing"

	"dbvfs/internal/storage"
)

// TestRef03CountMatchesList is the adapter invariant: Count uses the same
// filters and grep predicate as List, so it is always the size of that set.
func TestRef03CountMatchesList(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)

	cases := []storage.Query{
		{Table: "items"},
		{Table: "items", Filters: []storage.Filter{{Column: "label", Op: "is-null"}}},
		{Table: "items", Filters: []storage.Filter{{Column: "big", Op: ">", Value: storage.Value{Kind: "integer", Data: "42"}}}},
		{Table: "items", Search: &storage.Search{Text: "th", Columns: []string{"note", "label"}}},
		{Table: "plain", Search: &storage.Search{Text: "a", Columns: []string{"a"}}},
	}
	for _, q := range cases {
		var n int64
		var listed int
		err := st.Read(context.Background(), func(rd storage.Reader) error {
			var err error
			if n, err = rd.Count(context.Background(), q); err != nil {
				return err
			}
			rows, _, err := rd.List(context.Background(), q)
			listed = len(rows)
			return err
		})
		if err != nil {
			t.Fatalf("count/list %q: %v", q.Table, err)
		}
		if n != int64(listed) {
			t.Errorf("count %q with %+v = %d, list returned %d", q.Table, q.Filters, n, listed)
		}
	}
}

// TestRef03CountIgnoresLimitAndNeedsNoPrimaryKey: a count is the whole filtered
// set and works on a table without identity.
func TestRef03CountIgnoresLimitAndNeedsNoPrimaryKey(t *testing.T) {
	path, _ := openFixture(t)
	st := openStore(t, path)

	tests := []struct {
		q    storage.Query
		want int64
	}{
		{storage.Query{Table: "items", Limit: 1}, 3},
		{storage.Query{Table: "plain"}, 2},
		{storage.Query{Table: "plain", Limit: 1, Cursor: "ignored"}, 2},
	}
	for _, tc := range tests {
		var n int64
		err := st.Read(context.Background(), func(rd storage.Reader) error {
			var err error
			n, err = rd.Count(context.Background(), tc.q)
			return err
		})
		if err != nil {
			t.Fatalf("count %q: %v", tc.q.Table, err)
		}
		if n != tc.want {
			t.Errorf("count %q with limit %d = %d, want %d", tc.q.Table, tc.q.Limit, n, tc.want)
		}
	}

	err := st.Read(context.Background(), func(rd storage.Reader) error {
		_, err := rd.Count(context.Background(), storage.Query{Table: "missing"})
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("count of a missing table = %v, want ErrNotFound", err)
	}
}
