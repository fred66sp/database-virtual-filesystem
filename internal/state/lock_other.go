//go:build !unix

package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// acquireLock is the portable fallback: exclusive creation of a lock file.
// Shortcut: a crash leaves the lock behind until it is removed by hand; replace
// this with a native advisory lock before Windows is supported (NFR-06).
func acquireLock(dir string) (func(), error) {
	path := filepath.Join(dir, "config.lock")
	for range 500 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("lock config: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("timed out waiting for the config lock")
}
