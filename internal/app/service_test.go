package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dbvfs/internal/storage"
)

// TestMountRejectsInvalidAlias proves the registry enforces DESIGN §4 even when
// the CLI is bypassed. Service knows no exit codes; the frontier maps
// storage.ErrInvalid to E_USAGE.
func TestMountRejectsInvalidAlias(t *testing.T) {
	for _, alias := range []string{"bad/alias", "bad alias", ""} {
		t.Run(alias, func(t *testing.T) {
			_, err := (Service{ConfigDir: t.TempDir()}).Mount("ignored.db", alias, "ro")
			if !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("Mount alias %q error = %v, want storage.ErrInvalid (E_USAGE)", alias, err)
			}
		})
	}
}

func TestCheckMountFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	if err := checkMountFile(write("ok.db", sqliteMagic+"rest")); err != nil {
		t.Errorf("checkMountFile(valid header) = %v, want nil", err)
	}

	tests := []struct {
		name string
		path string
		want error
	}{
		{"missing", filepath.Join(dir, "absent.db"), storage.ErrNotFound},
		{"garbage", write("plain.txt", "not a database"), storage.ErrUnsupported},
		{"truncated", write("short.db", ""), storage.ErrUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkMountFile(tc.path); !errors.Is(err, tc.want) {
				t.Errorf("checkMountFile(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
