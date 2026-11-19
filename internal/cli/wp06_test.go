package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// wp06Schema is the batch fixture: a writable table with an INTEGER key and
// incoming references (denies rm), a table with a UNIQUE title where rm is
// allowed, and an unrelated table. Batch needs no adversarial schema of its own;
// WP04/WP05 own those constraints.
const wp06Schema = `
CREATE TABLE items (
	id INTEGER PRIMARY KEY,
	v TEXT
);
INSERT INTO items (id, v) VALUES (1, 'one'), (2, 'two'), (3, 'three');
CREATE TABLE notes (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL UNIQUE
);
INSERT INTO notes (id, title) VALUES (1, 'first'), (2, 'second');
CREATE TABLE refs (
	id INTEGER PRIMARY KEY,
	item_id INTEGER REFERENCES items(id)
);
INSERT INTO refs VALUES (1, 1);
`

type batchResultJSON struct {
	Index  int                  `json:"index"`
	Op     string               `json:"op"`
	Path   string               `json:"path"`
	Fields map[string]valueJSON `json:"fields"`
	ETag   string               `json:"etag"`
}

type batchPlanJSON struct {
	Index int      `json:"index"`
	Op    string   `json:"op"`
	Plan  planJSON `json:"plan"`
}

type batchDataJSON struct {
	Operations []json.RawMessage `json:"operations"`
}

func setupWP06Fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	db := newFixture(t, wp06Schema)
	if _, code := runCLI(t, "mount", db, "/"+writeAlias); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	setMount(t, "rw", fullMutationPolicy())
	return db
}

// batchPayload joins operation wrappers into the --data array.
func batchPayload(ops ...string) string { return "[" + strings.Join(ops, ",") + "]" }

func batchTouch(path, data string) string {
	return fmt.Sprintf(`{"op":"touch","path":%q,"data":%s}`, path, data)
}

func batchWrite(path, data, ifMatch string) string {
	return fmt.Sprintf(`{"op":"write","path":%q,"data":%s,"if_match":%q}`, path, data, ifMatch)
}

func batchRemove(path, ifMatch string) string {
	return fmt.Sprintf(`{"op":"rm","path":%q,"if_match":%q}`, path, ifMatch)
}

// TestWP06RollbackOnSecondFailure is the FR-12 criterion: the first operation
// is applied inside the batch transaction and the second fails, so the row the
// first one wrote is untouched and the envelope is the standard error shape
// naming the failing index and operation.
func TestWP06RollbackOnSecondFailure(t *testing.T) {
	db := setupWP06Fixture(t)
	first := catRow(t, "/work/items/1")
	payload := batchPayload(
		batchWrite("/work/items/1/v", `"changed"`, first.ETag),
		batchWrite("/work/notes/1/title", `"changed"`, "sha256:stale"),
	)

	before := sha256File(t, db)
	env, code := runCLI(t, "batch", "--data", payload, "--request-id", "wp06-rollback", "--actor", "alice")
	mustCode(t, env, code, CodeConflict, "batch with a failing second operation")
	if !strings.Contains(env.Error.Message, "batch operation 1 (write)") {
		t.Errorf("batch error message = %q, want the failing index and operation", env.Error.Message)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("a rolled-back batch changed the database: %s -> %s", before, after)
	}
	if got := catRow(t, "/work/items/1").Fields["v"]; got != (valueJSON{Kind: "text", Data: "one"}) {
		t.Errorf("the first operation persisted after rollback: v = %+v, want one", got)
	}
	if got := catRow(t, "/work/notes/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "first"}) {
		t.Errorf("the failing operation changed its row: title = %+v, want first", got)
	}

	// Every attempted operation is audited as an error once the batch rolls
	// back; none is reported as ok.
	records := auditLines(t)
	attempts, failures := 0, 0
	for _, r := range records {
		if r.RequestID != "wp06-rollback" {
			continue
		}
		switch r.Status {
		case "attempt":
			attempts++
		case "error":
			failures++
			if r.Code != "E_CONFLICT" || r.Actor != "alice" {
				t.Errorf("rolled-back audit = %+v, want E_CONFLICT by alice", r)
			}
		case "ok":
			t.Errorf("rolled-back batch reported ok: %+v", r)
		}
	}
	if attempts != 2 || failures != 2 {
		t.Errorf("rolled-back batch left %d attempts and %d errors, want 2 and 2", attempts, failures)
	}
}

