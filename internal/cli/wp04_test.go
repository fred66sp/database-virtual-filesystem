package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dbvfs/internal/audit"
	"dbvfs/internal/state"
)

// wp04Schema is the mutation fixture: no triggers on the writable tables, an
// INTEGER and a TEXT primary key, a non-navigable table, a generated column and
// a table whose trigger must make writes E_UNSUPPORTED.
const wp04Schema = `
CREATE TABLE notes (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	body TEXT
);
INSERT INTO notes (id, title, body) VALUES (1, 'first', 'uno'), (2, 'second', 'dos');
CREATE TABLE labels (
	code TEXT NOT NULL PRIMARY KEY,
	label TEXT
);
INSERT INTO labels VALUES ('a', 'alpha');
CREATE TABLE plain (x TEXT);
INSERT INTO plain VALUES ('a');
CREATE TABLE generated (
	id INTEGER PRIMARY KEY,
	n INTEGER,
	twice INTEGER GENERATED ALWAYS AS (n * 2) VIRTUAL
);
INSERT INTO generated (id, n) VALUES (1, 5);
CREATE TABLE guarded (id INTEGER PRIMARY KEY, v TEXT);
CREATE TRIGGER guarded_ins AFTER INSERT ON guarded BEGIN SELECT 1; END;
`

const writeAlias = "work"

type planJSON struct {
	Op      string               `json:"op"`
	Path    string               `json:"path"`
	Table   string               `json:"table"`
	Fields  map[string]valueJSON `json:"fields"`
	IfMatch string               `json:"if_match"`
	DryRun  bool                 `json:"dry_run"`
	Mutates bool                 `json:"mutates"`
	Warning string               `json:"warning"`
}

type auditJSON struct {
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
	Actor     string `json:"actor"`
	Operation string `json:"operation"`
	Alias     string `json:"alias"`
	Table     string `json:"table"`
	Resource  string `json:"resource"`
	Status    string `json:"status"`
	Code      string `json:"code"`
}

// setupWriteFixture mounts a fresh fixture read-only on a RW-capable alias; the
// tests then edit the operator config to grant mode and policy.
func setupWriteFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	db := newFixture(t, wp04Schema)
	if _, code := runCLI(t, "mount", db, "/"+writeAlias); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	return db
}

func fullWritePolicy() []state.Policy {
	return []state.Policy{{
		Ops:    []string{"ls", "cat", "find", "grep", "describe", "stat", "tree", "export", "touch", "write"},
		Prefix: "/" + writeAlias,
	}}
}

func setMount(t *testing.T, mode string, allow []state.Policy) {
	t.Helper()
	dir, err := state.Dir()
	if err != nil {
		t.Fatalf("state.Dir: %v", err)
	}
	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts[writeAlias]
		m.Mode = mode
		m.Allow = allow
		cfg.Mounts[writeAlias] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set mount: %v", err)
	}
}

func catRow(t *testing.T, path string) rowJSON {
	t.Helper()
	env, code := runCLI(t, "cat", path)
	return decode[rowJSON](t, mustOK(t, env, code, "cat", path))
}

func auditLines(t *testing.T) []auditJSON {
	t.Helper()
	dir, err := state.StateDir()
	if err != nil {
		t.Fatalf("state.StateDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var out []auditJSON
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec auditJSON
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("audit line %d is not JSON: %v (%q)", i+1, err, line)
		}
		out = append(out, rec)
	}
	return out
}

func findAudit(t *testing.T, recs []auditJSON, match func(auditJSON) bool) auditJSON {
	t.Helper()
	for _, rec := range recs {
		if match(rec) {
			return rec
		}
	}
	t.Fatalf("no audit record matched; records = %+v", recs)
	return auditJSON{}
}

