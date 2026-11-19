package cli

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dbvfs/internal/audit"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
)

// wp05Schema is the adversarial fixture for Gate M2: UNIQUE, NOT NULL, CHECK
// and generated columns, a composite key, a trigger, incoming references with
// and without cascading actions and an outgoing cascading foreign key.
const wp05Schema = `
CREATE TABLE notes (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL UNIQUE,
	body TEXT,
	twice TEXT GENERATED ALWAYS AS (upper(title)) VIRTUAL
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
INSERT INTO guarded VALUES (1, 'x');
CREATE TRIGGER guarded_ins AFTER INSERT ON guarded BEGIN SELECT 1; END;
CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO parents VALUES (1, 'one');
CREATE TABLE cascading_children (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE ON UPDATE CASCADE
);
INSERT INTO cascading_children VALUES (1, 1);
CREATE TABLE pinned (
	id INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES parents(id) ON DELETE RESTRICT ON UPDATE RESTRICT
);
INSERT INTO pinned VALUES (1, 1);
CREATE TABLE pairs (a TEXT NOT NULL, b INTEGER NOT NULL, v TEXT, PRIMARY KEY (a, b));
INSERT INTO pairs VALUES ('a', 1, 'x');
CREATE TABLE measured (id INTEGER PRIMARY KEY CHECK (id > 0), n INTEGER NOT NULL);
INSERT INTO measured VALUES (1, 5);
`

// wp05Plan is the dry-run shape cp, mv and rm emit, including the destination.
type wp05Plan struct {
	Op          string               `json:"op"`
	Path        string               `json:"path"`
	Table       string               `json:"table"`
	Destination string               `json:"destination"`
	Fields      map[string]valueJSON `json:"fields"`
	IfMatch     string               `json:"if_match"`
	DryRun      bool                 `json:"dry_run"`
	Mutates     bool                 `json:"mutates"`
	Warning     string               `json:"warning"`
}

func fullMutationPolicy() []state.Policy {
	return []state.Policy{{
		Ops: []string{
			"ls", "cat", "find", "grep", "describe", "stat", "tree", "export",
			"touch", "write", "cp", "mv", "rm",
		},
		Prefix: "/" + writeAlias,
	}}
}

func setupWP05Fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	db := newFixture(t, wp05Schema)
	if _, code := runCLI(t, "mount", db, "/"+writeAlias); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	setMount(t, "rw", fullMutationPolicy())
	return db
}