// TestWP06NoChainingWithinBatch fixes the documented contract: an operation
// cannot write a row an earlier operation of the same batch created, because
// the client's if_match describes the state before the batch.
func TestWP06NoChainingWithinBatch(t *testing.T) {
	db := setupWP06Fixture(t)
	prior := "sha256:" + strings.Repeat("a", 64)
	payload := batchPayload(
		batchTouch("/work/items", `{"v":"created"}`),
		batchWrite("/work/items/4/v", `"chained"`, prior),
	)

	before := sha256File(t, db)
	env, code := runCLI(t, "batch", "--data", payload)
	mustCode(t, env, code, CodeConflict, "batch chaining on a row its first operation created")
	if !strings.Contains(env.Error.Message, "batch operation 1 (write)") {
		t.Errorf("chained batch error = %q, want the failing index and operation", env.Error.Message)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("a rolled-back chained batch changed the database: %s -> %s", before, after)
	}
	if _, code := runCLI(t, "cat", "/work/items/4"); code != CodeNotFound.Exit() {
		t.Errorf("the first touch persisted after the rollback: exit %d", code)
	}
}

// TestWP06BlockedBeginAuditsAttempts is the MENOR regression: a competing
// writer holds SQLite's write lock, so the batch fails before its callback runs.
// The lock contention surfaces as E_TIMEOUT because busy_timeout and the
// operation deadline share one flag (DESIGN §10); the branch is the same one a
// busy acquire or BEGIN takes. Every operation still gets an attempt and an
// error, rather than the batch failing silently outside the audit log.
func TestWP06BlockedBeginAuditsAttempts(t *testing.T) {
	db := setupWP06Fixture(t)
	item := catRow(t, "/work/items/1")

	blocker, err := sql.Open("sqlite", "file:"+db)
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	t.Cleanup(func() { blocker.Close() })
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	// A no-op update takes RESERVED and keeps it until rollback, so the
	// batch's BEGIN IMMEDIATE cannot acquire the write lock.
	if _, err := tx.Exec("UPDATE items SET v = v WHERE id = 1"); err != nil {
		t.Fatalf("hold the write lock: %v", err)
	}

	payload := batchPayload(
		batchWrite("/work/items/1/v", `"blocked"`, item.ETag),
		batchTouch("/work/items", `{"v":"blocked"}`),
	)
	env, code := runCLI(t, "batch", "--data", payload, "--timeout", "1", "--request-id", "wp06-busy")
	if code != CodeBusy.Exit() || env.OK || env.Error == nil {
		t.Fatalf("blocked BEGIN = exit %d, envelope %+v; want the lock-contention exit %d", code, env, CodeBusy.Exit())
	}
	if env.Error.Code != string(CodeBusy) && env.Error.Code != string(CodeTimeout) {
		t.Fatalf("blocked BEGIN code = %q, want E_BUSY or E_TIMEOUT", env.Error.Code)
	}

	attempts, failures := 0, 0
	for _, r := range auditLines(t) {
		if r.RequestID != "wp06-busy" {
			continue
		}
		switch r.Status {
		case "attempt":
			attempts++
		case "error":
			failures++
			if r.Code != env.Error.Code {
				t.Errorf("blocked BEGIN audit = %+v, want code %s", r, env.Error.Code)
			}
		}
	}
	if attempts != 2 || failures != 2 {
		t.Errorf("blocked BEGIN left %d attempts and %d errors, want 2 and 2", attempts, failures)
	}
	_ = tx.Rollback()
}