// TestWP04PolicyDenials is the FR-13 write side: an rw mount without an
// explicit grant, a grant that does not cover the path, a grant that does not
// cover the field, and the read-side table query that would reveal a field.
func TestWP04PolicyDenials(t *testing.T) {
	setupWriteFixture(t)

	// rw without a policy writes nothing (D-17).
	setMount(t, "rw", nil)
	env, code := runCLI(t, "write", "/work/notes/1/title", "--data", `"x"`, "--if-match", "sha256:x")
	mustCode(t, env, code, CodeDenied, "rw without policy")

	// A read-only mount never writes, even with a grant.
	setMount(t, "ro", fullWritePolicy())
	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"x"`, "--if-match", "sha256:x")
	mustCode(t, env, code, CodeDenied, "ro mount with a write grant")

	// The policy covers another row but not the target path.
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"cat"}, Prefix: "/work/notes"},
		{Ops: []string{"write"}, Prefix: "/work/notes/1/title"},
	})
	row2 := catRow(t, "/work/notes/2")
	env, code = runCLI(t, "write", "/work/notes/2/title", "--data", `"hacked"`, "--if-match", row2.ETag)
	mustCode(t, env, code, CodeDenied, "write on an unlisted path")

	// The policy covers one field of the row but not the target field.
	row1 := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "write", "/work/notes/1/body", "--data", `"x"`, "--if-match", row1.ETag)
	mustCode(t, env, code, CodeDenied, "write on an unlisted field")

	// A field-level read grant must not authorize a table query that would
	// reveal the other columns (FR-13).
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"cat", "find"}, Prefix: "/work/notes/1/title"},
	})
	env, code = runCLI(t, "find", "/work/notes")
	mustCode(t, env, code, CodeDenied, "table query with a field-level grant")
	env, code = runCLI(t, "cat", "/work/notes/1/body")
	mustCode(t, env, code, CodeDenied, "cat of a prohibited field")
	env, code = runCLI(t, "cat", "/work/notes/1/title")
	if got := decode[valueJSON](t, mustOK(t, env, code, "cat of the granted field")); got.Data != "first" {
		t.Errorf("cat of the granted field = %+v, want first", got)
	}
}

func TestWP04ETagConflictLeavesRowUntouched(t *testing.T) {
	setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())

	before := catRow(t, "/work/notes/1")
	env, code := runCLI(t, "write", "/work/notes/1/title", "--data", `"updated"`, "--if-match", before.ETag)
	updated := decode[rowJSON](t, mustOK(t, env, code, "write with the current ETag"))
	if got := updated.Fields["title"]; got != (valueJSON{Kind: "text", Data: "updated"}) {
		t.Errorf("updated title = %+v, want updated", got)
	}
	if updated.ETag == before.ETag {
		t.Error("ETag did not change after the write")
	}
	if updated.Fields["id"] != (valueJSON{Kind: "integer", Data: "1"}) {
		t.Errorf("write lost the key: %+v", updated.Fields)
	}

	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"late"`, "--if-match", before.ETag)
	mustCode(t, env, code, CodeConflict, "write with a stale ETag")

	after := catRow(t, "/work/notes/1")
	if got := after.Fields["title"]; got != (valueJSON{Kind: "text", Data: "updated"}) {
		t.Errorf("row after the conflict = %+v, want the earlier update preserved", got)
	}
	if after.ETag != updated.ETag {
		t.Errorf("row ETag changed after a failed write: %q -> %q", updated.ETag, after.ETag)
	}
}

