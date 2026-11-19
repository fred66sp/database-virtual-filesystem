package app

import (
	"errors"
	"strings"
	"testing"

	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

func TestWriteOpAllowedDeniesByDefault(t *testing.T) {
	m := state.Mount{File: "/tmp/x.db", Mode: "rw"}
	for _, path := range []string{"/work/tasks", "/work/tasks/1", "/work/tasks/1/status"} {
		if writeOpAllowed(m, "write", path) {
			t.Errorf("writeOpAllowed with no policy on %s = true, want deny", path)
		}
	}
	if readOpAllowed(m, "cat", "/work/tasks") != true {
		t.Error("read defaults must stay as before: a ro mount with no policy is readable")
	}
}

func TestWriteOpAllowedMatchesWholeSegments(t *testing.T) {
	m := state.Mount{Allow: []state.Policy{
		{Ops: []string{"write"}, Prefix: "/work/tasks/1/status"},
		{Ops: []string{"touch"}, Prefix: "/work/tasks"},
	}}
	tests := []struct {
		op    string
		path  string
		allow bool
	}{
		{"write", "/work/tasks/1/status", true},
		{"write", "/work/tasks/1/title", false},
		{"write", "/work/tasks/1", false},
		{"write", "/work/tasks/10/status", false},
		{"touch", "/work/tasks", true},
		{"touch", "/work/tasks/1/status", true},
		{"touch", "/work/task", false},
		{"touch", "/work/tasks2", false},
	}
	for _, tc := range tests {
		if got := writeOpAllowed(m, tc.op, tc.path); got != tc.allow {
			t.Errorf("writeOpAllowed(%s, %s) = %v, want %v", tc.op, tc.path, got, tc.allow)
		}
	}
}

func TestParseMutationFields(t *testing.T) {
	field, err := vfs.Parse("/work/notes/1/title")
	if err != nil {
		t.Fatalf("parse field path: %v", err)
	}
	row, err := vfs.Parse("/work/notes/1")
	if err != nil {
		t.Fatalf("parse row path: %v", err)
	}
	table, err := vfs.Parse("/work/notes")
	if err != nil {
		t.Fatalf("parse table path: %v", err)
	}

	tests := []struct {
		name string
		p    vfs.Path
		data string
		want map[string]storage.Value
		err  error
	}{
		{
			name: "field natural string",
			p:    field,
			data: `"done"`,
			want: map[string]storage.Value{"title": {Kind: "text", Data: "done"}},
		},
		{
			name: "field typed wrapper",
			p:    field,
			data: `{"kind":"integer","data":"42"}`,
			want: map[string]storage.Value{"title": {Kind: "integer", Data: "42"}},
		},
		{
			name: "row natural map",
			p:    row,
			data: `{"title":"a","note":null,"big":12,"ratio":1.5}`,
			want: map[string]storage.Value{
				"title": {Kind: "text", Data: "a"},
				"note":  {Kind: "null"},
				"big":   {Kind: "integer", Data: "12"},
				"ratio": {Kind: "real", Data: "1.5"},
			},
		},
		{
			name: "table typed values",
			p:    table,
			data: `{"id":{"kind":"integer","data":"7"},"title":"x"}`,
			want: map[string]storage.Value{
				"id":    {Kind: "integer", Data: "7"},
				"title": {Kind: "text", Data: "x"},
			},
		},
		{
			name: "duplicate key",
			p:    row,
			data: `{"title":"a","title":"b"}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "trailing data",
			p:    row,
			data: `{"title":"a"} {"title":"b"}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "array value",
			p:    row,
			data: `{"title":[1]}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "object without kind",
			p:    field,
			data: `{"value":"x"}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "unknown wrapper kind",
			p:    field,
			data: `{"kind":"date","data":"x"}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "non-canonical integer wrapper",
			p:    field,
			data: `{"kind":"integer","data":"007"}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "empty table map",
			p:    table,
			data: `{}`,
			want: map[string]storage.Value{},
		},
		{
			name: "empty row map",
			p:    row,
			data: `{}`,
			err:  storage.ErrInvalid,
		},
		{
			name: "null touch payload",
			p:    table,
			data: `null`,
			err:  storage.ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMutationFields(tc.p, []byte(tc.data))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("parseMutationFields(%s) error = %v, want %v", tc.data, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMutationFields(%s): %v", tc.data, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseMutationFields(%s) = %+v, want %+v", tc.data, got, tc.want)
			}
			for name, want := range tc.want {
				if got[name] != want {
					t.Errorf("field %q = %+v, want %+v", name, got[name], want)
				}
			}
		})
	}
}

func TestAuditResourceRedactsUnauthorizedKey(t *testing.T) {
	row, err := vfs.Parse("/work/tasks/42/title")
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}

	open := state.Mount{Mode: "rw"}
	resource, table := auditResource(open, row)
	if resource != "/work/tasks/42/title" || table != "" {
		t.Errorf("default read policy resource = %q/%q, want the full path", resource, table)
	}

	granted := state.Mount{Mode: "rw", Allow: []state.Policy{
		{Ops: []string{"cat"}, Prefix: "/work/tasks/42"},
	}}
	resource, _ = auditResource(granted, row)
	if resource != "/work/tasks/42/title" {
		t.Errorf("authorized resource = %q, want the full path", resource)
	}

	denied := state.Mount{Mode: "rw", Allow: []state.Policy{
		{Ops: []string{"cat"}, Prefix: "/work/tasks/42/title"},
	}}
	resource, table = auditResource(denied, row)
	if table != "tasks" {
		t.Errorf("redacted table = %q, want tasks", table)
	}
	if !strings.HasPrefix(resource, "sha256:") || len(resource) != len("sha256:")+16 {
		t.Errorf("redacted resource = %q, want sha256:<16 hex>", resource)
	}
	if strings.Contains(resource, "42") {
		t.Errorf("redacted resource leaks the key: %q", resource)
	}
}