func TestWP05Copy(t *testing.T) {
	db := setupWP05Fixture(t)

	// A plain copy: ordinary columns copied, destination key explicit.
	label := catRow(t, "/work/labels/a")
	env, code := runCLI(t, "cp", "/work/labels/a", "/work/labels/b", "--if-match", label.ETag)
	copied := decode[rowJSON](t, mustOK(t, env, code, "cp labels a to b"))
	if copied.Path != "/work/labels/b" {
		t.Errorf("cp path = %q, want /work/labels/b", copied.Path)
	}
	if got := copied.Fields["label"]; got != (valueJSON{Kind: "text", Data: "alpha"}) {
		t.Errorf("copied label = %+v, want alpha", got)
	}
	if copied.ETag == label.ETag {
		t.Error("the copy kept the source ETag")
	}

	// Generated columns are recomputed, never copied.
	env, code = runCLI(t, "cat", "/work/generated/1")
	generated := decode[rowJSON](t, mustOK(t, env, code, "cat generated"))
	env, code = runCLI(t, "cp", "/work/generated/1", "/work/generated/2", "--if-match", generated.ETag)
	copied = decode[rowJSON](t, mustOK(t, env, code, "cp a generated column table"))
	if got := copied.Fields["twice"]; got != (valueJSON{Kind: "integer", Data: "10"}) {
		t.Errorf("copied generated column = %+v, want the engine result 10", got)
	}

	// A CHECK constraint that a destination key violates still speaks.
	measured := catRow(t, "/work/measured/1")
	env, code = runCLI(t, "cp", "/work/measured/1", "/work/measured/2", "--if-match", measured.ETag)
	mustOK(t, env, code, "cp of a CHECK-constrained table")
	env, code = runCLI(t, "cp", "/work/measured/1", "/work/measured/0", "--if-match", measured.ETag)
	mustCode(t, env, code, CodeConstraint, "cp violating CHECK")

	// A relative destination resolves against the session cwd, not the process
	// cwd, and the new row lands on its absolute path.
	env, code = runCLI(t, "cd", "/work", "--session", "relcp")
	mustOK(t, env, code, "cd /work for a relative destination")
	env, code = runCLI(t, "cp", "/work/labels/a", "labels/relative", "--if-match", label.ETag, "--session", "relcp")
	relative := decode[rowJSON](t, mustOK(t, env, code, "cp to a relative destination"))
	if relative.Path != "/work/labels/relative" {
		t.Errorf("relative cp path = %q, want /work/labels/relative", relative.Path)
	}
	if got := catRow(t, "/work/labels/relative").Fields["label"]; got != (valueJSON{Kind: "text", Data: "alpha"}) {
		t.Errorf("relative cp label = %+v, want alpha", got)
	}

	before := sha256File(t, db)
	failures := []struct {
		name string
		args []string
		want Code
	}{
		{"stale ETag", []string{"cp", "/work/labels/a", "/work/labels/c", "--if-match", "sha256:stale"}, CodeConflict},
		{"destination exists", []string{"cp", "/work/labels/a", "/work/labels/b", "--if-match", label.ETag}, CodeConflict},
		{"UNIQUE violation", []string{"cp", "/work/notes/1", "/work/notes/3", "--if-match", catRow(t, "/work/notes/1").ETag}, CodeConstraint},
		{"non-navigable table", []string{"cp", "/work/plain/1", "/work/plain/2", "--if-match", "sha256:x"}, CodeUnsupported},
		{"triggered table", []string{"cp", "/work/guarded/1", "/work/guarded/2", "--if-match", "sha256:x"}, CodeUnsupported},
		{"outgoing cascade", []string{"cp", "/work/cascading_children/1", "/work/cascading_children/2", "--if-match", "sha256:x"}, CodeUnsupported},
		{"cross-table copy", []string{"cp", "/work/labels/a", "/work/notes/9", "--if-match", label.ETag}, CodeUnsupported},
		{"apply is not a cp flag", []string{"cp", "/work/labels/a", "/work/labels/d", "--if-match", label.ETag, "--apply"}, CodeUsage},
		{"missing ETag", []string{"cp", "/work/labels/a", "/work/labels/d"}, CodeUsage},
		{"missing row", []string{"cp", "/work/notes/999", "/work/notes/3", "--if-match", "sha256:x"}, CodeNotFound},
	}
	for _, tc := range failures {
		env, code := runCLI(t, tc.args...)
		mustCode(t, env, code, tc.want, tc.args...)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("a failed cp changed the database: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/labels/c"); code != CodeNotFound.Exit() {
		t.Errorf("cat of the failed destination c = exit %d, want not found", code)
	}

	// cp --dry-run plans and leaves the file byte-identical.
	before = sha256File(t, db)
	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/z", "--if-match", label.ETag, "--dry-run")
	plan := decode[wp05Plan](t, mustOK(t, env, code, "cp --dry-run"))
	if plan.Op != "cp" || plan.Path != "/work/labels/a" || plan.Destination != "/work/labels/z" {
		t.Errorf("cp plan = %+v", plan)
	}
	if !plan.DryRun || plan.Mutates || plan.Warning == "" || plan.IfMatch != label.ETag {
		t.Errorf("cp plan flags = %+v, want dry-run, no mutation and a warning", plan)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("cp --dry-run changed the database: %s -> %s", before, after)
	}

	// The plan consults the ETag and the destination, so it fails like the
	// execution would.
	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/z", "--if-match", "sha256:stale", "--dry-run")
	mustCode(t, env, code, CodeConflict, "cp --dry-run with a stale ETag")
	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/b", "--if-match", label.ETag, "--dry-run")
	mustCode(t, env, code, CodeConflict, "cp --dry-run onto an existing destination")
}

func TestWP05Move(t *testing.T) {
	db := setupWP05Fixture(t)

	label := catRow(t, "/work/labels/a")
	env, code := runCLI(t, "mv", "/work/labels/a", "/work/labels/b", "--if-match", label.ETag, "--apply")
	moved := decode[rowJSON](t, mustOK(t, env, code, "mv labels a to b"))
	if moved.Path != "/work/labels/b" {
		t.Errorf("mv path = %q, want /work/labels/b", moved.Path)
	}
	if _, code := runCLI(t, "cat", "/work/labels/a"); code != CodeNotFound.Exit() {
		t.Errorf("the source key still resolves after mv: exit %d", code)
	}
	if got := catRow(t, "/work/labels/b").Fields["label"]; got != (valueJSON{Kind: "text", Data: "alpha"}) {
		t.Errorf("moved label = %+v, want alpha", got)
	}

	// A rename of an INTEGER key works too.
	note := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "mv", "/work/notes/1", "/work/notes/10", "--if-match", note.ETag, "--apply")
	if got := decode[rowJSON](t, mustOK(t, env, code, "mv notes 1 to 10")).Path; got != "/work/notes/10" {
		t.Errorf("mv notes path = %q, want /work/notes/10", got)
	}

	before := sha256File(t, db)
	current := catRow(t, "/work/notes/10")
	failures := []struct {
		name string
		args []string
		want Code
	}{
		{"stale ETag", []string{"mv", "/work/notes/10", "/work/notes/11", "--if-match", note.ETag, "--apply"}, CodeConflict},
		{"destination exists", []string{"mv", "/work/notes/10", "/work/notes/2", "--if-match", current.ETag, "--apply"}, CodeConflict},
		{"composite primary key", []string{"mv", "/work/pairs/~k" + compositeKeyToken("a", 1), "/work/pairs/~k" + compositeKeyToken("b", 1), "--if-match", "sha256:x", "--apply"}, CodeUnsupported},
		{"incoming references", []string{"mv", "/work/parents/1", "/work/parents/2", "--if-match", catRow(t, "/work/parents/1").ETag, "--apply"}, CodeUnsupported},
		{"triggered table", []string{"mv", "/work/guarded/1", "/work/guarded/2", "--if-match", "sha256:x", "--apply"}, CodeUnsupported},
		{"cross-table move", []string{"mv", "/work/labels/b", "/work/notes/9", "--if-match", label.ETag, "--apply"}, CodeUnsupported},
		{"missing --apply", []string{"mv", "/work/notes/10", "/work/notes/11", "--if-match", current.ETag}, CodeUsage},
		{"missing ETag", []string{"mv", "/work/notes/10", "/work/notes/11", "--apply"}, CodeUsage},
		{"missing row", []string{"mv", "/work/notes/999", "/work/notes/11", "--if-match", "sha256:x", "--apply"}, CodeNotFound},
	}
	for _, tc := range failures {
		env, code := runCLI(t, tc.args...)
		mustCode(t, env, code, tc.want, tc.args...)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("a failed mv changed the database: %s -> %s", before, after)
	}
	if got := catRow(t, "/work/notes/10").Fields["title"]; got != (valueJSON{Kind: "text", Data: "first"}) {
		t.Errorf("mv failure changed the row: title = %+v", got)
	}

	// --dry-run without --apply plans and does not mutate.
	before = sha256File(t, db)
	env, code = runCLI(t, "mv", "/work/notes/10", "/work/notes/12", "--if-match", current.ETag, "--dry-run")
	plan := decode[wp05Plan](t, mustOK(t, env, code, "mv --dry-run"))
	if plan.Op != "mv" || plan.Path != "/work/notes/10" || plan.Destination != "/work/notes/12" {
		t.Errorf("mv plan = %+v", plan)
	}
	if !plan.DryRun || plan.Mutates || plan.Warning == "" {
		t.Errorf("mv plan flags = %+v, want dry-run, no mutation and a warning", plan)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("mv --dry-run changed the database: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/notes/12"); code != CodeNotFound.Exit() {
		t.Errorf("mv --dry-run created the destination: exit %d", code)
	}
}

func TestWP05Remove(t *testing.T) {
	db := setupWP05Fixture(t)

	// --apply is mandatory unless the invocation is a dry-run.
	note := catRow(t, "/work/notes/2")
	env, code := runCLI(t, "rm", "/work/notes/2", "--if-match", note.ETag)
	mustCode(t, env, code, CodeUsage, "rm without --apply")

	before := sha256File(t, db)
	env, code = runCLI(t, "rm", "/work/notes/2", "--if-match", note.ETag, "--dry-run")
	plan := decode[wp05Plan](t, mustOK(t, env, code, "rm --dry-run"))
	if plan.Op != "rm" || plan.Path != "/work/notes/2" || plan.Mutates || plan.Warning == "" {
		t.Errorf("rm plan = %+v", plan)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("rm --dry-run changed the database: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != 0 {
		t.Errorf("rm --dry-run deleted the row: exit %d", code)
	}

	failures := []struct {
		name string
		args []string
		want Code
	}{
		{"stale ETag", []string{"rm", "/work/notes/2", "--if-match", "sha256:stale", "--apply"}, CodeConflict},
		{"incoming references", []string{"rm", "/work/parents/1", "--if-match", catRow(t, "/work/parents/1").ETag, "--apply"}, CodeUnsupported},
		{"outgoing cascade", []string{"rm", "/work/cascading_children/1", "--if-match", "sha256:x", "--apply"}, CodeUnsupported},
		{"triggered table", []string{"rm", "/work/guarded/1", "--if-match", "sha256:x", "--apply"}, CodeUnsupported},
		{"non-navigable table", []string{"rm", "/work/plain/1", "--if-match", "sha256:x", "--apply"}, CodeUnsupported},
		{"missing row", []string{"rm", "/work/notes/999", "--if-match", "sha256:x", "--apply"}, CodeNotFound},
	}
	for _, tc := range failures {
		env, code := runCLI(t, tc.args...)
		mustCode(t, env, code, tc.want, tc.args...)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("a failed rm changed the database: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != 0 {
		t.Errorf("a failed rm deleted the row: exit %d", code)
	}

	// The successful delete removes exactly one row.
	env, code = runCLI(t, "rm", "/work/notes/2", "--if-match", note.ETag, "--apply")
	removed := decode[rowJSON](t, mustOK(t, env, code, "rm notes 2"))
	if removed.Path != "/work/notes/2" || removed.Fields["title"] != (valueJSON{Kind: "text", Data: "second"}) {
		t.Errorf("rm returned %+v, want the deleted row", removed)
	}
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != CodeNotFound.Exit() {
		t.Errorf("the removed row still resolves: exit %d", code)
	}
	if _, code := runCLI(t, "cat", "/work/notes/1"); code != 0 {
		t.Errorf("rm deleted a sibling row: exit %d", code)
	}
}

// TestWP05AbortsWhenAuditAttemptFails is the §11 rule for rm: the durable
// attempt is a gate, so an unwritable audit log is E_INTERNAL and the row stays.
func TestWP05AbortsWhenAuditAttemptFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not bind root")
	}
	db := setupWP05Fixture(t)
	row := catRow(t, "/work/notes/2")

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
	code := Run([]string{"rm", "/work/notes/2", "--if-match", row.ETag, "--apply"}, &stdout, &stderr)
	var env envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if code != CodeInternal.Exit() || env.OK || env.Error == nil || env.Error.Code != string(CodeInternal) {
		t.Fatalf("rm with an unwritable audit log = exit %d, %+v; want E_INTERNAL", code, env)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("rm changed the database although its audit attempt failed: %s -> %s", before, after)
	}
	_ = os.Chmod(readOnly, 0o700)
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != 0 {
		t.Errorf("rm deleted the row although its audit attempt failed: exit %d", code)
	}
}

// TestWP05Audit is the D-9 criterion for the three operations: every mutation
// leaves attempt then result, the resource is hashed when the actor cannot read
// the row and complete when it can, and no data value reaches the log.
func TestWP05Audit(t *testing.T) {
	setupWP05Fixture(t)

	// Only the cp grant: the actor cannot read the source row, so both the
	// operation and its resource are denied and hashed.
	setMount(t, "rw", []state.Policy{{Ops: []string{"cp"}, Prefix: "/work/labels"}})
	env, code := runCLI(t, "cp", "/work/labels/a", "/work/labels/b", "--if-match", "sha256:unknown",
		"--request-id", "wp05-cp-deny", "--actor", "bob")
	mustCode(t, env, code, CodeDenied, "cp without a cat grant")

	// A cp grant on the destination without a cat grant there keeps the
	// readable source complete and redacts the destination (D-9).
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"cp"}, Prefix: "/work/labels"},
		{Ops: []string{"cat"}, Prefix: "/work/labels/a"},
	})
	source := catRow(t, "/work/labels/a")
	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/redacted", "--if-match", source.ETag,
		"--request-id", "wp05-cp-dest-redacted", "--actor", "bob")
	mustOK(t, env, code, "cp with a write-only destination")

	// mv always holds a write grant on its destination; without a cat grant
	// there, the destination identity is hashed the same way.
	setMount(t, "rw", []state.Policy{
		{Ops: []string{"mv"}, Prefix: "/work/generated"},
		{Ops: []string{"cat"}, Prefix: "/work/generated/1"},
	})
	generated := catRow(t, "/work/generated/1")
	env, code = runCLI(t, "mv", "/work/generated/1", "/work/generated/20", "--if-match", generated.ETag, "--apply",
		"--request-id", "wp05-mv-dest-redacted", "--actor", "bob")
	mustOK(t, env, code, "mv with a write-only destination")

	setMount(t, "rw", fullMutationPolicy())
	copied := catRow(t, "/work/labels/a")
	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/b", "--if-match", copied.ETag,
		"--request-id", "wp05-cp-ok", "--actor", "alice")
	mustOK(t, env, code, "authorized cp")

	note := catRow(t, "/work/notes/1")
	env, code = runCLI(t, "mv", "/work/notes/1", "/work/notes/10", "--if-match", note.ETag, "--apply",
		"--request-id", "wp05-mv-ok", "--actor", "alice")
	mustOK(t, env, code, "authorized mv")

	env, code = runCLI(t, "rm", "/work/notes/2", "--if-match", catRow(t, "/work/notes/2").ETag, "--apply",
		"--request-id", "wp05-rm-ok", "--actor", "alice")
	mustOK(t, env, code, "authorized rm")

	env, code = runCLI(t, "cp", "/work/labels/a", "/work/labels/c", "--if-match", "sha256:stale",
		"--request-id", "wp05-cp-error")
	mustCode(t, env, code, CodeConflict, "cp conflict for the audit log")
	env, code = runCLI(t, "mv", "/work/notes/10", "/work/notes/11", "--if-match", "sha256:stale", "--apply",
		"--request-id", "wp05-mv-error")
	mustCode(t, env, code, CodeConflict, "mv conflict for the audit log")
	env, code = runCLI(t, "rm", "/work/notes/10", "--if-match", "sha256:stale", "--apply",
		"--request-id", "wp05-rm-error")
	mustCode(t, env, code, CodeConflict, "rm conflict for the audit log")

	records := auditLines(t)

	denied := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "wp05-cp-deny" })
	if denied.Operation != "cp" || denied.Status != "denied" || denied.Code != "E_DENIED" ||
		denied.Table != "labels" || !strings.HasPrefix(denied.Resource, "sha256:") {
		t.Errorf("denied cp audit = %+v, want a redacted labels resource", denied)
	}

	redactedDest := findAudit(t, records, func(r auditJSON) bool {
		return r.RequestID == "wp05-cp-dest-redacted" && r.Status == "ok"
	})
	if redactedDest.Resource != "/work/labels/a" {
		t.Errorf("readable cp source = %q, want the full path", redactedDest.Resource)
	}
	if !strings.HasPrefix(redactedDest.Dest, "sha256:") || len(redactedDest.Dest) != len("sha256:")+16 {
		t.Errorf("write-only destination = %q, want sha256:<16 hex>", redactedDest.Dest)
	}
	mvDest := findAudit(t, records, func(r auditJSON) bool {
		return r.RequestID == "wp05-mv-dest-redacted" && r.Status == "ok"
	})
	if mvDest.Resource != "/work/generated/1" {
		t.Errorf("readable mv source = %q, want the full path", mvDest.Resource)
	}
	if !strings.HasPrefix(mvDest.Dest, "sha256:") || len(mvDest.Dest) != len("sha256:")+16 {
		t.Errorf("write-only mv destination = %q, want sha256:<16 hex>", mvDest.Dest)
	}

	for _, tc := range []struct {
		request, op, resource, dest string
	}{
		{"wp05-cp-ok", "cp", "/work/labels/a", "/work/labels/b"},
		{"wp05-mv-ok", "mv", "/work/notes/1", "/work/notes/10"},
		{"wp05-rm-ok", "rm", "/work/notes/2", ""},
	} {
		attempt := findAudit(t, records, func(r auditJSON) bool {
			return r.RequestID == tc.request && r.Status == "attempt" && r.Operation == tc.op
		})
		if attempt.Resource != tc.resource || attempt.Dest != tc.dest {
			t.Errorf("%s attempt = %q/%q, want %q/%q", tc.op, attempt.Resource, attempt.Dest, tc.resource, tc.dest)
		}
		ok := findAudit(t, records, func(r auditJSON) bool {
			return r.RequestID == tc.request && r.Status == "ok"
		})
		if ok.Code != "OK" || ok.Actor != "alice" || ok.Resource != tc.resource || ok.Dest != tc.dest {
			t.Errorf("%s final audit = %+v", tc.op, ok)
		}
	}
	for _, tc := range []struct{ request, op, dest string }{
		{"wp05-cp-error", "cp", "/work/labels/c"},
		{"wp05-mv-error", "mv", "/work/notes/11"},
		{"wp05-rm-error", "rm", ""},
	} {
		attempt := findAudit(t, records, func(r auditJSON) bool {
			return r.RequestID == tc.request && r.Status == "attempt"
		})
		if attempt.Operation != tc.op || attempt.Dest != tc.dest {
			t.Errorf("%s attempt = %q/%q, want %q/%q", tc.request, attempt.Operation, attempt.Dest, tc.op, tc.dest)
		}
		failed := findAudit(t, records, func(r auditJSON) bool {
			return r.RequestID == tc.request && r.Status == "error"
		})
		if failed.Code != "E_CONFLICT" || failed.Dest != tc.dest {
			t.Errorf("%s error = %q/%q, want E_CONFLICT/%q", tc.request, failed.Code, failed.Dest, tc.dest)
		}
	}

	dir, err := state.StateDir()
	if err != nil {
		t.Fatalf("state.StateDir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	for _, value := range []string{"alpha", "first", "second"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Errorf("audit log contains the data value %q", value)
		}
	}
	if bytes.Contains(raw, []byte(`"fields"`)) {
		t.Error("audit log contains a fields payload")
	}
}

