package vfs

import (
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"dbvfs/internal/storage"
)

// composite is the canonical segment from DESIGN §4 for the key
// [{"kind":"text","data":"a"},{"kind":"integer","data":"42"}].
const composite = "~kW3sia2luZCI6InRleHQiLCJkYXRhIjoiYSJ9LHsia2luZCI6ImludGVnZXIiLCJkYXRhIjoiNDIifV0"

func TestParse(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		want          Path
		wantCanonical string
	}{
		{"root", "/", Path{Kind: KindRoot}, "/"},
		{"mount", "/work", Path{Kind: KindMount, Alias: "work"}, "/work"},
		{"table", "/work/tasks", Path{Kind: KindTable, Alias: "work", Table: "tasks"}, "/work/tasks"},
		{"row text", "/work/tasks/TK-001", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "TK-001"}}}, "/work/tasks/TK-001"},
		{"field", "/work/tasks/TK-001/status", Path{Kind: KindField, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "TK-001"}}, Field: "status"}, "/work/tasks/TK-001/status"},
		{"escaped slash", "/work/tasks/a%2Fb", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "a/b"}}}, "/work/tasks/a%2Fb"},
		{"empty text key", "/work/tasks/~e", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: ""}}}, "/work/tasks/~e"},
		{"literal tilde", "/work/tasks/%7Ee", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "~e"}}}, "/work/tasks/%7Ee"},
		{"literal dot", "/work/tasks/%2E", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "."}}}, "/work/tasks/%2E"},
		{"literal dot dot", "/work/tasks/%2E%2E", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: ".."}}}, "/work/tasks/%2E%2E"},
		{"dot inside name", "/work/tasks/a.b", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "a.b"}}}, "/work/tasks/a.b"},
		{"plain number is text", "/work/tasks/42", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "42"}}}, "/work/tasks/42"},
		{"space encoded", "/work/tasks/a%20b", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "a b"}}}, "/work/tasks/a%20b"},
		{"utf-8 bytes", "/work/tasks/caf%C3%A9", Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "café"}}}, "/work/tasks/caf%C3%A9"},
		{"composite key", "/work/tasks/" + composite, Path{Kind: KindRow, Alias: "work", Table: "tasks", Key: storage.Key{{Kind: "text", Data: "a"}, {Kind: "integer", Data: "42"}}}, "/work/tasks/" + composite},
		{"resolve dot", "/work/./tasks", Path{Kind: KindTable, Alias: "work", Table: "tasks"}, "/work/tasks"},
		{"resolve dot dot", "/work/tasks/../users", Path{Kind: KindTable, Alias: "work", Table: "users"}, "/work/users"},
		{"dot dot to mount", "/work/tasks/..", Path{Kind: KindMount, Alias: "work"}, "/work"},
		{"alias charset", "/a-b_c", Path{Kind: KindMount, Alias: "a-b_c"}, "/a-b_c"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			canonical, err := got.Canonical()
			if err != nil {
				t.Fatalf("Canonical(%q) error: %v", tc.in, err)
			}
			if canonical != tc.wantCanonical {
				t.Errorf("Canonical(%q) = %q, want %q", tc.in, canonical, tc.wantCanonical)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	encoded := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"relative", "work/tasks"},
		{"trailing slash", "/work/"},
		{"empty segment", "/work//tasks"},
		{"escape root", "/../work"},
		{"escape root single", "/.."},
		{"nul encoded", "/work/tasks/%00"},
		{"nul raw", "/work/tasks/\x00"},
		{"lowercase hex", "/work/tasks/a%2fb"},
		{"unreserved escape", "/work/tasks/%41"},
		{"raw utf-8", "/work/tasks/café"},
		{"raw space", "/work/tasks/a b"},
		{"truncated escape", "/work/tasks/%2"},
		{"lowercase hex escape", "/work/tasks/%7e"},
		{"unknown marker", "/work/tasks/~x"},
		{"composite bad base64", "/work/tasks/~k!!"},
		{"composite empty payload", "/work/tasks/~k"},
		{"composite empty array", "/work/tasks/~k" + encoded("[]")},
		{"composite single value", "/work/tasks/~k" + encoded(`[{"kind":"integer","data":"70"}]`)},
		{"composite not canonical", "/work/tasks/~k" + encoded(`[ {"kind":"text","data":"a"} ]`)},
		{"composite non-canonical integer", "/work/tasks/~k" + encoded(`[{"kind":"integer","data":"042"}]`)},
		{"composite real kind", "/work/tasks/~k" + encoded(`[{"kind":"real","data":"1.5"}]`)},
		{"composite unknown field", "/work/tasks/~k" + encoded(`[{"kind":"text","data":"a","extra":1}]`)},
		{"composite object not array", "/work/tasks/~k" + encoded(`{"kind":"text","data":"a"}`)},
		{"bad alias", "/a.b"},
		{"too deep", "/work/tasks/TK-001/status/extra"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.in); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidPath", tc.in, err)
			}
		})
	}
}

