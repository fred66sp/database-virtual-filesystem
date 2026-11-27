package memory

import (
	"encoding/json"
	"os"
	"testing"
)

// TestContentHashGoldens fixes the hash byte by byte against the fixtures
// generated with tools/content-hash.py. The hash is a table shared with every
// existing base, so a difference here is a blocker, not a formatting change.
func TestContentHashGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/contenthash.golden.json")
	if err != nil {
		t.Fatalf("read goldens: %v", err)
	}
	var cases []struct {
		Name  string         `json:"name"`
		Table string         `json:"table"`
		Data  map[string]any `json:"data"`
		Hash  string         `json:"hash"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decode goldens: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no golden cases")
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			got, err := ContentHash(testCase.Table, testCase.Data)
			if err != nil {
				t.Fatalf("ContentHash(%s): %v", testCase.Table, err)
			}
			if got != testCase.Hash {
				t.Errorf("ContentHash(%s, %v):\n got  %s\n want %s",
					testCase.Table, testCase.Data, got, testCase.Hash)
			}
		})
	}
}

func TestNormalizeUsesPythonWhitespace(t *testing.T) {
	got := normalize("  a\x1cb\x1dc\u00a0d\u2028e\u3000f  ")
	want := "a b c d e f"
	if got != want {
		t.Errorf("normalize(python whitespace):\n got  %q\n want %q", got, want)
	}
}

func TestQuoteLeavesRFC8259RunesRaw(t *testing.T) {
	got := quote("<>&\u2028\u2029\U0001F600")
	want := "\"<>&\u2028\u2029\U0001F600\""
	if got != want {
		t.Errorf("quote:\n got  %q\n want %q", got, want)
	}
}
