package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dbvfs/internal/state"
)

// ref04Schema is the upsert fixture: an INTEGER primary key (the conflict
// destination), two independent UNIQUE constraints (one of them incidental to
// the upsert), a CHECK, a table whose only identity is a UNIQUE, a guarded table
// and a generated column.
const ref04Schema = `
CREATE TABLE docs (
	id INTEGER PRIMARY KEY,
	slug TEXT NOT NULL,
	alias TEXT UNIQUE,
	title TEXT NOT NULL,
	body TEXT,
	CONSTRAINT docs_slug UNIQUE (slug),
	CHECK (title <> '')
);
INSERT INTO docs (id, slug, alias, title) VALUES (1, 'a', 'x', 'first');
CREATE TABLE unique_only (code TEXT NOT NULL UNIQUE, note TEXT);
INSERT INTO unique_only VALUES ('u1', 'one');
CREATE TABLE guarded (id INTEGER PRIMARY KEY, v TEXT);
CREATE TRIGGER guarded_ins AFTER INSERT ON guarded BEGIN SELECT 1; END;
CREATE TABLE generated (
	id INTEGER PRIMARY KEY,
	n INTEGER,
	twice INTEGER GENERATED ALWAYS AS (n * 2) VIRTUAL
);
`

type upsertJSON struct {
	Result  string `json:"result"`
	Applied bool   `json:"applied"`
	Path    string `json:"path"`
	ETag    string `json:"etag"`
}

func setupUpsertFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	db := newFixture(t, ref04Schema)
	if _, code := runCLI(t, "mount", db, "/"+writeAlias); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	return db
}

// TestREF04CreateAndUpdate covers the branch semantics: an insert reports
// created, an existing row can only be updated with its current ETag, and no
// invocation silently creates or updates an unrequested row.
func TestREF04CreateAndUpdate(t *testing.T) {
	db := setupUpsertFixture(t)
	setMount(t, "rw", fullWritePolicy())

	env, code := runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"second"}`)
	created := decode[upsertJSON](t, mustOK(t, env, code, "upsert create"))
	if created.Result != "created" || !created.Applied {
		t.Errorf("create result = %+v, want created/applied", created)
	}
	if created.Path != "/work/docs/2" || !strings.HasPrefix(created.ETag, "sha256:") {
		t.Errorf("create path/etag = %q/%q", created.Path, created.ETag)
	}

	// A second call without --if-match must not update anything.
	before := sha256File(t, db)
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"silent"}`)
	mustCode(t, env, code, CodeUsage, "upsert existing without --if-match")
	if after := sha256File(t, db); after != before {
		t.Errorf("rejected unconditional update changed the database: %s -> %s", before, after)
	}

	row := catRow(t, "/work/docs/2")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"changed"}`, "--if-match", row.ETag)
	updated := decode[upsertJSON](t, mustOK(t, env, code, "upsert update"))
	if updated.Result != "updated" || !updated.Applied || updated.ETag == row.ETag {
		t.Errorf("update result = %+v, want updated with a new ETag", updated)
	}

	// A stale ETag is a conflict and writes nothing.
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"late"}`, "--if-match", row.ETag)
	mustCode(t, env, code, CodeConflict, "upsert with a stale ETag")
	if got := catRow(t, "/work/docs/2").Fields["title"]; got != (valueJSON{Kind: "text", Data: "changed"}) {
		t.Errorf("row after a stale ETag = %+v, want changed preserved", got)
	}

	// --if-match on a missing row never recreates it.
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":3,"slug":"c","title":"third"}`, "--if-match", updated.ETag)
	mustCode(t, env, code, CodeUsage, "upsert with --if-match on a missing row")
	env, code = runCLI(t, "cat", "/work/docs/3")
	mustCode(t, env, code, CodeNotFound, "cat after a refused --if-match insert")
}

// TestREF04DryRunDoesNotWrite proves the simulation reports the branch but
// leaves both the file bytes and the rows untouched.
func TestREF04DryRunDoesNotWrite(t *testing.T) {
	db := setupUpsertFixture(t)
	setMount(t, "rw", fullWritePolicy())

	before := sha256File(t, db)
	env, code := runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"ghost"}`, "--dry-run")
	plan := decode[upsertJSON](t, mustOK(t, env, code, "upsert create --dry-run"))
	if plan.Result != "created" || plan.Applied || plan.Path != "/work/docs/2" || plan.ETag != "" {
		t.Errorf("create plan = %+v, want created/applied=false without an ETag", plan)
	}

	row := catRow(t, "/work/docs/1")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":1,"slug":"a","title":"ghost"}`, "--if-match", row.ETag, "--dry-run")
	plan = decode[upsertJSON](t, mustOK(t, env, code, "upsert update --dry-run"))
	if plan.Result != "updated" || plan.Applied || plan.Path != "/work/docs/1" {
		t.Errorf("update plan = %+v, want updated/applied=false", plan)
	}

	if after := sha256File(t, db); after != before {
		t.Errorf("dry-run changed the database bytes: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/docs/2"); code != CodeNotFound.Exit() {
		t.Errorf("dry-run created a row: cat exit = %d", code)
	}
	if got := catRow(t, "/work/docs/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "first"}) {
		t.Errorf("dry-run changed row 1: %+v", got)
	}

	// The plan still checks the precondition, so a stale ETag is a conflict.
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":1,"slug":"a","title":"ghost"}`, "--if-match", "sha256:stale", "--dry-run")
	mustCode(t, env, code, CodeConflict, "dry-run with a stale ETag")
	if after := sha256File(t, db); after != before {
		t.Errorf("failed dry-run changed the database: %s -> %s", before, after)
	}
}

