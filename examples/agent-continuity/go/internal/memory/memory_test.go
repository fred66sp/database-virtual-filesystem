package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var uuidPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDv7Shape(t *testing.T) {
	moment := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	first := newID(moment)
	second := newID(moment.Add(time.Millisecond))
	for name, id := range map[string]string{"first": first, "second": second} {
		if !uuidPattern.MatchString(id) {
			t.Errorf("%s id %q is not a UUIDv7", name, id)
		}
	}
	if first >= second {
		t.Errorf("UUIDv7 does not sort by time: %q >= %q", first, second)
	}
}

// tempStore prepares a clean base with the real init.sh and returns the store
// over it, as tests/run-tests.sh does.
func tempStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	if _, err := exec.LookPath("db-ai"); err != nil {
		t.Fatalf("db-ai is not in PATH: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ACM_HOME", filepath.Join(root, "proyecto"))
	t.Setenv("ACM_ALIAS", "mem-test")
	initPath, err := filepath.Abs("../../../init.sh")
	if err != nil {
		t.Fatalf("resolve init.sh: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", initPath)
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init.sh: %v\n%s", err, output)
	}
	return New("mem-test", "go-test", nil)
}

func tableRows(t *testing.T, store *Store, table string, wheres ...string) []Row {
	t.Helper()
	rows, err := store.rows(context.Background(), table, wheres, "", 1000)
	if err != nil {
		t.Fatalf("find %s %v: %v", table, wheres, err)
	}
	return rows
}

func errorCode(t *testing.T, err error) string {
	t.Helper()
	var contract *Error
	if !errors.As(err, &contract) {
		t.Fatalf("want a contract error, got %v", err)
	}
	return contract.Code
}

