// Package memory holds the ACM memory core: sessions, observations,
// checkpoints, preferences, continuity, recall and the canonical content hash.
// It talks to db-ai only through its CLI; it does not import it as a package.
package memory

import "fmt"

// Codes of the contract errors the core reports. They are the same db-ai codes
// the Python adapter returned, so a consumer can keep switching on them.
const (
	codeUsage      = "E_USAGE"
	codeNotFound   = "E_NOT_FOUND"
	codeConflict   = "E_CONFLICT"
	codeConstraint = "E_CONSTRAINT"
	codeInternal   = "E_INTERNAL"
)

// Error is a contract error: the stable code and the message to show.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// errorf builds a contract error with a formatted message.
func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