// TestWP06SuccessOrder is the success contract: touch, write and rm in one
// transaction, each result in input order.
func TestWP06SuccessOrder(t *testing.T) {
	setupWP06Fixture(t)
	second := catRow(t, "/work/items/2")
	note := catRow(t, "/work/notes/2")
	payload := batchPayload(
		batchTouch("/work/items", `{"v":"four"}`),
		batchWrite("/work/items/2/v", `"TWO"`, second.ETag),
		batchRemove("/work/notes/2", note.ETag),
	)

	env, code := runCLI(t, "batch", "--data", payload, "--request-id", "wp06-success", "--actor", "alice")
	data := decode[batchDataJSON](t, mustOK(t, env, code, "batch touch+write+rm"))
	if len(data.Operations) != 3 {
		t.Fatalf("batch returned %d results, want 3", len(data.Operations))
	}
	want := []struct{ op, path string }{
		{"touch", "/work/items/4"},
		{"write", "/work/items/2"},
		{"rm", "/work/notes/2"},
	}
	for i, tc := range want {
		got := decode[batchResultJSON](t, data.Operations[i])
		if got.Index != i || got.Op != tc.op || got.Path != tc.path {
			t.Errorf("result %d = %+v, want index %d, op %s, path %s", i, got, i, tc.op, tc.path)
		}
		if len(got.ETag) != len("sha256:")+64 {
			t.Errorf("result %d ETag = %q", i, got.ETag)
		}
	}
	if got := decode[batchResultJSON](t, data.Operations[0]).Fields["v"]; got != (valueJSON{Kind: "text", Data: "four"}) {
		t.Errorf("touch result = %+v, want v four", got)
	}
	if got := decode[batchResultJSON](t, data.Operations[1]).Fields["v"]; got != (valueJSON{Kind: "text", Data: "TWO"}) {
		t.Errorf("write result = %+v, want v TWO", got)
	}

	if got := catRow(t, "/work/items/2").Fields["v"]; got != (valueJSON{Kind: "text", Data: "TWO"}) {
		t.Errorf("write did not persist: %+v", got)
	}
	if got := catRow(t, "/work/items/4").Fields["v"]; got != (valueJSON{Kind: "text", Data: "four"}) {
		t.Errorf("touch did not persist: %+v", got)
	}
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != CodeNotFound.Exit() {
		t.Errorf("rm did not persist: exit %d", code)
	}
	if got := catRow(t, "/work/items/1").Fields["v"]; got != (valueJSON{Kind: "text", Data: "one"}) {
		t.Errorf("batch touched an unrelated row: %+v", got)
	}

	// Success audits exactly one attempt and one ok per operation, in order.
	records := auditLines(t)
	var statuses []string
	for _, r := range records {
		if r.RequestID == "wp06-success" {
			statuses = append(statuses, r.Status+"/"+r.Operation)
		}
	}
	wantStatuses := []string{
		"attempt/touch", "attempt/write", "attempt/rm",
		"ok/touch", "ok/write", "ok/rm",
	}
	if strings.Join(statuses, " ") != strings.Join(wantStatuses, " ") {
		t.Errorf("batch audit = %v, want %v", statuses, wantStatuses)
	}
}

// TestWP06BatchLimit is D-8: 100 operations is the maximum, 101 is E_LIMIT.
func TestWP06BatchLimit(t *testing.T) {
	db := setupWP06Fixture(t)

	ops := make([]string, 101)
	for i := range ops {
		ops[i] = batchTouch("/work/items", `{"v":"x"}`)
	}
	before := sha256File(t, db)
	env, code := runCLI(t, "batch", "--data", batchPayload(ops...))
	mustCode(t, env, code, CodeLimit, "batch with 101 operations")
	if !strings.Contains(env.Error.Message, "maximum is 100") {
		t.Errorf("limit message = %q, want the D-8 maximum", env.Error.Message)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("an over-limit batch changed the database: %s -> %s", before, after)
	}

	env, code = runCLI(t, "batch", "--data", batchPayload(ops[:100]...))
	data := decode[batchDataJSON](t, mustOK(t, env, code, "batch with 100 operations"))
	if len(data.Operations) != 100 {
		t.Fatalf("100-operation batch returned %d results", len(data.Operations))
	}
	first := decode[batchResultJSON](t, data.Operations[0])
	last := decode[batchResultJSON](t, data.Operations[99])
	if first.Path != "/work/items/4" || last.Path != "/work/items/103" {
		t.Errorf("100-operation paths = %s..%s, want /work/items/4../work/items/103", first.Path, last.Path)
	}
}

