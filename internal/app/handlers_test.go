package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dbvfs/internal/storage"
)

func TestResolveKeyTypesAgainstPrimaryKey(t *testing.T) {
	intTable := storage.Table{
		Name:    "tasks",
		Columns: []storage.Column{{Name: "id", DeclaredType: "INTEGER", PKOrder: 1}},
	}
	textTable := storage.Table{
		Name:    "users",
		Columns: []storage.Column{{Name: "id", DeclaredType: "TEXT", PKOrder: 1}},
	}
	composite := storage.Table{
		Name: "tags",
		Columns: []storage.Column{
			{Name: "name", DeclaredType: "TEXT", PKOrder: 1},
			{Name: "kind", DeclaredType: "TEXT", PKOrder: 2},
		},
	}
	blobKey := storage.Table{
		Name:    "blobs",
		Columns: []storage.Column{{Name: "id", DeclaredType: "BLOB", PKOrder: 1}},
	}

	tests := []struct {
		name  string
		table storage.Table
		key   storage.Key
		want  storage.Key
		err   error
	}{
		{
			name:  "integer from a plain segment",
			table: intTable,
			key:   storage.Key{{Kind: "text", Data: "42"}},
			want:  storage.Key{{Kind: "integer", Data: "42"}},
		},
		{
			name:  "non-canonical integer",
			table: intTable,
			key:   storage.Key{{Kind: "text", Data: "007"}},
			err:   storage.ErrNotFound,
		},
		{
			name:  "text stays text",
			table: textTable,
			key:   storage.Key{{Kind: "text", Data: "42"}},
			want:  storage.Key{{Kind: "text", Data: "42"}},
		},
		{
			name:  "composite keeps order",
			table: composite,
			key:   storage.Key{{Kind: "text", Data: "a"}, {Kind: "text", Data: "b"}},
			want:  storage.Key{{Kind: "text", Data: "a"}, {Kind: "text", Data: "b"}},
		},
		{
			name:  "composite count mismatch",
			table: composite,
			key:   storage.Key{{Kind: "text", Data: "a"}},
			err:   storage.ErrNotFound,
		},
		{
			name:  "kind does not match declared TEXT",
			table: textTable,
			key:   storage.Key{{Kind: "integer", Data: "1"}},
			err:   storage.ErrNotFound,
		},
		{
			name:  "non-navigable affinity",
			table: blobKey,
			key:   storage.Key{{Kind: "text", Data: "x"}},
			err:   storage.ErrUnsupported,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveKey(tc.key, tc.table)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("resolveKey(%+v) error = %v, want %v", tc.key, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveKey: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("resolveKey = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("key[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestValidateFile(t *testing.T) {
	dir := t.TempDir()

	if _, err := validateFile(filepath.Join(dir, "absent.db")); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing file error = %v, want storage.ErrNotFound", err)
	}

	plain := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(plain, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	if _, err := validateFile(plain); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("plain file error = %v, want storage.ErrInvalid", err)
	}

	short := filepath.Join(dir, "short.db")
	if err := os.WriteFile(short, []byte("SQLite"), 0o600); err != nil {
		t.Fatalf("write short file: %v", err)
	}
	if _, err := validateFile(short); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("short file error = %v, want storage.ErrInvalid", err)
	}
}
