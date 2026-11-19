// This file owns explicit sessions (DESIGN §9, D-18): one file per session ID
// under $XDG_STATE_HOME/db-ai/sessions, holding the working directory. There is
// no global current directory, so two sessions never interfere.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrStaleSession marks a session whose directory no longer resolves: a
// removed session file, an unmounted alias, or a table/row that disappeared.
var ErrStaleSession = errors.New("stale session")

// sessionVersion is the only session schema this build accepts.
const sessionVersion = 1

// Session is the persisted state of one explicit session.
type Session struct {
	Version int    `json:"version"`
	Cwd     string `json:"cwd"`
}

// SessionsDir returns $XDG_STATE_HOME/db-ai/sessions with the documented
// fallback ~/.local/state/db-ai (DESIGN §9).
func SessionsDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

// StateDir returns $XDG_STATE_HOME/db-ai with the documented fallback
// ~/.local/state/db-ai. Sessions and the audit log live under it, never inside
// the user's database (DESIGN §9, §11).
func StateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "db-ai"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate state directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "db-ai"), nil
}

// ValidSessionID accepts the ASCII identity D-18 requires. A period is allowed
// but "." and ".." are not, so the ID is always a safe file name.
func ValidSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// LoadSession reads one session file. A missing or unreadable session is a
// stale context; an invalid ID is a usage error the caller can correct.
func LoadSession(dir, id string) (Session, error) {
	if !ValidSessionID(id) {
		return Session{}, fmt.Errorf("%w: session ID %q is not ASCII [A-Za-z0-9._-]+", ErrInvalid, id)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, fmt.Errorf("%w: session %q does not exist", ErrStaleSession, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session %q: %w", id, err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("%w: session %q is not valid JSON", ErrStaleSession, id)
	}
	if s.Version != sessionVersion || s.Cwd == "" {
		return Session{}, fmt.Errorf("%w: session %q has no usable working directory", ErrStaleSession, id)
	}
	return s, nil
}

// SaveSession replaces one session file under an exclusive lock and an atomic
// rename in the same directory, with 0700 directories and a 0600 file.
func SaveSession(dir, id, cwd string) error {
	if !ValidSessionID(id) {
		return fmt.Errorf("%w: session ID %q is not ASCII [A-Za-z0-9._-]+", ErrInvalid, id)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sessions directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set sessions directory permissions: %w", err)
	}
	release, err := acquireLockFile(filepath.Join(dir, "sessions.lock"))
	if err != nil {
		return err
	}
	defer release()

	data, err := json.Marshal(Session{Version: sessionVersion, Cwd: cwd})
	if err != nil { // impossible: Session holds an int and a string
		return fmt.Errorf("encode session: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".session-*.json")
	if err != nil {
		return fmt.Errorf("create temporary session: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set session permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write session: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, id+".json")); err != nil {
		return fmt.Errorf("replace session: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best effort; the rename already replaced the file
		_ = d.Close()
	}
	return nil
}