// TestWP06StdinPayloadLimit is the stdin budget: an over-cap payload is
// E_LIMIT before it is parsed or checked against D-8.
func TestWP06StdinPayloadLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	payload := `{"v":"` + strings.Repeat("x", maxStdinPayload) + `"}`

	var stdout, stderr bytes.Buffer
	code := run([]string{"touch", "/work/items", "--data", "-"}, strings.NewReader(payload), &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("over-limit stdin stderr = %q, want empty", stderr.String())
	}
	var env envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("over-limit stdin stdout is not one JSON object: %v (%q)", err, stdout.String())
	}
	mustCode(t, env, code, CodeLimit, "over-limit --data from stdin")
}

// TestWP06BatchRejectsMixedAlias proves the one-mount rule.
func TestWP06BatchRejectsMixedAlias(t *testing.T) {
	setupWP06Fixture(t)
	other := newFixture(t, wp06Schema)
	if _, code := runCLI(t, "mount", other, "/other"); code != 0 {
		t.Fatalf("mount /other exit = %d", code)
	}
	payload := batchPayload(
		batchTouch("/work/items", `{"v":"here"}`),
		batchTouch("/other/items", `{"v":"there"}`),
	)
	env, code := runCLI(t, "batch", "--data", payload)
	mustCode(t, env, code, CodeUsage, "batch mixing two aliases")
	if !strings.Contains(env.Error.Message, "one batch targets one mount") {
		t.Errorf("mixed-alias message = %q", env.Error.Message)
	}

	// A typo in the wrapper is a usage error, never a silent no-op.
	for _, bad := range []string{
		`[{"op":"rename","path":"/work/items"}]`,
		`[{"op":"touch","path":"/work/items"}]`,
		`[{"op":"touch","path":"/work/items","data":{},"extra":1}]`,
		`[]`,
		`{"op":"touch"}`,
	} {
		env, code := runCLI(t, "batch", "--data", bad)
		mustCode(t, env, code, CodeUsage, "malformed batch "+bad)
	}
}

// TestWP06BatchDryRun is the FR-10 criterion for the batch: every operation is
// planned, the response says mutates false and the database is untouched.
func TestWP06BatchDryRun(t *testing.T) {
	db := setupWP06Fixture(t)
	item := catRow(t, "/work/items/2")
	note := catRow(t, "/work/notes/2")
	payload := batchPayload(
		batchWrite("/work/items/2/v", `"ghost"`, item.ETag),
		batchRemove("/work/notes/2", note.ETag),
	)

	before := sha256File(t, db)
	env, code := runCLI(t, "batch", "--data", payload, "--dry-run")
	data := decode[batchDataJSON](t, mustOK(t, env, code, "batch --dry-run"))
	if len(data.Operations) != 2 {
		t.Fatalf("dry-run returned %d plans, want 2", len(data.Operations))
	}
	first := decode[batchPlanJSON](t, data.Operations[0])
	if first.Index != 0 || first.Op != "write" || first.Plan.Path != "/work/items/2/v" ||
		!first.Plan.DryRun || first.Plan.Mutates || first.Plan.Warning == "" || first.Plan.IfMatch != item.ETag {
		t.Errorf("write plan = %+v", first)
	}
	second := decode[batchPlanJSON](t, data.Operations[1])
	if second.Index != 1 || second.Op != "rm" || second.Plan.Path != "/work/notes/2" ||
		!second.Plan.DryRun || second.Plan.Mutates || second.Plan.Warning == "" {
		t.Errorf("rm plan = %+v", second)
	}
	if after := sha256File(t, db); after != before {
		t.Errorf("batch --dry-run changed the database: %s -> %s", before, after)
	}
	if got := catRow(t, "/work/items/2").Fields["v"]; got != (valueJSON{Kind: "text", Data: "two"}) {
		t.Errorf("batch --dry-run wrote a field: %+v", got)
	}
	if _, code := runCLI(t, "cat", "/work/notes/2"); code != 0 {
		t.Errorf("batch --dry-run deleted a row: exit %d", code)
	}

	// The plan consults each ETag precondition, just like the individual command.
	env, code = runCLI(t, "batch", "--data", batchPayload(batchWrite("/work/items/2/v", `"ghost"`, "sha256:stale")), "--dry-run")
	mustCode(t, env, code, CodeConflict, "batch --dry-run with a stale ETag")
}