func TestCanonicalKeys(t *testing.T) {
	tests := []struct {
		name string
		key  storage.Key
		want string
	}{
		{"empty text", storage.Key{{Kind: "text", Data: ""}}, "~e"},
		{"slash", storage.Key{{Kind: "text", Data: "a/b"}}, "a%2Fb"},
		{"tilde", storage.Key{{Kind: "text", Data: "~e"}}, "%7Ee"},
		{"dot", storage.Key{{Kind: "text", Data: "."}}, "%2E"},
		{"dot dot", storage.Key{{Kind: "text", Data: ".."}}, "%2E%2E"},
		{"percent", storage.Key{{Kind: "text", Data: "50%"}}, "50%25"},
		{"integer", storage.Key{{Kind: "integer", Data: "42"}}, "42"},
		{"negative integer", storage.Key{{Kind: "integer", Data: "-7"}}, "-7"},
		{"composite", storage.Key{{Kind: "text", Data: "a"}, {Kind: "integer", Data: "42"}}, composite},
		{"composite empty text", storage.Key{{Kind: "integer", Data: "42"}, {Kind: "text", Data: ""}}, "~k" + base64.RawURLEncoding.EncodeToString([]byte(`[{"kind":"integer","data":"42"},{"kind":"text","data":""}]`))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Path{Kind: KindRow, Alias: "w", Table: "t", Key: tc.key}
			got, err := p.Canonical()
			if err != nil {
				t.Fatalf("Canonical(%v) error: %v", tc.key, err)
			}
			want := "/w/t/" + tc.want
			if got != want {
				t.Errorf("Canonical(%v) = %q, want %q", tc.key, got, want)
			}
			back, err := Parse(got)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", got, err)
			}
			// A bare decimal key parses back as text: the path alone cannot
			// tell INTEGER 42 from TEXT "42"; the catalog layer decides.
			backCanonical, err := back.Canonical()
			if err != nil {
				t.Fatalf("Canonical(Parse(%q)) error: %v", got, err)
			}
			if backCanonical != got {
				t.Errorf("roundtrip %q -> %q, want identity", got, backCanonical)
			}
		})
	}
}

func TestCanonicalRejectsUnnavigableKeys(t *testing.T) {
	tests := []struct {
		name string
		key  storage.Key
	}{
		{"nil key", nil},
		{"empty key", storage.Key{}},
		{"non-canonical integer", storage.Key{{Kind: "integer", Data: "042"}}},
		{"plus integer", storage.Key{{Kind: "integer", Data: "+1"}}},
		{"integer overflow", storage.Key{{Kind: "integer", Data: "99999999999999999999"}}},
		{"real simple key", storage.Key{{Kind: "real", Data: "1.5"}}},
		{"blob simple key", storage.Key{{Kind: "blob", Data: "AAAA"}}},
		{"real in composite", storage.Key{{Kind: "real", Data: "1.5"}, {Kind: "text", Data: "x"}}},
		{"invalid utf-8 in composite text", storage.Key{{Kind: "text", Data: "\xff"}, {Kind: "text", Data: "x"}}},
		{"nul in simple text", storage.Key{{Kind: "text", Data: "a\x00b"}}},
		{"nul in composite text", storage.Key{{Kind: "text", Data: "a\x00b"}, {Kind: "text", Data: "x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Path{Kind: KindRow, Alias: "w", Table: "t", Key: tc.key}
			if _, err := p.Canonical(); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Canonical(%v) error = %v, want ErrInvalidPath", tc.key, err)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"/",
		"/work",
		"/work/tasks/TK-001/status",
		"/work/tasks/a%2Fb",
		"/work/tasks/~e",
		"/work/tasks/" + composite,
		"/work/tasks/%00",
		"/work/./tasks",
		"/../x",
		"/work/tasks/42",
		"relative",
		"/a.b",
		"/work//tasks",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		p, err := Parse(raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("Parse(%q) returned an unclassified error: %v", raw, err)
			}
			return
		}
		canonical, err := p.Canonical()
		if err != nil {
			t.Fatalf("Parse(%q) accepted a path Canonical rejects: %v", raw, err)
		}
		again, err := Parse(canonical)
		if err != nil {
			t.Fatalf("Parse(%q) -> %q does not parse back: %v", raw, canonical, err)
		}
		if !reflect.DeepEqual(p, again) {
			t.Fatalf("roundtrip mismatch for %q: %+v != %+v", raw, p, again)
		}
	})
}
