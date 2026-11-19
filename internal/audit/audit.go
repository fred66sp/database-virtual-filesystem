// Package audit appends the local JSONL audit log from DESIGN §11 outside the
// user's database. It records identifiers, never data values: the caller
// redacts the resource according to the actor's policy (D-9).
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dbvfs/internal/state"
)

// FileName is the log inside the state directory; LockName serializes appends.
const (
	FileName = "audit.jsonl"
	LockName = "audit.lock"
)

// Record is one JSONL line. Code is the stable error code ("OK" on success) and
// Status is attempt, ok, denied, error, unknown or dry-run.
type Record struct {
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
	Actor     string `json:"actor"`
	Operation string `json:"operation"`
	Alias     string `json:"alias"`
	Table     string `json:"table,omitempty"`
	Resource  string `json:"resource"`
	Status    string `json:"status"`
	Code      string `json:"code"`
}

// Append writes one line durably under an exclusive lock. A caller that mutates
// data must call it before the mutation and abort if it fails (DESIGN §11).
func Append(dir string, r Record) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set audit directory permissions: %w", err)
	}
	r.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(r)
	if err != nil { // impossible: Record holds only strings
		return fmt.Errorf("encode audit record: %w", err)
	}
	line = append(line, '\n')

	release, err := state.LockFile(filepath.Join(dir, LockName))
	if err != nil {
		return err
	}
	defer release()

	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("write audit log: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync audit log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	// Best effort: make the directory entry durable too, as state.save does.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
