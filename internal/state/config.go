// Package state owns the local state from DESIGN §9: the operator-editable
// mount registry in $XDG_CONFIG_HOME/db-ai/config.json and explicit session
// files in $XDG_STATE_HOME/db-ai/sessions. The audit log belongs to WP-04.
package state

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"dbvfs/internal/vfs"
)

// Version is the only config schema this build accepts.
const Version = 1

// ErrInvalid marks a config file this build cannot interpret (malformed JSON
// or an unknown version). Callers classify it once, at the CLI frontier.
var ErrInvalid = errors.New("invalid config")

// Policy is one operator-written allow entry. WP-03 applies it to the read
// side; WP-04 completes the policy engine for writes and listings.
type Policy struct {
	Ops    []string `json:"ops"`
	Prefix string   `json:"prefix"`
}

// Mount is one registered database. File is absolute and canonicalized; Mode
// is "ro" or "rw"; Allow is empty unless the operator granted writes (D-17).
type Mount struct {
	File  string   `json:"file"`
	Mode  string   `json:"mode"`
	Allow []Policy `json:"allow,omitempty"`
}

// Config is config.json. Mounts is never nil after Load. CursorKey is the HMAC
// key for pagination cursors (DESIGN §7); it is generated on first use.
type Config struct {
	Version   int              `json:"version"`
	Mounts    map[string]Mount `json:"mounts"`
	CursorKey string           `json:"cursor_key,omitempty"`
}

// Dir returns the db-ai configuration directory: $XDG_CONFIG_HOME/db-ai with
// the platform fallback (~/.config) that os.UserConfigDir already applies.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(base, "db-ai"), nil
}

// Load reads config.json. A missing file is an empty registry; reads never
// create or modify anything.
func Load(dir string) (Config, error) {
	cfg := Config{Version: Version, Mounts: map[string]Mount{}}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("%w: parse %s: %v", ErrInvalid, filepath.Join(dir, "config.json"), err)
	}
	if cfg.Version != Version {
		return Config{}, fmt.Errorf("%w: unsupported config version %d, want %d", ErrInvalid, cfg.Version, Version)
	}
	if cfg.Mounts == nil {
		cfg.Mounts = map[string]Mount{}
	}
	// Operators edit config.json by hand (DESIGN §9): an alias that the path
	// codec cannot represent as /ALIAS would break every command that reads
	// the registry. Same rule, single definition, in vfs (DESIGN §4).
	for alias := range cfg.Mounts {
		if !vfs.ValidAlias(alias) {
			return Config{}, fmt.Errorf("%w: mount alias %q is not [A-Za-z0-9_-]+", ErrInvalid, alias)
		}
	}
	return cfg, nil
}

// Update runs fn on the current config under an exclusive lock and, if fn
// succeeds, replaces config.json atomically in its own directory (DESIGN §9).
// Directories are created 0700 and the file is 0600.
func Update(dir string, fn func(*Config) error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	// Enforce the contract even when the directory already existed (DESIGN §9).
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set config directory permissions: %w", err)
	}
	release, err := acquireLock(dir)
	if err != nil {
		return err
	}
	defer release()

	cfg, err := Load(dir)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return save(dir, cfg)
}

// Aliases returns the registered aliases in byte-wise order so every command
// that lists mounts is deterministic (NFR-01).
func (c Config) Aliases() []string {
	aliases := make([]string, 0, len(c.Mounts))
	for alias := range c.Mounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}

// CursorKey returns the HMAC key for pagination cursors (DESIGN §7). The key
// is generated once and persisted in config.json so cursors stay verifiable
// across invocations; it is operator configuration, not session state.
func CursorKey(dir string) ([]byte, error) {
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if cfg.CursorKey != "" {
		return decodeCursorKey(cfg.CursorKey)
	}
	var key []byte
	err = Update(dir, func(c *Config) error {
		if c.CursorKey == "" {
			var b [32]byte
			if _, err := rand.Read(b[:]); err != nil {
				return fmt.Errorf("generate cursor key: %w", err)
			}
			c.CursorKey = base64.RawURLEncoding.EncodeToString(b[:])
		}
		var err error
		key, err = decodeCursorKey(c.CursorKey)
		return err
	})
	return key, err
}

func decodeCursorKey(encoded string) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: cursor_key must be base64url of exactly 32 bytes", ErrInvalid)
	}
	return key, nil
}

func save(dir string, cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil { // impossible: Config holds only strings, ints and slices
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "config.json")); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best effort; the rename already replaced the file
		_ = d.Close()
	}
	return nil
}
