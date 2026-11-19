package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"dbvfs/internal/vfs"
)

// Code is a stable machine-readable error code from DESIGN §10. It is the
// source of truth for an agent; exit codes are the coarse group.
type Code string

const (
	CodeOK            Code = "OK"
	CodeUsage         Code = "E_USAGE"
	CodeNotFound      Code = "E_NOT_FOUND"
	CodeStaleSession  Code = "E_STALE_SESSION"
	CodeDenied        Code = "E_DENIED"
	CodeConflict      Code = "E_CONFLICT"
	CodeConstraint    Code = "E_CONSTRAINT"
	CodeBusy          Code = "E_BUSY"
	CodeTimeout       Code = "E_TIMEOUT"
	CodeUnsupported   Code = "E_UNSUPPORTED"
	CodeLimit         Code = "E_LIMIT"
	CodeInternal      Code = "E_INTERNAL"
	CodeCommitUnknown Code = "E_COMMIT_UNKNOWN"
)

// Exit maps every code in DESIGN §10 to its process exit status.
func (c Code) Exit() int {
	switch c {
	case CodeOK:
		return 0
	case CodeUsage:
		return 2
	case CodeNotFound, CodeStaleSession:
		return 3
	case CodeDenied:
		return 4
	case CodeConflict:
		return 5
	case CodeConstraint:
		return 6
	case CodeBusy, CodeTimeout:
		return 7
	case CodeUnsupported, CodeLimit:
		return 8
	case CodeCommitUnknown:
		return 10
	}
	return 9 // E_INTERNAL and anything unclassified
}

// Error is a coded error at the CLI frontier. Message is public and
// deterministic; Err keeps the cause for diagnostics.
type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// CodedError classifies err once, at the boundary. Errors that already carry a
// code keep it; foreign errors are mapped with errors.Is. Unclassified errors
// are internal and their public message stays generic (DESIGN §10, skill
// go-errors-and-lifecycle).
func CodedError(err error) *Error {
	var coded *Error
	if errors.As(err, &coded) {
		return coded
	}
	switch {
	case errors.Is(err, vfs.ErrInvalidPath):
		return &Error{Code: CodeUsage, Message: err.Error(), Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeTimeout, Message: "operation timed out", Err: err}
	case errors.Is(err, os.ErrNotExist):
		return &Error{Code: CodeNotFound, Message: "resource not found", Err: err}
	case errors.Is(err, context.Canceled):
		// DESIGN §10 has no cancellation exit group, so it stays internal.
		return &Error{Code: CodeInternal, Message: "operation canceled", Err: err}
	}
	return &Error{Code: CodeInternal, Message: "internal error", Err: err}
}

func usageError(format string, args ...any) *Error {
	return codedError(CodeUsage, format, args...)
}

func codedError(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