func TestStartOpensAndRestores(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()

	markdown, err := store.Start(ctx, "suite-a", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.HasPrefix(markdown, "Sesión iniciada.") {
		t.Errorf("first start:\n%s", markdown)
	}
	if strings.Contains(markdown, "Objetivo:") {
		t.Errorf("default goal must not be shown:\n%s", markdown)
	}
	restored, err := store.Start(ctx, "suite-a", "otro")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.HasPrefix(restored, "Sesión restaurada.") {
		t.Errorf("second start:\n%s", restored)
	}
	if rows := tableRows(t, store, "sessions"); len(rows) != 1 {
		t.Errorf("sessions: got %d, want 1", len(rows))
	}

	withGoal, err := store.Start(ctx, "suite-b", "migrar el parser")
	if err != nil {
		t.Fatalf("start with goal: %v", err)
	}
	if !strings.Contains(withGoal, "Objetivo: migrar el parser") {
		t.Errorf("goal missing:\n%s", withGoal)
	}
}

func TestObservationDedupAndConsolidation(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()
	if _, err := store.Start(ctx, "suite-a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	observation := Observation{
		Title: "El parser exige UTC", Body: "Las fechas sin zona se leen como UTC.",
		Topic: "parser/fechas",
	}
	if result, err := store.RecordObservation(ctx, "suite-a", "learning", observation); err != nil || result != "created" {
		t.Fatalf("first capture: result=%q err=%v", result, err)
	}
	observation.Title = "  El parser exige UTC  "
	observation.Body = "Las  fechas sin zona se leen como UTC."
	if result, err := store.RecordObservation(ctx, "suite-a", "learning", observation); err != nil || result != "consolidated" {
		t.Fatalf("second capture: result=%q err=%v", result, err)
	}
	rows := tableRows(t, store, "observations")
	if len(rows) != 1 {
		t.Fatalf("observations: got %d, want 1", len(rows))
	}
	if count := rows[0].Values()["duplicate_count"]; count != "2" {
		t.Errorf("duplicate_count: got %q, want 2", count)
	}
}

func TestPreferenceSupersession(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()
	if _, err := store.Start(ctx, "suite-a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if result, _ := store.SetPreference(ctx, "suite-a", "tools/editor", "vim", "", "human"); result != "created" {
		t.Fatalf("first preference: %q", result)
	}
	if result, _ := store.SetPreference(ctx, "suite-a", "tools/editor", "vim", "", "human"); result != "existing" {
		t.Fatalf("repeated preference: %q", result)
	}
	if result, err := store.SetPreference(ctx, "suite-a", "tools/editor", "helix", "", "human"); err != nil || result != "created" {
		t.Fatalf("second value: result=%q err=%v", result, err)
	}
	active := tableRows(t, store, "preferences", "pref_key=tools/editor", "status=active")
	if len(active) != 1 || active[0].Values()["value"] != "helix" {
		t.Fatalf("active preference: %v", active)
	}
	if superseded := tableRows(t, store, "preferences", "pref_key=tools/editor", "status=superseded"); len(superseded) != 1 {
		t.Errorf("superseded rows: got %d, want 1", len(superseded))
	}
	if total := tableRows(t, store, "preferences"); len(total) != 2 {
		t.Errorf("total preferences: got %d, want 2", len(total))
	}
}

func TestCheckpointSupersessionAndClose(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()
	if _, err := store.Start(ctx, "suite-a", "cerrar el ciclo"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if result, _, err := store.WriteCheckpoint(ctx, "suite-a", "estado uno", "paso uno", "", ""); err != nil || result != "created" {
		t.Fatalf("first checkpoint: result=%q err=%v", result, err)
	}
	if result, _, err := store.WriteCheckpoint(ctx, "suite-a", "estado uno", "paso uno", "", ""); err != nil || result != "existing" {
		t.Fatalf("duplicate checkpoint: result=%q err=%v", result, err)
	}
	if result, _, err := store.WriteCheckpoint(ctx, "suite-a", "estado dos", "paso dos", "", ""); err != nil || result != "created" {
		t.Fatalf("second checkpoint: result=%q err=%v", result, err)
	}
	if active := tableRows(t, store, "checkpoints", "status=active"); len(active) != 1 {
		t.Errorf("active checkpoints: got %d, want 1", len(active))
	}
	if superseded := tableRows(t, store, "checkpoints", "status=superseded"); len(superseded) != 1 {
		t.Errorf("superseded checkpoints: got %d, want 1", len(superseded))
	}

	endedAt, checkpointID, err := store.CloseSession(ctx, "suite-a", "ciclo cerrado", "", "", "", "")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if endedAt == "" || checkpointID == "" {
		t.Fatalf("close answer: ended_at=%q checkpoint=%q", endedAt, checkpointID)
	}
	if _, err := store.OpenSession(ctx, "suite-a"); errorCode(t, err) != codeConstraint {
		t.Fatalf("closed session must be refused, got %v", err)
	}
	if _, err := store.Start(ctx, "suite-a", ""); errorCode(t, err) != codeConstraint {
		t.Fatalf("start on a closed session must be refused, got %v", err)
	}
	sessions := tableRows(t, store, "sessions")
	if status := sessions[0].Values()["status"]; status != "closed" {
		t.Errorf("session status: got %q, want closed", status)
	}
}

func TestBatchRollback(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()
	if _, err := store.Start(ctx, "suite-a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	session, etag, err := store.loadSession(ctx, "suite-a")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	operations := []any{
		map[string]any{
			"op": "write", "path": store.aliasPath("sessions") + "/" + session.ID,
			"if_match": etag,
			"data":     map[string]any{"summary": "no debe quedar"},
		},
		map[string]any{
			"op": "write", "path": store.aliasPath("sessions") + "/" + session.ID,
			"if_match": etag,
			"data":     map[string]any{"columna_inexistente": "x"},
		},
	}
	if err := store.batch(ctx, operations); err == nil {
		t.Fatal("batch with an invalid operation must fail")
	}
	after, _, err := store.loadSession(ctx, "suite-a")
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if after.Summary != "" {
		t.Errorf("batch did not roll back: summary=%q", after.Summary)
	}
}

func TestRecallFoldingAndRelaxation(t *testing.T) {
	store := tempStore(t)
	ctx := context.Background()
	if _, err := store.Start(ctx, "suite-a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	mustRecord := func(kind, title, body, topic string) {
		t.Helper()
		rationale := ""
		if kind == "decision" {
			rationale = "para que el parser no dependa de la máquina"
		}
		if _, err := store.RecordObservation(ctx, "suite-a", kind, Observation{
			Title: title, Body: body, Topic: topic, Rationale: rationale,
		}); err != nil {
			t.Fatalf("record %q: %v", title, err)
		}
	}
	mustRecord("decision", "Decisión sobre el parser", "Las fechas usan UTC.",
		"parser/fechas")
	mustRecord("discovery", "Otro hallazgo", "El caché se invalida al escribir.", "")

	folded, err := store.Recall(ctx, "suite-a", "DECISIÓN", 10)
	if err != nil {
		t.Fatalf("recall folded: %v", err)
	}
	if folded.Count != 1 || folded.Relaxed {
		t.Errorf("folded recall: count=%d relaxed=%v", folded.Count, folded.Relaxed)
	}
	exact, err := store.Recall(ctx, "suite-a", "parser fechas utc", 10)
	if err != nil || exact.Count != 1 || exact.Relaxed {
		t.Fatalf("exact recall: %+v err=%v", exact, err)
	}
	relaxed, err := store.Recall(ctx, "suite-a", "parser fechas desaparecido", 10)
	if err != nil || relaxed.Count != 1 || !relaxed.Relaxed {
		t.Fatalf("relaxed recall: %+v err=%v", relaxed, err)
	}
	empty, err := store.Recall(ctx, "suite-a", "zzz", 10)
	if err != nil || empty.Count != 0 || empty.Message == "" {
		t.Fatalf("empty recall: %+v err=%v", empty, err)
	}
	stopword, err := store.Recall(ctx, "suite-a", "the parser", 10)
	if err != nil || stopword.Count != 1 || !stopword.Relaxed {
		t.Fatalf("stopword recall: %+v err=%v", stopword, err)
	}
	if _, err := store.Recall(ctx, "suite-a", "...", 10); errorCode(t, err) != codeUsage {
		t.Fatalf("empty query must be E_USAGE, got %v", err)
	}
}

func TestCommitUnknownIsNotRetried(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls.log")
	stub := filepath.Join(root, "db-ai")
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' "$1" >> %q
if [ "$1" = "find" ]; then
  case "$2" in
    */sessions)
      printf '{"version":1,"ok":true,"data":{"rows":[{"path":"%%s","fields":{"id":{"kind":"text","data":"s1"},"goal":{"kind":"text","data":"objetivo"},"status":{"kind":"text","data":"open"},"summary":{"kind":"null","data":""}},"etag":"sha256:x"}]},"meta":{}}\n' "$2"
      exit 0 ;;
    *)
      printf '{"version":1,"ok":true,"data":{"rows":[]},"meta":{}}\n'
      exit 0 ;;
  esac
fi
if [ "$1" = "batch" ]; then
  printf '{"version":1,"ok":false,"error":{"code":"E_COMMIT_UNKNOWN","message":"resultado desconocido"}}\n'
  exit 10
fi
printf '{"version":1,"ok":false,"error":{"code":"E_INTERNAL","message":"llamada inesperada"}}\n'
exit 9
`, logPath)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	store := New("mem-test", "go-test", nil)
	store.db = dbai{binary: stub}
	_, _, err := store.WriteCheckpoint(context.Background(), "s1", "estado", "paso", "", "")
	if err == nil {
		t.Fatal("WriteCheckpoint with E_COMMIT_UNKNOWN must fail")
	}
	if code := errorCode(t, err); code != "E_COMMIT_UNKNOWN" {
		t.Errorf("code: got %q, want E_COMMIT_UNKNOWN", code)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	batches := 0
	for _, call := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if call == "batch" {
			batches++
		}
	}
	if batches != 1 {
		t.Errorf("batch invocations: got %d, want 1 (E_COMMIT_UNKNOWN must not retry)", batches)
	}
}