// TestWP05CommitUnknown is the Gate M2 criterion for an uncertain commit: a
// competing reader blocks COMMIT, the invocation exits 10 and the envelope asks
// for reconciliation instead of a blind retry.
func TestWP05CommitUnknown(t *testing.T) {
	db := setupWP05Fixture(t)
	label := catRow(t, "/work/labels/a")

	blocker, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	t.Cleanup(func() { blocker.Close() })
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM labels").Scan(&count); err != nil {
		t.Fatalf("hold a shared lock: %v", err)
	}

	env, code := runCLI(t, "cp", "/work/labels/a", "/work/labels/blocked",
		"--if-match", label.ETag, "--timeout", "1", "--request-id", "wp05-unknown")
	mustCode(t, env, code, CodeCommitUnknown, "cp with a blocked commit")
	if env.Error == nil || !strings.Contains(env.Error.Message, "reconcile") {
		t.Errorf("unknown-commit message = %+v, want a reconciliation signal", env.Error)
	}

	record := findAudit(t, auditLines(t), func(r auditJSON) bool {
		return r.RequestID == "wp05-unknown" && r.Status == "unknown"
	})
	if record.Code != "E_COMMIT_UNKNOWN" {
		t.Errorf("unknown-commit audit code = %q, want E_COMMIT_UNKNOWN", record.Code)
	}
	if record.Dest != "/work/labels/blocked" {
		t.Errorf("unknown-commit destination = %q, want /work/labels/blocked", record.Dest)
	}

	// Reconciling by reading settles it: the blocked commit did not persist.
	_ = tx.Rollback()
	if _, code := runCLI(t, "cat", "/work/labels/blocked"); code != CodeNotFound.Exit() {
		t.Errorf("reconciliation read = exit %d, want not found", code)
	}
}