// TestWP04AuditRedaction is the FR-14/D-9 criterion: a denied and an authorized
// operation leave records with request_id, actor, operation, alias and a
// resource that keeps the full key only when the actor may read the row; no
// data value appears anywhere.
func TestWP04AuditRedaction(t *testing.T) {
	setupWriteFixture(t)

	// The actor holds a write grant on one field but no cat grant on the row,
	// so the ETag precondition cannot be met and the PK must be redacted.
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"write"}, Prefix: "/work/notes/1/title"},
	})
	env, code := runCLI(t, "write", "/work/notes/1/title", "--data", `"denied-value"`,
		"--if-match", "sha256:unknown", "--request-id", "req-deny", "--actor", "bob")
	mustCode(t, env, code, CodeDenied, "denied write for the audit log")

	setMount(t, "rw", fullWritePolicy())
	row := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"sentinel-value"`,
		"--if-match", row.ETag, "--request-id", "req-ok", "--actor", "alice")
	mustOK(t, env, code, "authorized write for the audit log")

	records := auditLines(t)
	denied := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "req-deny" })
	if denied.Operation != "write" || denied.Alias != writeAlias || denied.Actor != "bob" {
		t.Errorf("denied record = %+v", denied)
	}
	if denied.Status != "denied" || denied.Code != "E_DENIED" {
		t.Errorf("denied status/code = %s/%s, want denied/E_DENIED", denied.Status, denied.Code)
	}
	if denied.Table != "notes" || !strings.HasPrefix(denied.Resource, "sha256:") {
		t.Errorf("denied resource = %q table %q, want a truncated hash plus the table", denied.Resource, denied.Table)
	}
	if denied.Timestamp == "" {
		t.Error("audit record has no timestamp")
	}

	ok := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "req-ok" && r.Status == "ok" })
	if ok.Actor != "alice" || ok.Code != "OK" || ok.Resource != "/work/notes/1/title" {
		t.Errorf("authorized record = %+v, want alice/OK and the full path", ok)
	}
	if ok.Table != "" {
		t.Errorf("authorized record still names a redacted table: %q", ok.Table)
	}

	dir, err := state.StateDir()
	if err != nil {
		t.Fatalf("state.StateDir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	for _, value := range []string{"denied-value", "sentinel-value"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Errorf("audit log contains the data value %q", value)
		}
	}
	if bytes.Contains(raw, []byte(`"fields"`)) {
		t.Error("audit log contains a fields payload")
	}
}

// TestWP04DryRunDoesNotModify is the FR-10 criterion: the plan is emitted and
// the database bytes are identical before and after both dry-runs.
func TestWP04DryRunDoesNotModify(t *testing.T) {
	db := setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())

	before := sha256File(t, db)
	env, code := runCLI(t, "touch", "/work/notes", "--data", `{"title":"ghost"}`, "--dry-run")
	touchPlan := decode[planJSON](t, mustOK(t, env, code, "touch --dry-run"))
	if touchPlan.Op != "touch" || touchPlan.Path != "/work/notes" || !touchPlan.DryRun || touchPlan.Mutates {
		t.Errorf("touch plan = %+v", touchPlan)
	}
	if touchPlan.Warning == "" {
		t.Error("touch plan has no warning that it does not guarantee success")
	}

	row := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"ghost"`, "--if-match", row.ETag, "--dry-run")
	writePlan := decode[planJSON](t, mustOK(t, env, code, "write --dry-run"))
	if writePlan.Op != "write" || writePlan.Path != "/work/notes/1/title" || !writePlan.DryRun || writePlan.Mutates {
		t.Errorf("write plan = %+v", writePlan)
	}
	if writePlan.IfMatch != row.ETag || writePlan.Warning == "" {
		t.Errorf("write plan precondition/warning = %q/%q", writePlan.IfMatch, writePlan.Warning)
	}

	if after := sha256File(t, db); after != before {
		t.Errorf("dry-run changed the database bytes: %s -> %s", before, after)
	}
	if got := catRow(t, "/work/notes/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "first"}) {
		t.Errorf("dry-run changed the row: %+v", got)
	}
	if _, code := runCLI(t, "ls", "/work/notes"); code != 0 {
		t.Fatalf("ls after dry-run exit = %d", code)
	}

	// The plan consults the ETag precondition, so a stale one is a conflict
	// even without writing.
	env, code = runCLI(t, "write", "/work/notes/1/title", "--data", `"ghost"`, "--if-match", "sha256:stale", "--dry-run")
	mustCode(t, env, code, CodeConflict, "write --dry-run with a stale ETag")
}

