package storage

import "errors"

// Sentinels shared by adapters, application services and the CLI frontier.
// The CLI maps each to one code and exit group from DESIGN §10; adapters only
// classify, they never know process codes.
var (
	// ErrNotFound marks an absent file, mount, table or row.
	ErrNotFound = errors.New("not found")
	// ErrDenied marks a policy or access-mode refusal.
	ErrDenied = errors.New("denied")
	// ErrConflict marks a duplicate alias or a failing precondition.
	ErrConflict = errors.New("conflict")
	// ErrUnsupported marks a capability outside this release (views, filters,
	// cursors, mutations, non-navigable identities).
	ErrUnsupported = errors.New("unsupported")
	// ErrInvalid marks an invalid value the caller can correct.
	ErrInvalid = errors.New("invalid")
)