// TestWP05BinaryAcceptance runs the Gate M2 flow against the built binary:
// copy, rename and delete succeed, a stale ETag conflicts, cascades and
// triggers are denied, and dry-runs leave the file byte-identical.
func TestWP05BinaryAcceptance(t *testing.T) {
	bin := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	db := newFixture(t, wp05Schema)
	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir, "XDG_STATE_HOME="+stateDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_STATE_HOME", stateDir)

	runBinary := func(args ...string) (envelopeJSON, int) {
		t.Helper()
		return runBinaryWith(t, bin, env, args...)
	}

	envJSON, code := runBinary("mount", db, "/"+writeAlias)
	mustOK(t, envJSON, code, "mount")
	setMount(t, "rw", fullMutationPolicy())

	envJSON, code = runBinary("cat", "/work/labels/a")
	label := decode[rowJSON](t, mustOK(t, envJSON, code, "cat labels a"))

	envJSON, code = runBinary("cp", "/work/labels/a", "/work/labels/b", "--if-match", label.ETag)
	copied := decode[rowJSON](t, mustOK(t, envJSON, code, "binary cp"))
	if copied.Path != "/work/labels/b" {
		t.Errorf("binary cp path = %q", copied.Path)
	}

	envJSON, code = runBinary("mv", "/work/labels/b", "/work/labels/c", "--if-match", copied.ETag, "--apply")
	moved := decode[rowJSON](t, mustOK(t, envJSON, code, "binary mv"))
	if moved.Path != "/work/labels/c" {
		t.Errorf("binary mv path = %q", moved.Path)
	}

	before := sha256File(t, db)
	envJSON, code = runBinary("cp", "/work/labels/a", "/work/labels/d", "--if-match", "sha256:stale")
	mustCode(t, envJSON, code, CodeConflict, "binary stale cp")
	envJSON, code = runBinary("mv", "/work/parents/1", "/work/parents/2", "--if-match",
		"sha256:x", "--apply")
	mustCode(t, envJSON, code, CodeUnsupported, "binary mv with incoming references")
	envJSON, code = runBinary("rm", "/work/cascading_children/1", "--if-match", "sha256:x", "--apply")
	mustCode(t, envJSON, code, CodeUnsupported, "binary rm with an outgoing cascade")
	envJSON, code = runBinary("rm", "/work/notes/2", "--if-match", "sha256:stale", "--apply")
	mustCode(t, envJSON, code, CodeConflict, "binary stale rm")
	if after := sha256File(t, db); after != before {
		t.Errorf("a denied binary mutation changed the database: %s -> %s", before, after)
	}

	envJSON, code = runBinary("rm", "/work/notes/2", "--if-match", catRow(t, "/work/notes/2").ETag, "--apply")
	mustOK(t, envJSON, code, "binary rm")
	envJSON, code = runBinary("cat", "/work/notes/2")
	mustCode(t, envJSON, code, CodeNotFound, "binary cat after rm")

	before = sha256File(t, db)
	envJSON, code = runBinary("mv", "/work/labels/c", "/work/labels/e", "--if-match", moved.ETag, "--dry-run")
	plan := decode[wp05Plan](t, mustOK(t, envJSON, code, "binary mv --dry-run"))
	if !plan.DryRun || plan.Mutates || plan.Warning == "" || plan.Destination != "/work/labels/e" {
		t.Errorf("binary mv plan = %+v", plan)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("binary dry-run changed the database: %s -> %s", before, after)
	}
}

// compositeKeyToken encodes one composite path key the way the CLI accepts it.
func compositeKeyToken(text string, n int64) string {
	key := storage.Key{
		{Kind: "text", Data: text},
		{Kind: "integer", Data: strconv.FormatInt(n, 10)},
	}
	payload, err := json.Marshal(key)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}
