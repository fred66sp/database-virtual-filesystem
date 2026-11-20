package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"dbvfs/internal/state"
)

const ref03Schema = `
CREATE TABLE items (
	id INTEGER PRIMARY KEY,
	kind TEXT,
	body TEXT
);
INSERT INTO items VALUES
	(1, 'a', 'alpha one'),
	(2, 'a', 'alpha two'),
	(3, 'b', 'beta'),
	(4, 'b', NULL),
	(5, NULL, 'alpha three');
CREATE TABLE nopk (x TEXT);
INSERT INTO nopk VALUES ('a'), ('b'), ('c'), (NULL);
`

func setupRef03(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	fixture := newFixture(t, ref03Schema)
	if _, code := runCLI(t, "mount", fixture, "/r3"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
}

type countJSON struct {
	Count int64 `json:"count"`
}

// ref03Count runs a count and checks the compact payload has no row array.
func ref03Count(t *testing.T, args ...string) (int64, envelopeJSON) {
	t.Helper()
	env, code := runCLI(t, args...)
	data := mustOK(t, env, code, args...)
	if bytes.Contains(data, []byte(`"rows"`)) || bytes.Contains(data, []byte(`"entries"`)) {
		t.Errorf("%q payload = %s; a count carries no rows", args, data)
	}
	return decode[countJSON](t, data).Count, env
}

// TestRef03CountMatchesFind ties the count to the equivalent read, with and
// without a --where filter.
func TestRef03CountMatchesFind(t *testing.T) {
	setupRef03(t)

	queries := [][]string{
		{"find", "/r3/items"},
		{"find", "/r3/items", "--where", "kind=a"},
		{"find", "/r3/items", "--where", "id>=4"},
		{"find", "/r3/items", "--where", "body is-null"},
	}
	for _, base := range queries {
		env, code := runCLI(t, base...)
		page := decode[findJSON](t, mustOK(t, env, code, base...))
		args := append(append([]string{}, base...), "--count")
		n, _ := ref03Count(t, args...)
		if n != int64(len(page.Rows)) {
			t.Errorf("count of %q = %d, find returned %d rows", base, n, len(page.Rows))
		}
	}
}

// TestRef03GrepCountCountsRowsNotEntries: grep --count uses grep's predicate and
// counts matching rows, even when one row matches in more than one column.
func TestRef03GrepCountCountsRowsNotEntries(t *testing.T) {
	setupRef03(t)

	env, code := runCLI(t, "grep", "/r3/items", "a")
	found := decode[grepJSON](t, mustOK(t, env, code, "grep /r3/items a"))
	rows := make(map[string]bool, len(found.Entries))
	for _, entry := range found.Entries {
		rows[entry.Row] = true
	}
	if len(found.Entries) <= len(rows) {
		t.Fatalf("fixture does not exercise multi-column matches: %d entries, %d rows", len(found.Entries), len(rows))
	}

	n, _ := ref03Count(t, "grep", "/r3/items", "a", "--count")
	if n != int64(len(rows)) {
		t.Errorf("grep --count = %d, want %d matching rows (%d entries)", n, len(rows), len(found.Entries))
	}
}

// TestRef03CountIgnoresLimitAndIsCompact fixes the shape and the page-free
// semantics: --limit never bounds a count and nothing pagination-related leaks.
func TestRef03CountIgnoresLimit(t *testing.T) {
	setupRef03(t)

	n, env := ref03Count(t, "find", "/r3/items", "--count", "--limit", "1")
	if n != 5 {
		t.Errorf("count with --limit 1 = %d, want the whole set 5", n)
	}
	if want := `{"count":5}`; string(env.Data) != want {
		t.Errorf("payload = %s, want compact %s", env.Data, want)
	}
	if env.Meta.Truncated || env.Meta.NextCursor != "" {
		t.Errorf("meta = %+v, want a non-truncated count with no cursor", env.Meta)
	}

	if n, _ = ref03Count(t, "grep", "/r3/items", "a", "--count", "--limit", "1"); n != 4 {
		t.Errorf("grep count with --limit 1 = %d, want 4", n)
	}
}

// TestRef03CountRejectsCursor: counting one page is not a defined operation.
func TestRef03CountRejectsCursor(t *testing.T) {
	setupRef03(t)

	for _, args := range [][]string{
		{"find", "/r3/items", "--count", "--cursor", "abc"},
		{"grep", "/r3/items", "a", "--count", "--cursor", "abc"},
	} {
		env, code := runCLI(t, args...)
		mustCode(t, env, code, CodeUsage, args...)
		if !strings.Contains(env.Error.Message, "--count and --cursor are mutually exclusive") {
			t.Errorf("message = %q, want the incompatibility", env.Error.Message)
		}
	}
}

// TestRef03CountIgnoresFields: a count has no row shape, so a valid --fields
// list is ignored; a bogus one is still rejected so that a typo cannot hide
// behind an ignored flag (same anti-typo rule as --order, REF-03).
func TestRef03CountIgnoresFields(t *testing.T) {
	setupRef03(t)

	n, _ := ref03Count(t, "find", "/r3/items", "--count", "--fields", "kind")
	if n != 5 {
		t.Errorf("count with an ignored valid --fields = %d, want 5", n)
	}
	env, code := runCLI(t, "find", "/r3/items", "--count", "--fields", "bogus")
	mustCode(t, env, code, CodeUsage, "count with a bogus --fields is rejected, not ignored")
}

// TestRef03CountWithoutPrimaryKey: no identity is needed; there is no page to
// continue and therefore nothing to complicate.
func TestRef03CountWithoutPrimaryKey(t *testing.T) {
	setupRef03(t)

	tests := []struct {
		args []string
		want int64
	}{
		{[]string{"find", "/r3/nopk", "--count"}, 4},
		{[]string{"find", "/r3/nopk", "--count", "--limit", "1"}, 4},
		{[]string{"find", "/r3/nopk", "--count", "--where", "x is-null"}, 1},
	}
	for _, tc := range tests {
		n, _ := ref03Count(t, tc.args...)
		if n != tc.want {
			t.Errorf("%q = %d, want %d", tc.args, n, tc.want)
		}
	}
}

// TestRef03CountKeepsPolicy: counting is reading existence, so it answers with
// exactly the error and the set the equivalent read would.
func TestRef03CountKeepsPolicy(t *testing.T) {
	setupRef03(t)

	setRef03Policy(t, []state.Policy{{Ops: []string{"cat"}, Prefix: "/r3/items/1"}})
	plain, plainCode := runCLI(t, "find", "/r3/items")
	count, countCode := runCLI(t, "find", "/r3/items", "--count")
	mustCode(t, plain, plainCode, CodeDenied, "find on a denied table")
	mustCode(t, count, countCode, CodeDenied, "find --count on a denied table")
	if plain.Error.Message != count.Error.Message {
		t.Errorf("denial with --count = %q, without = %q; want the same error",
			count.Error.Message, plain.Error.Message)
	}

	// A grant for find also authorizes counting; a filter still trims the set.
	setRef03Policy(t, []state.Policy{{Ops: []string{"find"}, Prefix: "/r3/items"}})
	n, _ := ref03Count(t, "find", "/r3/items", "--count", "--where", "kind=b")
	if n != 2 {
		t.Errorf("count under a restrictive filter = %d, want 2", n)
	}

	// grep is a different op: its grant does not exist here, count included.
	for _, args := range [][]string{
		{"grep", "/r3/items", "a"},
		{"grep", "/r3/items", "a", "--count"},
	} {
		env, code := runCLI(t, args...)
		mustCode(t, env, code, CodeDenied, args...)
	}
}

// TestRef03GrepCountNeedsLiteral keeps grep's validation on the count path.
func TestRef03GrepCountNeedsLiteral(t *testing.T) {
	setupRef03(t)

	env, code := runCLI(t, "grep", "/r3/items", "", "--count")
	mustCode(t, env, code, CodeUsage, "grep --count with an empty literal")
}

func setRef03Policy(t *testing.T, allow []state.Policy) {
	t.Helper()
	dir, err := state.Dir()
	if err != nil {
		t.Fatalf("state.Dir: %v", err)
	}
	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["r3"]
		m.Allow = allow
		cfg.Mounts["r3"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
}

// TestRef03NoCountShapeUnchanged pins that without --count find still emits the
// historical {path, rows} object; the byte-level guarantee is the REF-01
// golden (find_default.golden), which this change leaves untouched.
func TestRef03NoCountShapeUnchanged(t *testing.T) {
	setupRef03(t)

	env, code := runCLI(t, "find", "/r3/items")
	data := mustOK(t, env, code, "find without --count")
	if !bytes.Contains(data, []byte(`"rows"`)) || !bytes.Contains(data, []byte(`"etag"`)) {
		t.Errorf("find without --count = %s, want the historical rows object", data)
	}
	if bytes.Contains(data, []byte(`"count":`)) {
		t.Errorf("find without --count = %s, want no count field", data)
	}
}
