package cli

import (
	"bytes"
	"strings"
	"testing"

	"dbvfs/internal/state"
)

// TestRef01FindGoldenWithoutFields fixes the unprojected output: --fields must
// never change what find emits when it is absent.
func TestRef01FindGoldenWithoutFields(t *testing.T) {
	setupAlpha(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"find", "/alpha/tasks", "--request-id", "req-ref01"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("find exit = %d, stderr = %q; want success", code, stderr.String())
	}
	checkGolden(t, "find_default.golden", stdout.Bytes())
}

// TestRef01FindProjection covers the projection itself: the PK is always
// emitted, unrequested columns are gone, the catalog order is kept and the
// ETag is the row's, not the projection's.
func TestRef01FindProjection(t *testing.T) {
	setupAlpha(t)

	fullEnv, fullCode := runCLI(t, "find", "/alpha/tasks")
	full := decode[findJSON](t, mustOK(t, fullEnv, fullCode, "find", "/alpha/tasks"))

	env, code := runCLI(t, "find", "/alpha/tasks", "--fields", "title")
	page := decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks", "--fields", "title"))
	if len(page.Rows) != 2 {
		t.Fatalf("projected page = %+v, want two rows", page)
	}
	for _, row := range page.Rows {
		if len(row.Fields) != 2 {
			t.Errorf("projected row %s = %+v, want only the key and title", row.Path, row.Fields)
		}
		if _, ok := row.Fields["id"]; !ok {
			t.Errorf("projected row %s lost the key: %+v", row.Path, row.Fields)
		}
		if _, ok := row.Fields["title"]; !ok {
			t.Errorf("projected row %s lost the requested column: %+v", row.Path, row.Fields)
		}
	}

	for i, row := range page.Rows {
		if row.ETag != full.Rows[i].ETag {
			t.Errorf("row %s ETag = %q with --fields, %q without; want the same",
				row.Path, row.ETag, full.Rows[i].ETag)
		}
	}

	// The key is emitted in catalog order even when it was not requested:
	// "id" precedes "note" in tasks, whatever order --fields used.
	env, code = runCLI(t, "find", "/alpha/tasks", "--fields", "note")
	mustOK(t, env, code, "find", "/alpha/tasks", "--fields", "note")
	idAt := bytes.Index(env.Data, []byte(`"id":{"kind"`))
	noteAt := bytes.Index(env.Data, []byte(`"note":{"kind"`))
	if idAt < 0 || noteAt < 0 || idAt > noteAt {
		t.Errorf("projected fields = %s; want id before note in catalog order", env.Data)
	}
	if bytes.Contains(env.Data, []byte(`"title":{"kind"`)) {
		t.Errorf("projected fields = %s; want no unrequested title column", env.Data)
	}
}

// TestRef01FieldsValidation requires E_USAGE for empty, repeated and unknown
// column lists, on both commands.
func TestRef01FieldsValidation(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "find", "/alpha/tasks", "--fields", "bogus")
	mustCode(t, env, code, CodeUsage, "find", "--fields bogus")
	if !strings.Contains(env.Error.Message, `unknown column "bogus"`) {
		t.Errorf("message = %q, want it to name the unknown column", env.Error.Message)
	}

	for _, args := range [][]string{
		{"find", "/alpha/tasks", "--fields", ""},
		{"find", "/alpha/tasks", "--fields", "id,id"},
		{"cat", "/alpha/tasks/1", "--fields", "bogus"},
		{"cat", "/alpha/tasks/1", "--fields", "id,id"},
		{"cat", "/alpha/tasks/1/title", "--fields", "title"},
	} {
		env, code = runCLI(t, args...)
		mustCode(t, env, code, CodeUsage, args...)
	}
}