func TestWP04TouchAndWriteValidation(t *testing.T) {
	setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())

	env, code := runCLI(t, "touch", "/work/notes", "--data", `{"title":"third"}`)
	inserted := decode[rowJSON](t, mustOK(t, env, code, "touch"))
	if inserted.Path != "/work/notes/3" {
		t.Errorf("touch path = %q, want /work/notes/3", inserted.Path)
	}
	if inserted.Fields["id"] != (valueJSON{Kind: "integer", Data: "3"}) ||
		inserted.Fields["title"] != (valueJSON{Kind: "text", Data: "third"}) {
		t.Errorf("touch row = %+v", inserted.Fields)
	}
	if len(inserted.ETag) != len("sha256:")+64 {
		t.Errorf("touch ETag = %q", inserted.ETag)
	}

	env, code = runCLI(t, "touch", "/work/notes", "--data", `{"id":10,"title":"ten"}`)
	if got := decode[rowJSON](t, mustOK(t, env, code, "touch with an explicit key")).Path; got != "/work/notes/10" {
		t.Errorf("touch explicit key path = %q, want /work/notes/10", got)
	}

	env, code = runCLI(t, "touch", "/work/labels", "--data", `{"code":"b","label":"beta"}`)
	if got := decode[rowJSON](t, mustOK(t, env, code, "touch a TEXT key")).Path; got != "/work/labels/b" {
		t.Errorf("touch text key path = %q, want /work/labels/b", got)
	}

	// stdin is read once, as DESIGN §3 requires.
	var stdout, stderr bytes.Buffer
	code = run([]string{"touch", "/work/notes", "--data", "-"}, strings.NewReader(`{"title":"stdin"}`), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("touch --data - exit = %d (stderr %q)", code, stderr.String())
	}
	var envStdin envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &envStdin); err != nil {
		t.Fatalf("touch --data - stdout: %v", err)
	}
	if !envStdin.OK {
		t.Errorf("touch --data - envelope = %+v", envStdin)
	}

	for _, tc := range []struct {
		name string
		args []string
		want Code
	}{
		{"touch on a row path", []string{"touch", "/work/notes/1", "--data", `{"title":"x"}`}, CodeUnsupported},
		{"touch a non-navigable table", []string{"touch", "/work/plain", "--data", `{"x":"b"}`}, CodeUnsupported},
		{"touch a generated column", []string{"touch", "/work/generated", "--data", `{"n":1,"twice":2}`}, CodeUnsupported},
		{"touch a table with triggers", []string{"touch", "/work/guarded", "--data", `{"v":"x"}`}, CodeUnsupported},
		{"touch violating NOT NULL", []string{"touch", "/work/notes", "--data", `{"body":"x"}`}, CodeConstraint},
		{"duplicate JSON keys", []string{"touch", "/work/notes", "--data", `{"title":"a","title":"b"}`}, CodeUsage},
		{"malformed JSON", []string{"touch", "/work/notes", "--data", `{`}, CodeUsage},
		{"write without a precondition", []string{"write", "/work/notes/1", "--data", `{"title":"x"}`}, CodeUsage},
	} {
		env, code := runCLI(t, tc.args...)
		mustCode(t, env, code, tc.want, tc.args...)
	}

	row := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "write", "/work/notes/1", "--data", `{"id":9}`, "--if-match", row.ETag)
	mustCode(t, env, code, CodeUnsupported, "write of a primary key")

	env, code = runCLI(t, "describe", "/work/notes")
	if got := decode[describeJSON](t, mustOK(t, env, code, "describe notes")); !got.Capabilities.Writable {
		t.Errorf("describe notes writable = false, want true under the full policy")
	}
	env, code = runCLI(t, "describe", "/work/guarded")
	if got := decode[describeJSON](t, mustOK(t, env, code, "describe guarded")); got.Capabilities.Writable {
		t.Errorf("describe guarded writable = true, want false because of its trigger")
	}
}

