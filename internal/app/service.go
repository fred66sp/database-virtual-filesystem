// Package app is the application service layer from DESIGN §2: it resolves
// virtual paths against the mount registry, opens the SQLite adapter and
// shapes the JSON data each read command returns. It knows no exit codes.
package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/storage/sqlite"
	"dbvfs/internal/vfs"
)

// Service is the command implementation shared by the CLI layer. ConfigDir is
// the db-ai config directory and StateDir the sessions directory; both are
// injected so tests never touch the real ones.
type Service struct {
	ConfigDir string
	StateDir  string
	Timeout   time.Duration
}

// MountInfo is the deterministic shape mount, umount and mounts emit.
type MountInfo struct {
	Alias string `json:"alias"`
	File  string `json:"file"`
	Mode  string `json:"mode"`
}

const sqliteMagic = "SQLite format 3\x00"

// Mount registers an existing SQLite file under an alias. The file is never
// created, opened read-write here, or modified.
func (s Service) Mount(file, alias, mode string) (MountInfo, error) {
	// Defense in depth: the CLI validates the alias too, but the registry owns
	// the invariant and must not depend on its caller (DESIGN §4).
	if !vfs.ValidAlias(alias) {
		return MountInfo{}, fmt.Errorf("%w: alias %q is not [A-Za-z0-9_-]+", storage.ErrInvalid, alias)
	}

	// DESIGN §9 and D-17: a read-write mount needs an explicit allow policy for
	// the mount. The CLI has no flag for that policy (config.json is operator
	// territory), and WP-02 does not implement the policy engine, so a
	// read-write registration is a policy refusal, not a syntax error.
	if mode == "rw" {
		return MountInfo{}, fmt.Errorf("%w: --mode rw requires an explicit allow policy on the mount; WP-02 registers read-only mounts only (DESIGN §9, D-17)", storage.ErrDenied)
	}

	resolved, err := validateFile(file)
	if err != nil {
		return MountInfo{}, err
	}
	st, err := sqlite.Open(sqlite.Options{Path: resolved, Mode: mode, Timeout: s.timeout()})
	if err != nil {
		return MountInfo{}, fmt.Errorf("%w: cannot open %q as SQLite: %v", storage.ErrUnsupported, file, err)
	}
	if err := st.Close(); err != nil {
		return MountInfo{}, fmt.Errorf("close probe connection: %w", err)
	}

	err = state.Update(s.ConfigDir, func(cfg *state.Config) error {
		if _, ok := cfg.Mounts[alias]; ok {
			return fmt.Errorf("%w: mount %q is already registered", storage.ErrConflict, alias)
		}
		cfg.Mounts[alias] = state.Mount{File: resolved, Mode: mode}
		return nil
	})
	if err != nil {
		return MountInfo{}, err
	}
	return MountInfo{Alias: alias, File: resolved, Mode: mode}, nil
}

// Umount drops the alias from the registry. The database file is untouched.
func (s Service) Umount(alias string) (MountInfo, error) {
	var removed state.Mount
	err := state.Update(s.ConfigDir, func(cfg *state.Config) error {
		m, ok := cfg.Mounts[alias]
		if !ok {
			return fmt.Errorf("%w: mount %q is not registered", storage.ErrNotFound, alias)
		}
		removed = m
		delete(cfg.Mounts, alias)
		return nil
	})
	if err != nil {
		return MountInfo{}, err
	}
	return MountInfo{Alias: alias, File: removed.File, Mode: removed.Mode}, nil
}

// Mounts lists registered aliases in byte-wise order (NFR-01).
func (s Service) Mounts() ([]MountInfo, error) {
	cfg, err := state.Load(s.ConfigDir)
	if err != nil {
		return nil, err
	}
	out := make([]MountInfo, 0, len(cfg.Mounts))
	for _, alias := range cfg.Aliases() {
		m := cfg.Mounts[alias]
		out = append(out, MountInfo{Alias: alias, File: m.File, Mode: m.Mode})
	}
	return out, nil
}

func (s Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 5 * time.Second
	}
	return s.Timeout
}

// mount resolves an alias to its registry entry.
func (s Service) mount(alias string) (state.Mount, error) {
	cfg, err := state.Load(s.ConfigDir)
	if err != nil {
		return state.Mount{}, err
	}
	m, ok := cfg.Mounts[alias]
	if !ok {
		return state.Mount{}, fmt.Errorf("%w: mount %q is not registered", storage.ErrNotFound, alias)
	}
	return m, nil
}

// open resolves an alias to its registered file and opens a store for it.
func (s Service) open(alias string) (*sqlite.Store, error) {
	m, err := s.mount(alias)
	if err != nil {
		return nil, err
	}
	if err := checkMountFile(m.File); err != nil {
		return nil, err
	}
	st, err := sqlite.Open(sqlite.Options{Path: m.File, Mode: m.Mode, Timeout: s.timeout()})
	if err != nil {
		return nil, fmt.Errorf("open mount %q: %w", alias, err)
	}
	return st, nil
}

// checkMountFile classifies a registered mount's file before the driver opens
// it. A file replaced or truncated after mount makes the driver fail with an
// opaque error, so the cheap 16-byte magic read keeps the result deterministic
// (DESIGN §10: a non-SQLite mount is unsupported, not internal).
func checkMountFile(name string) error {
	f, err := os.Open(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: database file %q is missing", storage.ErrNotFound, name)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: cannot read database file %q", storage.ErrDenied, name)
	case err != nil:
		return fmt.Errorf("open database file: %w", err)
	}
	header := make([]byte, len(sqliteMagic))
	_, readErr := io.ReadFull(f, header)
	closeErr := f.Close()
	if readErr != nil || string(header) != sqliteMagic {
		return fmt.Errorf("%w: database file %q is not a SQLite 3 database", storage.ErrUnsupported, name)
	}
	if closeErr != nil {
		return fmt.Errorf("close database file: %w", closeErr)
	}
	return nil
}

// validateFile enforces DESIGN §9: an existing regular file whose first 16
// bytes are the SQLite header. Nothing is opened for writing.
func validateFile(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: database file %q does not exist", storage.ErrNotFound, name)
	case errors.Is(err, fs.ErrPermission):
		return "", fmt.Errorf("%w: cannot read database file %q", storage.ErrDenied, name)
	case err != nil:
		return "", fmt.Errorf("stat database file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", storage.ErrInvalid, name)
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open database file: %w", err)
	}
	header := make([]byte, len(sqliteMagic))
	_, readErr := io.ReadFull(f, header)
	closeErr := f.Close()
	if readErr != nil {
		return "", fmt.Errorf("%w: %q is not a SQLite 3 database", storage.ErrInvalid, name)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close database file: %w", closeErr)
	}
	if string(header) != sqliteMagic {
		return "", fmt.Errorf("%w: %q is not a SQLite 3 database", storage.ErrInvalid, name)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize database path: %w", err)
	}
	return resolved, nil
}