// TestRef01FieldsKeepTablePolicy proves projecting never widens access: a
// denied table answers with the same error with and without --fields.
func TestRef01FieldsKeepTablePolicy(t *testing.T) {
	setupAlpha(t)
	dir, err := state.Dir()
	if err != nil {
		t.Fatalf("state.Dir: %v", err)
	}
	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = []state.Policy{{Ops: []string{"cat"}, Prefix: "/alpha/tasks/1/title"}}
		cfg.Mounts["alpha"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}

	plain, plainCode := runCLI(t, "find", "/alpha/tasks")
	projected, projectedCode := runCLI(t, "find", "/alpha/tasks", "--fields", "title")
	mustCode(t, plain, plainCode, CodeDenied, "find on a denied table")
	mustCode(t, projected, projectedCode, CodeDenied, "find --fields on a denied table")
	if plain.Error.Message != projected.Error.Message {
		t.Errorf("denial with --fields = %q, without = %q; want the same error",
			projected.Error.Message, plain.Error.Message)
	}

	plain, plainCode = runCLI(t, "cat", "/alpha/tasks/1")
	projected, projectedCode = runCLI(t, "cat", "/alpha/tasks/1", "--fields", "title")
	mustCode(t, plain, plainCode, CodeDenied, "cat of a denied row")
	mustCode(t, projected, projectedCode, CodeDenied, "cat --fields of a denied row")
	if plain.Error.Message != projected.Error.Message {
		t.Errorf("cat denial with --fields = %q, without = %q; want the same error",
			projected.Error.Message, plain.Error.Message)
	}
}

// TestRef01CursorBindsProjection proves a cursor only continues the projection
// that minted it.
func TestRef01CursorBindsProjection(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "find", "/alpha/tasks", "--limit", "1", "--fields", "id,title")
	page := decode[findJSON](t, mustOK(t, env, code, "find --fields with a page"))
	if !env.Meta.Truncated || env.Meta.NextCursor == "" {
		t.Fatalf("first page = %+v, meta %+v; want a cursor", page, env.Meta)
	}
	token := env.Meta.NextCursor

	env, code = runCLI(t, "find", "/alpha/tasks", "--limit", "1", "--fields", "id,title", "--cursor", token)
	page = decode[findJSON](t, mustOK(t, env, code, "find --fields with the same cursor"))
	if len(page.Rows) != 1 || page.Rows[0].Path != "/alpha/tasks/2" {
		t.Errorf("second page = %+v, want row 2", page)
	}

	for _, args := range [][]string{
		{"find", "/alpha/tasks", "--limit", "1", "--fields", "title", "--cursor", token},
		{"find", "/alpha/tasks", "--limit", "1", "--cursor", token},
	} {
		env, code = runCLI(t, args...)
		mustCode(t, env, code, CodeUsage, args...)
	}
}

// TestRef01FieldsWithoutPrimaryKey: a table without a key projects freely and
// still promises no cursor (D-12).
func TestRef01FieldsWithoutPrimaryKey(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "find", "/alpha/nopk", "--fields", "x", "--limit", "1")
	page := decode[findJSON](t, mustOK(t, env, code, "find --fields on a table without a key"))
	if len(page.Rows) != 1 || len(page.Rows[0].Fields) != 1 {
		t.Fatalf("projected page = %+v, want one row with only x", page)
	}
	if _, ok := page.Rows[0].Fields["x"]; !ok {
		t.Errorf("projected row = %+v, want x", page.Rows[0].Fields)
	}
	if !env.Meta.Truncated || env.Meta.NextCursor != "" {
		t.Errorf("meta = %+v, want truncation without a cursor", env.Meta)
	}
}

// TestRef01CatProjection covers cat of a row: the same projection, the same
// ETag and therefore a still-valid --if-match.
func TestRef01CatProjection(t *testing.T) {
	setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())

	full := catRow(t, "/work/notes/1")
	env, code := runCLI(t, "cat", "/work/notes/1", "--fields", "title")
	row := decode[rowJSON](t, mustOK(t, env, code, "cat --fields title"))
	if len(row.Fields) != 2 {
		t.Errorf("cat --fields = %+v, want id and title only", row.Fields)
	}
	if row.Fields["id"] != full.Fields["id"] || row.Fields["title"] != full.Fields["title"] {
		t.Errorf("cat --fields = %+v, want the key and title of %+v", row.Fields, full.Fields)
	}
	if row.ETag != full.ETag {
		t.Errorf("ETag with --fields = %q, without = %q; want the same", row.ETag, full.ETag)
	}

	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"updated"`, "--if-match", row.ETag)
	mustOK(t, env, code, "write with the ETag read through --fields")
}