// TestWP04BinaryAcceptance runs the tk-11 criterion against the built binary:
// policy denials (path and field), a redacted audit log, a stale ETag conflict
// and dry-runs that leave the file byte-identical.
func TestWP04BinaryAcceptance(t *testing.T) {
	bin := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	db := newFixture(t, wp04Schema)
	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir, "XDG_STATE_HOME="+stateDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_STATE_HOME", stateDir)

	runBinary := func(args ...string) (envelopeJSON, int) {
		t.Helper()
		return runBinaryWith(t, bin, env, args...)
	}

	envJSON, code := runBinary("mount", db, "/"+writeAlias)
	mustOK(t, envJSON, code, "mount")

	// Denied by path: read the row's ETag first, then take the cat grant away
	// so the same denied write also exercises the redacted audit resource.
	setMount(t, "rw", []state.Policy{{Ops: []string{"cat"}, Prefix: "/work/notes"}})
	envJSON, code = runBinary("cat", "/work/notes/2")
	row2 := decode[rowJSON](t, mustOK(t, envJSON, code, "cat row 2"))
	setMount(t, "rw", []state.Policy{{Ops: []string{"write"}, Prefix: "/work/notes/1/title"}})
	envJSON, code = runBinary("write", "/work/notes/2/title", "--data", `"hacked"`, "--if-match", row2.ETag,
		"--request-id", "bin-deny", "--actor", "bob")
	mustCode(t, envJSON, code, CodeDenied, "binary write denied by path")

	// Denied by field: the row is readable, the target field is not writable.
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"cat"}, Prefix: "/work/notes"},
		{Ops: []string{"write"}, Prefix: "/work/notes/1/title"},
	})
	envJSON, code = runBinary("cat", "/work/notes/1")
	row1 := decode[rowJSON](t, mustOK(t, envJSON, code, "cat row 1"))
	envJSON, code = runBinary("write", "/work/notes/1/body", "--data", `"x"`, "--if-match", row1.ETag)
	mustCode(t, envJSON, code, CodeDenied, "binary write denied by field")

	// A table query that would reveal a prohibited field is denied too.
	setMount(t, "rw", []state.Policy{{Ops: []string{"cat", "find"}, Prefix: "/work/notes/1/title"}})
	envJSON, code = runBinary("find", "/work/notes")
	mustCode(t, envJSON, code, CodeDenied, "binary field bypass")

	// Authorized write, then the same stale ETag.
	setMount(t, "rw", fullWritePolicy())
	envJSON, code = runBinary("write", "/work/notes/1/title", "--data", `"binary-sentinel"`,
		"--if-match", row1.ETag, "--request-id", "bin-ok", "--actor", "alice")
	written := decode[rowJSON](t, mustOK(t, envJSON, code, "binary write"))
	envJSON, code = runBinary("write", "/work/notes/1/title", "--data", `"late"`, "--if-match", row1.ETag)
	mustCode(t, envJSON, code, CodeConflict, "binary stale ETag")
	envJSON, code = runBinary("cat", "/work/notes/1")
	afterConflict := decode[rowJSON](t, mustOK(t, envJSON, code, "cat after conflict"))
	if got := afterConflict.Fields["title"]; got != (valueJSON{Kind: "text", Data: "binary-sentinel"}) {
		t.Errorf("row changed after the stale ETag: %+v", got)
	}

	// Dry-runs leave the file byte-identical and emit the plan.
	before := sha256File(t, db)
	envJSON, code = runBinary("touch", "/work/notes", "--data", `{"title":"ghost"}`, "--dry-run")
	touchPlan := decode[planJSON](t, mustOK(t, envJSON, code, "binary touch --dry-run"))
	if !touchPlan.DryRun || touchPlan.Mutates || touchPlan.Warning == "" {
		t.Errorf("binary touch plan = %+v", touchPlan)
	}
	envJSON, code = runBinary("write", "/work/notes/1/title", "--data", `"ghost"`, "--if-match", written.ETag, "--dry-run")
	writePlan := decode[planJSON](t, mustOK(t, envJSON, code, "binary write --dry-run"))
	if !writePlan.DryRun || writePlan.Mutates || writePlan.Warning == "" {
		t.Errorf("binary write plan = %+v", writePlan)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("dry-run changed the database: %s -> %s", before, after)
	}

	// Audit: the denied record is hashed, the authorized one keeps the path,
	// and no data value appears.
	records := auditLines(t)
	denied := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "bin-deny" })
	if denied.Code != "E_DENIED" || denied.Actor != "bob" || denied.Table != "notes" ||
		!strings.HasPrefix(denied.Resource, "sha256:") {
		t.Errorf("binary denied audit = %+v", denied)
	}
	ok := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "bin-ok" && r.Status == "ok" })
	if ok.Resource != "/work/notes/1/title" || ok.Actor != "alice" || ok.Code != "OK" {
		t.Errorf("binary authorized audit = %+v", ok)
	}
	dir, err := state.StateDir()
	if err != nil {
		t.Fatalf("state.StateDir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	for _, value := range []string{"binary-sentinel", "hacked"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Errorf("audit log contains the data value %q", value)
		}
	}
}

