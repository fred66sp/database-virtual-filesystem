package storage

import (
	"errors"
	"testing"
)

// TestSentinelsStayDistinct guards the CLI mapping: no sentinel may match
// another, or two codes would become indistinguishable.
func TestSentinelsStayDistinct(t *testing.T) {
	all := []error{ErrNotFound, ErrDenied, ErrConflict, ErrUnsupported, ErrInvalid}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", b, a)
			}
		}
	}
}
