package app

import (
	"fmt"
	"slices"
	"strings"

	"dbvfs/internal/state"
	"dbvfs/internal/storage"
)

// readOpAllowed reports whether an explicit allow policy grants op on path.
// With no allow entries the DESIGN §9 default applies: a read-only mount may
// read ordinary tables. An explicit policy replaces that default, denies by
// default and matches by whole segments, never by string prefix.
func readOpAllowed(m state.Mount, op, path string) bool {
	if len(m.Allow) == 0 {
		return true
	}
	return allowMatches(m, op, path)
}

// writeOpAllowed is the mutation side of FR-13 and D-17: there is no default
// grant at all. An explicit allow entry must cover the operation and the whole
// path, segment by segment; a read-write mount without a policy writes nothing.
func writeOpAllowed(m state.Mount, op, path string) bool {
	if len(m.Allow) == 0 {
		return false
	}
	return allowMatches(m, op, path)
}

func allowMatches(m state.Mount, op, path string) bool {
	target, ok := pathSegments(path)
	if !ok {
		return false
	}
	for _, allow := range m.Allow {
		if !slices.Contains(allow.Ops, op) {
			continue
		}
		prefix, ok := pathSegments(allow.Prefix)
		if !ok {
			continue
		}
		if segmentPrefix(prefix, target) {
			return true
		}
	}
	return false
}

// fieldAllowed reports whether a table-wide query may reference the columns of
// tablePath. A grant at the table or above covers every column. A grant deeper
// than the table (a row or a field) never authorizes a table-wide read, because
// that would expose other rows; this is the guard that makes a filter on a
// prohibited field an E_DENIED instead of a bypass (FR-13).
func fieldAllowed(m state.Mount, op, tablePath string) bool {
	if len(m.Allow) == 0 {
		return true
	}
	return readOpAllowed(m, op, tablePath)
}

func pathSegments(p string) ([]string, bool) {
	if !strings.HasPrefix(p, "/") {
		return nil, false
	}
	if p == "/" {
		return nil, true
	}
	segs := strings.Split(p[1:], "/")
	for _, seg := range segs {
		if seg == "" {
			return nil, false
		}
	}
	return segs, true
}

func segmentPrefix(prefix, target []string) bool {
	if len(prefix) > len(target) {
		return false
	}
	for i := range prefix {
		if prefix[i] != target[i] {
			return false
		}
	}
	return true
}

func policyDenied(op, field, path string) error {
	return fmt.Errorf("%w: policy does not allow %s on %q in %s", storage.ErrDenied, op, field, path)
}
