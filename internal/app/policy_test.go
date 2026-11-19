package app

import (
	"testing"

	"dbvfs/internal/state"
)

func TestReadOpAllowedDefaultsToReadAccess(t *testing.T) {
	m := state.Mount{File: "/tmp/x.db", Mode: "ro"}
	if !readOpAllowed(m, "find", "/work/tasks") {
		t.Error("no policy should allow ordinary read access")
	}
	if !fieldAllowed(m, "find", "/work/tasks") {
		t.Error("no policy should allow table columns")
	}
}

func TestReadOpAllowedExplicitPolicyMatchesSegments(t *testing.T) {
	m := state.Mount{Allow: []state.Policy{
		{Ops: []string{"find", "cat"}, Prefix: "/work/tasks"},
		{Ops: []string{"ls"}, Prefix: "/work"},
	}}

	tests := []struct {
		op    string
		path  string
		allow bool
	}{
		{"find", "/work/tasks", true},
		{"find", "/work/tasks/1/title", true},
		{"cat", "/work/tasks", true},
		{"export", "/work/tasks", false},
		{"ls", "/work", true},
		{"ls", "/work/users", true},
		// String prefixes are not segment prefixes.
		{"find", "/work/task", false},
		{"find", "/work/tasks2", false},
		{"find", "/other/tasks", false},
	}
	for _, tc := range tests {
		if got := readOpAllowed(m, tc.op, tc.path); got != tc.allow {
			t.Errorf("readOpAllowed(%s, %s) = %v, want %v", tc.op, tc.path, got, tc.allow)
		}
	}
}

func TestFieldAllowedRejectsDeeperGrantForTableQuery(t *testing.T) {
	m := state.Mount{Allow: []state.Policy{
		{Ops: []string{"find"}, Prefix: "/work/tasks/1/title"},
	}}
	if fieldAllowed(m, "find", "/work/tasks") {
		t.Error("a field-level grant must not authorize a table-wide query")
	}
	if !readOpAllowed(m, "find", "/work/tasks/1/title") {
		t.Error("the field-level grant should still authorize that exact field")
	}
}

func TestReadOpAllowedIgnoresMalformedPolicy(t *testing.T) {
	m := state.Mount{Allow: []state.Policy{
		{Ops: []string{"find"}, Prefix: "work/tasks"},
		{Ops: []string{"find"}, Prefix: "/work//tasks"},
	}}
	if readOpAllowed(m, "find", "/work/tasks") {
		t.Error("malformed policy prefixes must not authorize anything")
	}
}