// TestWP06BinaryAcceptance runs the Gate M3 flows against the built binary: a
// batch rolls back end to end, two competing processes mutate one row without
// corrupting the file or violating the ETag, and two disjoint batches commit.
func TestWP06BinaryAcceptance(t *testing.T) {
	bin := buildBinary(t)

	t.Run("rollback", func(t *testing.T) {
		db, env := setupWP06Binary(t, bin)
		row := binaryCatRow(t, bin, env, "/work/items/1")
		payload := batchPayload(
			batchWrite("/work/items/1/v", `"changed"`, row.ETag),
			batchWrite("/work/notes/1/title", `"changed"`, "sha256:stale"),
		)
		before := sha256File(t, db)
		envJSON, code := runBinaryWith(t, bin, env, "batch", "--data", payload)
		mustCode(t, envJSON, code, CodeConflict, "binary batch rollback")
		if after := sha256File(t, db); after != before {
			t.Errorf("binary rollback changed the database: %s -> %s", before, after)
		}
		if got := binaryCatRow(t, bin, env, "/work/items/1").Fields["v"]; got != (valueJSON{Kind: "text", Data: "one"}) {
			t.Errorf("binary rollback persisted the first operation: %+v", got)
		}
	})

	t.Run("two processes writing one row", func(t *testing.T) {
		db, env := setupWP06Binary(t, bin)
		row := binaryCatRow(t, bin, env, "/work/items/1")

		values := []string{"alpha", "beta"}
		type outcome struct {
			value string
			env   envelopeJSON
			code  int
			err   error
		}
		results := make([]outcome, len(values))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, value := range values {
			wg.Add(1)
			go func(i int, value string) {
				defer wg.Done()
				<-start
				var stdout, stderr bytes.Buffer
				cmd := exec.Command(bin, "write", "/work/items/1/v", "--data", strconv.Quote(value), "--if-match", row.ETag)
				cmd.Env = env
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				results[i].value = value
				err := cmd.Run()
				if err != nil {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) {
						results[i].err = err
						return
					}
					results[i].code = exitErr.ExitCode()
				}
				if stderr.Len() != 0 {
					results[i].err = fmt.Errorf("stderr = %q", stderr.String())
					return
				}
				if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &results[i].env); err != nil {
					results[i].err = fmt.Errorf("stdout is not JSON: %v (%q)", err, stdout.String())
				}
			}(i, value)
		}
		close(start)
		wg.Wait()

		var winner, loser *outcome
		for i := range results {
			if results[i].err != nil {
				t.Fatalf("competing write %q: %v", results[i].value, results[i].err)
			}
			switch {
			case results[i].code == 0:
				if winner != nil {
					t.Fatalf("both competing writes succeeded: %+v", results)
				}
				winner = &results[i]
			case results[i].code == CodeConflict.Exit() || results[i].code == CodeBusy.Exit():
				if loser != nil {
					t.Fatalf("both competing writes were rejected: %+v", results)
				}
				loser = &results[i]
			default:
				t.Fatalf("competing write %q exit = %d, want 0, %d or %d", results[i].value, results[i].code, CodeConflict.Exit(), CodeBusy.Exit())
			}
		}
		if winner == nil || loser == nil {
			t.Fatalf("competing writes = %+v, want exactly one winner and one rejection", results)
		}
		if winner.env.OK != true || loser.env.Error == nil ||
			(loser.env.Error.Code != string(CodeConflict) && loser.env.Error.Code != string(CodeBusy)) {
			t.Errorf("competitor envelopes = winner %+v, loser %+v", winner.env, loser.env)
		}

		final := binaryCatRow(t, bin, env, "/work/items/1")
		if got := final.Fields["v"]; got != (valueJSON{Kind: "text", Data: winner.value}) {
			t.Errorf("final value = %+v, want the winner's %q", got, winner.value)
		}
		if len(final.ETag) != len("sha256:")+64 {
			t.Errorf("final ETag = %q", final.ETag)
		}
		requireIntegrity(t, db)
	})

	t.Run("two concurrent batches", func(t *testing.T) {
		db, env := setupWP06Binary(t, bin)
		item2 := binaryCatRow(t, bin, env, "/work/items/2")
		item3 := binaryCatRow(t, bin, env, "/work/items/3")
		note1 := binaryCatRow(t, bin, env, "/work/notes/1")
		note2 := binaryCatRow(t, bin, env, "/work/notes/2")
		payloads := []string{
			batchPayload(
				batchWrite("/work/items/2/v", `"A"`, item2.ETag),
				batchWrite("/work/notes/1/title", `"A"`, note1.ETag),
			),
			batchPayload(
				batchWrite("/work/items/3/v", `"B"`, item3.ETag),
				batchWrite("/work/notes/2/title", `"B"`, note2.ETag),
			),
		}
		results := make([]struct {
			env  envelopeJSON
			code int
			err  error
		}, len(payloads))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, payload := range payloads {
			wg.Add(1)
			go func(i int, payload string) {
				defer wg.Done()
				<-start
				var stdout, stderr bytes.Buffer
				cmd := exec.Command(bin, "batch", "--data", payload)
				cmd.Env = env
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if err != nil {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) {
						results[i].err = err
						return
					}
					results[i].code = exitErr.ExitCode()
				}
				if stderr.Len() != 0 {
					results[i].err = fmt.Errorf("stderr = %q", stderr.String())
					return
				}
				if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &results[i].env); err != nil {
					results[i].err = fmt.Errorf("stdout is not JSON: %v (%q)", err, stdout.String())
				}
			}(i, payload)
		}
		close(start)
		wg.Wait()

		for i, r := range results {
			if r.err != nil {
				t.Fatalf("concurrent batch %d: %v", i, r.err)
			}
			if r.code != 0 || !r.env.OK {
				t.Errorf("concurrent batch %d = exit %d, %+v; want success", i, r.code, r.env)
			}
		}
		if got := binaryCatRow(t, bin, env, "/work/items/2").Fields["v"]; got != (valueJSON{Kind: "text", Data: "A"}) {
			t.Errorf("batch A item = %+v, want A", got)
		}
		if got := binaryCatRow(t, bin, env, "/work/items/3").Fields["v"]; got != (valueJSON{Kind: "text", Data: "B"}) {
			t.Errorf("batch B item = %+v, want B", got)
		}
		if got := binaryCatRow(t, bin, env, "/work/notes/1").Fields["title"]; got != (valueJSON{Kind: "text", Data: "A"}) {
			t.Errorf("batch A note = %+v, want A", got)
		}
		if got := binaryCatRow(t, bin, env, "/work/notes/2").Fields["title"]; got != (valueJSON{Kind: "text", Data: "B"}) {
			t.Errorf("batch B note = %+v, want B", got)
		}
		requireIntegrity(t, db)
	})
}

// setupWP06Binary prepares a fresh fixture plus the environment for the built
// binary and the in-process helpers used to read rows back.
func setupWP06Binary(t *testing.T, bin string) (string, []string) {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	db := newFixture(t, wp06Schema)
	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir, "XDG_STATE_HOME="+stateDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_STATE_HOME", stateDir)
	envJSON, code := runBinaryWith(t, bin, env, "mount", db, "/"+writeAlias)
	mustOK(t, envJSON, code, "binary mount")
	setMount(t, "rw", fullMutationPolicy())
	return db, env
}

func binaryCatRow(t *testing.T, bin string, env []string, path string) rowJSON {
	t.Helper()
	envJSON, code := runBinaryWith(t, bin, env, "cat", path)
	return decode[rowJSON](t, mustOK(t, envJSON, code, "binary cat", path))
}

// requireIntegrity proves the competing processes left a healthy database.
func requireIntegrity(t *testing.T, db string) {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Fatalf("open fixture for integrity check: %v", err)
	}
	defer conn.Close()
	var check string
	if err := conn.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Errorf("integrity_check = %q, want ok", check)
	}
}