// TestREF04BranchPermissions is the PO's per-branch grant: insert needs touch,
// update needs write, and a read-write mount still needs an explicit policy.
func TestREF04BranchPermissions(t *testing.T) {
	db := setupUpsertFixture(t)

	setMount(t, "rw", []state.Policy{{Ops: []string{"touch", "cat"}, Prefix: "/work"}})
	env, code := runCLI(t, "upsert", "/work/docs", "--data", `{"id":4,"slug":"d","title":"fourth"}`)
	if got := decode[upsertJSON](t, mustOK(t, env, code, "touch-only insert")); got.Result != "created" {
		t.Errorf("touch-only insert = %+v", got)
	}
	row := catRow(t, "/work/docs/1")
	deniedBytes := sha256File(t, db)
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":1,"slug":"a","title":"nope"}`, "--if-match", row.ETag)
	mustCode(t, env, code, CodeDenied, "touch-only update")
	if after := sha256File(t, db); after != deniedBytes {
		t.Errorf("a denied branch changed the database: %s -> %s", deniedBytes, after)
	}

	setMount(t, "rw", []state.Policy{{Ops: []string{"write", "cat"}, Prefix: "/work"}})
	row = catRow(t, "/work/docs/1")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":1,"slug":"a","title":"written"}`, "--if-match", row.ETag)
	if got := decode[upsertJSON](t, mustOK(t, env, code, "write-only update")); got.Result != "updated" {
		t.Errorf("write-only update = %+v", got)
	}
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":5,"slug":"e","title":"fifth"}`)
	mustCode(t, env, code, CodeDenied, "write-only insert")

	// A denied insert is the same denial touch gives: same gate, same message.
	setMount(t, "rw", []state.Policy{{Ops: []string{"cat"}, Prefix: "/work"}})
	envTouch, codeTouch := runCLI(t, "touch", "/work/docs", "--data", `{"id":9,"slug":"z","title":"denied"}`)
	mustCode(t, envTouch, codeTouch, CodeDenied, "touch denied for comparison")
	envUpsert, codeUpsert := runCLI(t, "upsert", "/work/docs", "--data", `{"id":9,"slug":"z","title":"denied"}`)
	mustCode(t, envUpsert, codeUpsert, CodeDenied, "upsert insert denied")
	if envUpsert.Error.Message != envTouch.Error.Message {
		t.Errorf("upsert denial = %q, touch denial = %q; want the same error", envUpsert.Error.Message, envTouch.Error.Message)
	}
}

// TestREF04Integrity refusals: an incidental UNIQUE, a CHECK, NOT NULL and a
// table without a navigable identity all fail without writing.
func TestREF04Integrity(t *testing.T) {
	db := setupUpsertFixture(t)
	setMount(t, "rw", fullWritePolicy())

	// Row 1 already owns alias 'x'; inserting row 2 with it must not land.
	before := sha256File(t, db)
	env, code := runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","alias":"x","title":"dup"}`)
	mustCode(t, env, code, CodeConstraint, "incidental UNIQUE")
	if after := sha256File(t, db); after != before {
		t.Errorf("incidental UNIQUE changed the database: %s -> %s", before, after)
	}

	// The update branch must not move row 1 onto row 2's alias either.
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","alias":"y","title":"second"}`)
	mustOK(t, env, code, "create row 2")
	row1 := catRow(t, "/work/docs/1")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":1,"slug":"a","alias":"y","title":"first"}`, "--if-match", row1.ETag)
	mustCode(t, env, code, CodeConstraint, "incidental UNIQUE on update")
	if got := catRow(t, "/work/docs/1").Fields["alias"]; got != (valueJSON{Kind: "text", Data: "x"}) {
		t.Errorf("row 1 alias after the refused update = %+v, want x", got)
	}

	for _, tc := range []struct {
		name string
		args []string
		want Code
	}{
		{"CHECK violation", []string{"upsert", "/work/docs", "--data", `{"id":3,"slug":"c","title":""}`}, CodeConstraint},
		{"NOT NULL violation", []string{"upsert", "/work/docs", "--data", `{"id":3,"title":"no slug"}`}, CodeConstraint},
		{"no navigable identity", []string{"upsert", "/work/unique_only", "--data", `{"code":"u2","note":"two"}`}, CodeUnsupported},
		{"table with triggers", []string{"upsert", "/work/guarded", "--data", `{"id":1,"v":"x"}`}, CodeUnsupported},
		{"missing primary key", []string{"upsert", "/work/docs", "--data", `{"title":"no id"}`}, CodeUsage},
		{"generated column", []string{"upsert", "/work/generated", "--data", `{"id":1,"n":2,"twice":4}`}, CodeUnsupported},
		{"unknown column", []string{"upsert", "/work/docs", "--data", `{"id":3,"slug":"c","title":"x","nope":1}`}, CodeUsage},
	} {
		env, code := runCLI(t, tc.args...)
		mustCode(t, env, code, tc.want, tc.name)
	}
}

