package state

// LockFile takes an exclusive lock on path and returns the release function.
// It serializes appends to state files across processes, such as the audit log
// (DESIGN §11); the platform-specific implementation decides crash behavior.
func LockFile(path string) (func(), error) { return acquireLockFile(path) }
