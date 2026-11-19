//go:build unix

package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock serializes config updates across processes with flock, which the
// kernel releases if the process dies. The lock lives in its own file because
// the config itself is replaced by rename.
func acquireLock(dir string) (func(), error) {
	return acquireLockFile(filepath.Join(dir, "config.lock"))
}

// acquireLockFile takes an exclusive flock on path.
func acquireLockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