// TestREF04Audit records the branch that actually ran, never a generic upsert,
// and keeps data values out of the log.
func TestREF04Audit(t *testing.T) {
	setupUpsertFixture(t)
	setMount(t, "rw", fullWritePolicy())

	env, code := runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"created-value"}`,
		"--request-id", "req-created", "--actor", "alice")
	mustOK(t, env, code, "audited create")
	row := catRow(t, "/work/docs/2")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"updated-value"}`,
		"--if-match", row.ETag, "--request-id", "req-updated", "--actor", "bob")
	mustOK(t, env, code, "audited update")

	records := auditLines(t)
	created := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "req-created" && r.Status == "ok" })
	if created.Operation != "created" || created.Actor != "alice" || created.Resource != "/work/docs/2" {
		t.Errorf("created audit = %+v", created)
	}
	updated := findAudit(t, records, func(r auditJSON) bool { return r.RequestID == "req-updated" && r.Status == "ok" })
	if updated.Operation != "updated" || updated.Actor != "bob" || updated.Resource != "/work/docs/2" {
		t.Errorf("updated audit = %+v", updated)
	}

	// A denied update still names the real branch it tried to take.
	setMount(t, "rw", []state.Policy{{Ops: []string{"touch", "cat"}, Prefix: "/work"}})
	row = catRow(t, "/work/docs/2")
	env, code = runCLI(t, "upsert", "/work/docs", "--data", `{"id":2,"slug":"b","title":"denied"}`,
		"--if-match", row.ETag, "--request-id", "req-denied", "--actor", "carol")
	mustCode(t, env, code, CodeDenied, "audited denial")
	denied := findAudit(t, auditLines(t), func(r auditJSON) bool { return r.RequestID == "req-denied" })
	if denied.Operation != "updated" || denied.Status != "denied" || denied.Code != "E_DENIED" || denied.Actor != "carol" {
		t.Errorf("denied audit = %+v", denied)
	}

	dir, err := state.StateDir()
	if err != nil {
		t.Fatalf("state.StateDir: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	for _, value := range []string{"created-value", "updated-value"} {
		if bytes.Contains(raw, []byte(value)) {
			t.Errorf("audit log contains the data value %q", value)
		}
	}
}

// TestREF04ConcurrentUpdateOneWinner proves atomicity: two upserts carrying the
// same ETag serialize inside BEGIN IMMEDIATE, one updates and the other is a
// conflict, never a lost update.
func TestREF04ConcurrentUpdateOneWinner(t *testing.T) {
	db := setupUpsertFixture(t)
	setMount(t, "rw", fullWritePolicy())
	row := catRow(t, "/work/docs/1")

	args := []string{"upsert", "/work/docs", "--data", `{"id":1,"slug":"a","title":"race"}`, "--if-match", row.ETag}
	start := make(chan struct{})
	codes := make([]int, 2)
	outs := make([]string, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			codes[i] = run(args, strings.NewReader(""), &stdout, &stderr)
			outs[i] = stdout.String()
		}(i)
	}
	close(start)
	wg.Wait()

	winners, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		var env envelopeJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(outs[i])), &env); err != nil {
			t.Fatalf("concurrent invocation %d stdout is not JSON: %v (%q)", i, err, outs[i])
		}
		switch {
		case codes[i] == 0 && env.OK:
			winners++
		case codes[i] == CodeConflict.Exit() && env.Error != nil && env.Error.Code == string(CodeConflict):
			conflicts++
		default:
			t.Errorf("concurrent invocation %d = exit %d, envelope %+v", i, codes[i], env)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent upserts = %d winners, %d conflicts; want 1 and 1", winners, conflicts)
	}
	if got := catRow(t, "/work/docs/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "race"}) {
		t.Errorf("row after the race = %+v, want race", got)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("stat database: %v", err)
	}
}