// TestWP04DeniedGolden fixes the exact E_DENIED envelope for a path, a field
// and a read that would reveal a prohibited field.
func TestWP04DeniedGolden(t *testing.T) {
	setupWriteFixture(t)
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"cat", "find"}, Prefix: "/work/notes"},
		{Ops: []string{"write"}, Prefix: "/work/notes/1/title"},
	})

	tests := []struct {
		name string
		args []string
		file string
	}{
		{
			name: "write denied by path",
			args: []string{"write", "/work/notes/2/title", "--data", `"x"`, "--if-match", "sha256:old"},
			file: "wp04_denied_path.golden",
		},
		{
			name: "write denied by field",
			args: []string{"write", "/work/notes/1/body", "--data", `"x"`, "--if-match", "sha256:old"},
			file: "wp04_denied_field.golden",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tc.args, &stdout, &stderr)
			if code != CodeDenied.Exit() {
				t.Fatalf("Run(%q) exit = %d, want %d", tc.args, code, CodeDenied.Exit())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			checkGolden(t, tc.file, bytes.TrimSpace(stdout.Bytes()))
		})
	}

	setMount(t, "rw", []state.Policy{{Ops: []string{"cat", "find"}, Prefix: "/work/notes/1/title"}})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"cat", "/work/notes/1/body"}, &stdout, &stderr); code != CodeDenied.Exit() {
		t.Fatalf("cat denied exit = %d, want %d", code, CodeDenied.Exit())
	}
	checkGolden(t, "wp04_denied_prohibited_field.golden", bytes.TrimSpace(stdout.Bytes()))
}

// TestWP04AbortsWhenAuditAttemptFails proves the durable pre-mutation record is
// a gate, not a courtesy: if the state directory cannot be written, the row is
// left untouched.
func TestWP04AbortsWhenAuditAttemptFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not bind root")
	}
	db := setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())

	readOnly := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(readOnly, 0o700); err != nil {
		t.Fatalf("create read-only root: %v", err)
	}
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatalf("make the state root read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	t.Setenv("XDG_STATE_HOME", readOnly)

	before := sha256File(t, db)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"write", "/work/notes/1/title", "--data", `"x"`, "--if-match", "sha256:whatever"}, &stdout, &stderr)
	var env envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if code == 0 || env.OK || env.Error == nil || env.Error.Code != string(CodeInternal) {
		t.Fatalf("write with an unwritable state directory = exit %d, %+v; want E_INTERNAL", code, env)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("the database changed although the audit attempt failed: %s -> %s", before, after)
	}
	if got := catRow(t, "/work/notes/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "first"}) {
		t.Errorf("row changed although the audit attempt failed: %+v", got)
	}
}

