package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingLeavesNothingBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db-ai")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Version != Version || len(cfg.Mounts) != 0 {
		t.Errorf("Load = %+v, want empty version %d config", cfg, Version)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load created %s (err %v), want no filesystem effect", dir, err)
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db-ai")
	err := Update(dir, func(cfg *Config) error {
		cfg.Mounts["work"] = Mount{File: "/data/tasks.db", Mode: "ro"}
		return nil
	})
	if err != nil {
		t.Fatalf("Update add: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Mounts["work"]; got.File != "/data/tasks.db" || got.Mode != "ro" {
		t.Errorf("mount = %+v, want the registered file and mode", got)
	}

	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config permissions = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("config directory permissions = %o, want 700", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var raw struct {
		Version int                       `json:"version"`
		Mounts  map[string]map[string]any `json:"mounts"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("config is not JSON: %v (%s)", err, data)
	}
	if raw.Version != 1 || raw.Mounts["work"]["mode"] != "ro" {
		t.Errorf("config JSON = %s, want version 1 and the mount", data)
	}

	if err := Update(dir, func(cfg *Config) error {
		delete(cfg.Mounts, "work")
		return nil
	}); err != nil {
		t.Fatalf("Update remove: %v", err)
	}
	cfg, err = Load(dir)
	if err != nil {
		t.Fatalf("Load after remove: %v", err)
	}
	if len(cfg.Mounts) != 0 {
		t.Errorf("mounts after remove = %v, want empty", cfg.Mounts)
	}
}

func TestUpdateTightensExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db-ai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := Update(dir, func(*Config) error { return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("existing config directory permissions = %o, want 700", got)
	}
}

func TestUpdatePropagatesMutationError(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("nope")
	err := Update(dir, func(*Config) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want %v", err, sentinel)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed Update wrote config.json (err %v)", err)
	}
}

func TestLoadRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"version":99,"mounts":{}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(dir)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load error = %v, want ErrInvalid", err)
	}
}

// TestLoadRejectsUnrepresentableAlias proves the registry never holds an
// alias the path codec cannot spell as /ALIAS (DESIGN §4, §9: operators edit
// config.json by hand).
func TestLoadRejectsUnrepresentableAlias(t *testing.T) {
	for _, alias := range []string{"foo/bar", "bad alias", "ñ"} {
		t.Run(alias, func(t *testing.T) {
			dir := t.TempDir()
			cfg := fmt.Sprintf(`{"version":1,"mounts":{%q:{"file":"/data/x.db","mode":"ro"}}}`, alias)
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(dir); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load alias %q error = %v, want ErrInvalid", alias, err)
			}
		})
	}
}

func TestAliasesSorted(t *testing.T) {
	cfg := Config{Mounts: map[string]Mount{"zeta": {}, "alpha": {}, "Mu": {}}}
	got := cfg.Aliases()
	want := []string{"Mu", "alpha", "zeta"} // byte-wise: uppercase first
	if len(got) != len(want) {
		t.Fatalf("Aliases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Aliases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
