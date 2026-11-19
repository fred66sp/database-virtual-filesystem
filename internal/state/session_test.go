package state

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidSessionID(t *testing.T) {
	valid := []string{"s1", "agent-1", "agent.1", "A_b-9.0"}
	for _, id := range valid {
		if !ValidSessionID(id) {
			t.Errorf("ValidSessionID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", ".", "..", "a/b", `a\b`, "a b", "ñ", "a\x00b"}
	for _, id := range invalid {
		if ValidSessionID(id) {
			t.Errorf("ValidSessionID(%q) = true, want false", id)
		}
	}
}

func TestSessionRoundTripAndIsolation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := SaveSession(dir, "s1", "/work/tasks"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := SaveSession(dir, "s2", "/work/users"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	first, err := LoadSession(dir, "s1")
	if err != nil {
		t.Fatalf("LoadSession(s1): %v", err)
	}
	second, err := LoadSession(dir, "s2")
	if err != nil {
		t.Fatalf("LoadSession(s2): %v", err)
	}
	if first.Cwd != "/work/tasks" || second.Cwd != "/work/users" {
		t.Errorf("session cwds = %q, %q; want /work/tasks, /work/users", first.Cwd, second.Cwd)
	}

	info, err := os.Stat(filepath.Join(dir, "s1.json"))
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("session file permissions = %o, want 600", got)
	}
}

func TestLoadSessionStale(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSession(dir, "missing"); !errors.Is(err, ErrStaleSession) {
		t.Errorf("LoadSession(missing) error = %v, want ErrStaleSession", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write bad session: %v", err)
	}
	if _, err := LoadSession(dir, "bad"); !errors.Is(err, ErrStaleSession) {
		t.Errorf("LoadSession(bad) error = %v, want ErrStaleSession", err)
	}
	if _, err := LoadSession(dir, "a/b"); !errors.Is(err, ErrInvalid) {
		t.Errorf("LoadSession(invalid id) error = %v, want ErrInvalid", err)
	}
}

func TestCursorKeyIsStableAndPersisted(t *testing.T) {
	dir := t.TempDir()
	first, err := CursorKey(dir)
	if err != nil {
		t.Fatalf("CursorKey: %v", err)
	}
	second, err := CursorKey(dir)
	if err != nil {
		t.Fatalf("CursorKey again: %v", err)
	}
	if len(first) != 32 || string(first) != string(second) {
		t.Errorf("cursor keys = %d bytes then %d bytes; want one stable 32-byte key", len(first), len(second))
	}

	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config permissions = %o, want 600", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.CursorKey == "" {
		t.Error("config has no cursor_key after generation")
	}
}

func TestCursorKeyRejectsInvalid(t *testing.T) {
	for name, key := range map[string]string{
		"not base64":    "not base64!",
		"16 byte valid": base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{Version: Version, Mounts: map[string]Mount{}, CursorKey: key}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal config: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := CursorKey(dir); !errors.Is(err, ErrInvalid) {
				t.Errorf("CursorKey error = %v, want ErrInvalid", err)
			}
		})
	}
}