// TestWP04RollbackOnConstraint proves a failed constraint leaves no partial row
// and reports E_CONSTRAINT (exit 6).
func TestWP04RollbackOnConstraint(t *testing.T) {
	db := setupWriteFixture(t)
	setMount(t, "rw", fullWritePolicy())
	before := sha256File(t, db)

	env, code := runCLI(t, "touch", "/work/notes", "--data", `{"id":3,"title":"ok"}`)
	mustOK(t, env, code, "first insert")
	failed := sha256File(t, db)

	env, code = runCLI(t, "touch", "/work/notes", "--data", `{"id":4,"body":"no title"}`)
	mustCode(t, env, code, CodeConstraint, "NOT NULL violation")
	if after := sha256File(t, db); after != failed {
		t.Errorf("a failed constraint changed the database: %s -> %s", failed, after)
	}
	if before == failed {
		t.Error("the successful insert did not change the database")
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("stat database: %v", err)
	}
}

// wp04CascadeSchema is the DESIGN §8 follow-up fixture: a parent referenced
// with CASCADE and a sibling referencing with RESTRICT, so the denied and the
// allowed side of the same rule are observable.
const wp04CascadeSchema = `
CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO parents (id, name) VALUES (1, 'one');
CREATE TABLE children (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO children (id, parent_id) VALUES (1, 1);
CREATE TABLE pinned (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE RESTRICT ON UPDATE RESTRICT
);
INSERT INTO pinned (id, parent_id) VALUES (1, 1);
`

// TestWP04BinaryRejectsCascadingForeignKeys is the tk-11 review follow-up: the
// binary refuses touch/write where an FK action would reach rows outside the
// plan, and the file stays byte-identical. Updating the referenced parent is the
// bypass under test, because ON UPDATE CASCADE would rewrite the child.
func TestWP04BinaryRejectsCascadingForeignKeys(t *testing.T) {
	bin := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	db := newFixture(t, wp04CascadeSchema)
	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir, "XDG_STATE_HOME="+stateDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_STATE_HOME", stateDir)

	envJSON, code := runBinaryWith(t, bin, env, "mount", db, "/"+writeAlias)
	mustOK(t, envJSON, code, "mount")
	setMount(t, "rw", fullWritePolicy())

	envJSON, code = runBinaryWith(t, bin, env, "cat", "/work/children/1")
	child := decode[rowJSON](t, mustOK(t, envJSON, code, "cat cascading child"))
	envJSON, code = runBinaryWith(t, bin, env, "cat", "/work/parents/1")
	parent := decode[rowJSON](t, mustOK(t, envJSON, code, "cat parent"))

	before := sha256File(t, db)
	envJSON, code = runBinaryWith(t, bin, env, "touch", "/work/children", "--data", `{"parent_id":1}`)
	mustCode(t, envJSON, code, CodeUnsupported, "touch a cascading child")
	envJSON, code = runBinaryWith(t, bin, env, "write", "/work/children/1/parent_id", "--data", `2`, "--if-match", child.ETag)
	mustCode(t, envJSON, code, CodeUnsupported, "write a cascading child")
	envJSON, code = runBinaryWith(t, bin, env, "write", "/work/parents/1/name", "--data", `"two"`, "--if-match", parent.ETag)
	mustCode(t, envJSON, code, CodeUnsupported, "write a parent referenced with cascade")
	if after := sha256File(t, db); after != before {
		t.Errorf("a denied cascade changed the database: %s -> %s", before, after)
	}

	envJSON, code = runBinaryWith(t, bin, env, "touch", "/work/pinned", "--data", `{"parent_id":1}`)
	mustOK(t, envJSON, code, "touch a restrict child")
	if after := sha256File(t, db); after == before {
		t.Error("the allowed insert did not change the database")
	}
}

// runBinaryWith executes one binary invocation, requiring the contract's single
// JSON envelope on stdout and an empty stderr.
func runBinaryWith(t *testing.T, bin string, env []string, args ...string) (envelopeJSON, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run %q: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	if stderr.Len() != 0 {
		t.Errorf("binary %q stderr = %q, want empty", args, stderr.String())
	}
	var out envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); err != nil {
		t.Fatalf("binary %q stdout is not JSON: %v (%q)", args, err, stdout.String())
	}
	return out, code
}
