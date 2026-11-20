package cli

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ref02Schema mirrors the storage fixture: duplicate values, NULLs and mixed
// storage classes, plus a table without a stable identity.
const ref02CLISchema = `
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
CREATE TABLE nopk (x TEXT);
INSERT INTO nopk VALUES ('b'), ('a'), (NULL), ('c');
`

func setupRef02(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	fixture := newFixture(t, ref02CLISchema)
	if _, code := runCLI(t, "mount", fixture, "/ref"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
}

// ref02IDs reads the row identifiers from /ref/events paths.
func ref02IDs(page findJSON) []string {
	ids := make([]string, len(page.Rows))
	for i, row := range page.Rows {
		ids[i] = strings.TrimPrefix(row.Path, "/ref/events/")
	}
	return ids
}

func equalStrings(got, want []string) bool {
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

// TestRef02FindWithoutOrderIsByteIdentical pins the no-flag output: an explicit
// order equal to the default produces the same bytes. The no-flag golden in the
// REF-01 test is the stronger, historical check.
func TestRef02FindWithoutOrderIsByteIdentical(t *testing.T) {
	setupRef02(t)

	var plain, explicit, stderr bytes.Buffer
	code := Run([]string{"find", "/ref/events", "--request-id", "req-ref02"}, &plain, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("find without --order = %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	code = Run([]string{"find", "/ref/events", "--order", "id", "--request-id", "req-ref02"}, &explicit, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("find --order id = %d, stderr %q", code, stderr.String())
	}
	if !bytes.Equal(plain.Bytes(), explicit.Bytes()) {
		t.Errorf("output with no --order and with --order id differs\n got: %q\nwant: %q", explicit.Bytes(), plain.Bytes())
	}
}

func TestRef02OrderValidation(t *testing.T) {
	setupRef02(t)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown column", []string{"find", "/ref/events", "--order", "bogus"}, `unknown column "bogus"`},
		{"positional index", []string{"find", "/ref/events", "--order", "1"}, `unknown column "1"`},
		{"bad direction", []string{"find", "/ref/events", "--order", "id:sideways"}, `unknown order direction "sideways"`},
		{"uppercase direction", []string{"find", "/ref/events", "--order", "id:ASC"}, `unknown order direction "ASC"`},
		{"duplicate column", []string{"find", "/ref/events", "--order", "id:asc,id:desc"}, `column "id" is ordered more than once`},
		{"empty list", []string{"find", "/ref/events", "--order", ""}, "--order requires a non-empty list"},
		{"flag repeated", []string{"find", "/ref/events", "--order", "id", "--order", "score"}, `flag "--order" given more than once`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, code := runCLI(t, tc.args...)
			mustCode(t, env, code, CodeUsage, tc.args...)
			if !strings.Contains(env.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", env.Error.Message, tc.want)
			}
		})
	}
}

func TestRef02DirectionsAndNullsLast(t *testing.T) {
	setupRef02(t)

	tests := []struct {
		order string
		want  []string
	}{
		{"kind:asc", []string{"1", "2", "7", "3", "4", "8", "5", "6"}},
		{"kind:desc", []string{"3", "4", "8", "1", "2", "7", "5", "6"}},
		{"score:desc", []string{"1", "2", "5", "3", "6", "8", "4", "7"}},
		{"kind:asc,score:desc", []string{"1", "2", "7", "3", "8", "4", "5", "6"}},
		{"mixed:asc", []string{"2", "6", "1", "3", "7", "4", "5", "8"}},
		{"mixed:desc", []string{"4", "7", "3", "1", "6", "2", "5", "8"}},
	}
	for _, tc := range tests {
		t.Run(tc.order, func(t *testing.T) {
			env, code := runCLI(t, "find", "/ref/events", "--order", tc.order)
			page := decode[findJSON](t, mustOK(t, env, code, "find", "/ref/events", "--order", tc.order))
			if got := ref02IDs(page); !equalStrings(got, tc.want) {
				t.Errorf("%s = %v, want %v", tc.order, got, tc.want)
			}
		})
	}
}

func TestRef02OrderWithoutPrimaryKeyStaysOnFirstPage(t *testing.T) {
	setupRef02(t)

	tests := []struct {
		order string
		want  []string
	}{
		{"x:asc", []string{"a", "b", "c", ""}},
		{"x:desc", []string{"c", "b", "a", ""}},
	}
	for _, tc := range tests {
		t.Run(tc.order, func(t *testing.T) {
			env, code := runCLI(t, "find", "/ref/nopk", "--order", tc.order, "--limit", "2")
			page := decode[findJSON](t, mustOK(t, env, code, "find", "/ref/nopk", "--order", tc.order, "--limit", "2"))
			got := make([]string, len(page.Rows))
			for i, row := range page.Rows {
				got[i] = row.Fields["x"].Data
			}
			if !equalStrings(got, tc.want[:2]) {
				t.Errorf("%s first page = %v, want %v", tc.order, got, tc.want[:2])
			}
			if !env.Meta.Truncated {
				t.Errorf("%s --limit 2 on 4 rows is not truncated", tc.order)
			}
			if env.Meta.NextCursor != "" {
				t.Errorf("%s promised a cursor on a table without a key: %q", tc.order, env.Meta.NextCursor)
			}

			env, code = runCLI(t, "find", "/ref/nopk", "--order", tc.order)
			full := decode[findJSON](t, mustOK(t, env, code, "find", "/ref/nopk", "--order", tc.order))
			all := make([]string, len(full.Rows))
			for i, row := range full.Rows {
				all[i] = row.Fields["x"].Data
			}
			if !equalStrings(all, tc.want) {
				t.Errorf("%s = %v, want %v with NULL last", tc.order, all, tc.want)
			}
			if kind := full.Rows[len(full.Rows)-1].Fields["x"].Kind; kind != "null" {
				t.Errorf("%s last value kind = %q, want null", tc.order, kind)
			}
		})
	}
}

func TestRef02CursorBindsOrder(t *testing.T) {
	setupRef02(t)

	env, code := runCLI(t, "find", "/ref/events", "--order", "score:desc", "--limit", "2")
	first := decode[findJSON](t, mustOK(t, env, code, "find --order score:desc"))
	if !env.Meta.Truncated || env.Meta.NextCursor == "" {
		t.Fatalf("first page = %+v, meta %+v; want a cursor", first, env.Meta)
	}
	token := env.Meta.NextCursor
	if got := ref02IDs(first); !equalStrings(got, []string{"1", "2"}) {
		t.Fatalf("first page = %v, want rows 1 and 2", got)
	}

	env, code = runCLI(t, "find", "/ref/events", "--order", "score:desc", "--limit", "2", "--cursor", token)
	second := decode[findJSON](t, mustOK(t, env, code, "same order and cursor"))
	if got := ref02IDs(second); !equalStrings(got, []string{"5", "3"}) {
		t.Errorf("second page = %v, want rows 5 and 3", got)
	}

	for _, args := range [][]string{
		{"find", "/ref/events", "--limit", "2", "--cursor", token},
		{"find", "/ref/events", "--order", "kind:asc", "--limit", "2", "--cursor", token},
		{"find", "/ref/events", "--order", "score:asc", "--limit", "2", "--cursor", token},
	} {
		env, code = runCLI(t, args...)
		mustCode(t, env, code, CodeUsage, args...)
	}

	// --order and --fields are independent and both bound to the cursor.
	env, code = runCLI(t, "find", "/ref/events", "--order", "score:desc", "--fields", "kind", "--limit", "2")
	projected := decode[findJSON](t, mustOK(t, env, code, "find --order with --fields"))
	if projected.Rows[0].Path != "/ref/events/1" || env.Meta.NextCursor == "" {
		t.Fatalf("projected first page = %+v, meta %+v", projected, env.Meta)
	}
	env, code = runCLI(t, "find", "/ref/events", "--order", "score:desc", "--limit", "2", "--cursor", env.Meta.NextCursor)
	mustCode(t, env, code, CodeUsage, "cursor reused without the projection that minted it")
}

func TestRef02FullPaginationHasNoGapsOrRepeats(t *testing.T) {
	setupRef02(t)

	tests := []struct {
		order string
		want  []string
	}{
		{"kind:asc", []string{"1", "2", "7", "3", "4", "8", "5", "6"}},
		{"kind:desc", []string{"3", "4", "8", "1", "2", "7", "5", "6"}},
		{"score:desc", []string{"1", "2", "5", "3", "6", "8", "4", "7"}},
		{"kind:asc,score:desc", []string{"1", "2", "7", "3", "8", "4", "5", "6"}},
		{"mixed:asc", []string{"2", "6", "1", "3", "7", "4", "5", "8"}},
	}
	for _, tc := range tests {
		for _, limit := range []int{1, 2, 3} {
			t.Run(tc.order+" limit "+strconv.Itoa(limit), func(t *testing.T) {
				got := ref02Paginate(t, tc.order, limit)
				if !equalStrings(got, tc.want) {
					t.Errorf("pages of %s (limit %d) = %v, want %v", tc.order, limit, got, tc.want)
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

// ref02Paginate walks the fixture page by page with the signed cursor and
// rejects any non-final page that is not exactly limit rows (LIMIT n+1 intact).
func ref02Paginate(t *testing.T, order string, limit int) []string {
	t.Helper()
	var ids []string
	token := ""
	for page := 0; page < 50; page++ {
		args := []string{"find", "/ref/events", "--order", order, "--limit", strconv.Itoa(limit)}
		if token != "" {
			args = append(args, "--cursor", token)
		}
		env, code := runCLI(t, args...)
		found := decode[findJSON](t, mustOK(t, env, code, args...))
		if env.Meta.Truncated && len(found.Rows) != limit {
			t.Fatalf("page %d has %d rows, want exactly %d", page+1, len(found.Rows), limit)
		}
		ids = append(ids, ref02IDs(found)...)
		if !env.Meta.Truncated {
			if env.Meta.NextCursor != "" {
				t.Errorf("final page promised a cursor %q", env.Meta.NextCursor)
			}
			return ids
		}
		if env.Meta.NextCursor == "" {
			t.Fatalf("page %d is truncated without a cursor", page+1)
		}
		token = env.Meta.NextCursor
	}
	t.Fatalf("pagination did not finish; ids so far %v", ids)
	return nil
}
