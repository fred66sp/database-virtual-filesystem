package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acm-e2e-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "acm")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(dir)
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type base struct {
	db  string
	env []string
}

// newBase prepares a clean base with the real init.sh, as the bash suite does.
func newBase(t *testing.T) *base {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "proyecto")
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"ACM_HOME="+home,
		"ACM_ALIAS=mem-test",
		"OPENCODE_SESSION_ID=aif-session",
	)
	initPath, err := filepath.Abs("../../../init.sh")
	if err != nil {
		t.Fatalf("resolve init.sh: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", initPath)
	command.Env = env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init.sh: %v\n%s", err, output)
	}
	return &base{db: filepath.Join(home, ".agent-memory", "memory.db"), env: env}
}

// run executes the binary and returns stdout, stderr and the exit code.
func (b *base) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = b.env
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run %v: %v\n%s", args, err, stderr.String())
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func (b *base) aif(t *testing.T, request string) (map[string]any, int) {
	t.Helper()
	stdout, stderr, code := b.run(t, request, "aif")
	var response map[string]any
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("aif answer is not JSON: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return response, code
}

func (b *base) count(t *testing.T, query string) int {
	t.Helper()
	output, err := exec.Command("sqlite3", b.db, query).Output()
	if err != nil {
		t.Fatalf("sqlite3 %q: %v", query, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("count %q: %v", output, err)
	}
	return count
}

func TestVersion(t *testing.T) {
	base := newBase(t)
	stdout, _, code := base.run(t, "", "--version")
	if code != 0 || stdout != "acm "+version+"\n" {
		t.Errorf("--version: code=%d stdout=%q", code, stdout)
	}
}

func TestStartMarkdown(t *testing.T) {
	base := newBase(t)
	stdout, stderr, code := base.run(t, "", "start", "--session", "start-a", "--goal", "migrar")
	if code != 0 {
		t.Fatalf("start: code=%d stderr=%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "Sesión iniciada.\n\nObjetivo: migrar\n") {
		t.Errorf("first start:\n%s", stdout)
	}
	restored, _, code := base.run(t, "", "start", "--session", "start-a")
	if code != 0 || !strings.HasPrefix(restored, "Sesión restaurada.") {
		t.Errorf("restored start: code=%d\n%s", code, restored)
	}
	if got := base.count(t, "SELECT count(*) FROM sessions WHERE id='start-a'"); got != 1 {
		t.Errorf("session rows: got %d, want 1", got)
	}
}

func TestDiscovery(t *testing.T) {
	base := newBase(t)
	response, code := base.aif(t, `{"aif":"0.2"}`)
	if code != 0 {
		t.Fatalf("discovery exit: %d", code)
	}
	operations, ok := response["operations"].([]any)
	if !ok || len(operations) != 10 {
		t.Fatalf("discovery operations: %v", response["operations"])
	}
	if _, hasResult := response["result"]; hasResult {
		t.Errorf("discovery must not carry result: %v", response)
	}
	ids := map[string]bool{}
	for _, entry := range operations {
		info := entry.(map[string]any)
		ids[info["id"].(string)] = true
	}
	for _, want := range []string{"decision", "learning", "discovery", "bugfix",
		"pattern", "architecture", "preference", "recall", "checkpoint", "close"} {
		if !ids[want] {
			t.Errorf("discovery misses operation %q: %v", want, ids)
		}
	}
}

func TestInputNeeded(t *testing.T) {
	base := newBase(t)
	response, code := base.aif(t,
		`{"aif":"0.2","operation":"learning","input":{"title":"Solo el título"}}`)
	if code != 2 || response["result"] != "input_needed" {
		t.Fatalf("input_needed: code=%d response=%v", code, response)
	}
	missing, _ := response["missing"].([]any)
	found := false
	for _, entry := range missing {
		if entry == "body" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing body: %v", response["missing"])
	}
}

func TestInvalid(t *testing.T) {
	base := newBase(t)
	response, code := base.aif(t,
		`{"aif":"0.2","operation":"learning","input":{"title":"T","body":"B","confidence":"quizá"}}`)
	if code != 2 || response["result"] != "invalid" {
		t.Fatalf("invalid: code=%d response=%v", code, response)
	}
	invalid, ok := response["invalid"].(map[string]any)
	if !ok || invalid["confidence"] == nil {
		t.Errorf("invalid confidence: %v", response["invalid"])
	}
}

func TestExecutedAndConsolidated(t *testing.T) {
	base := newBase(t)
	if _, stderr, code := base.run(t, "", "start", "--session", "aif-exec"); code != 0 {
		t.Fatalf("start: code=%d stderr=%s", code, stderr)
	}
	request := `{"aif":"0.2","operation":"learning","input":{` +
		`"session":"aif-exec","title":"El parser exige UTC",` +
		`"body":"Las fechas sin zona se leen como UTC."}}`
	response, code := base.aif(t, request)
	if code != 0 || response["result"] != "executed" {
		t.Fatalf("executed: code=%d response=%v", code, response)
	}
	outcome := response["outcome"].(map[string]any)
	if outcome["result"] != "created" || outcome["session"] != "aif-exec" {
		t.Errorf("created outcome: %v", outcome)
	}
	response, code = base.aif(t, request)
	if code != 0 || response["result"] != "executed" {
		t.Fatalf("consolidated: code=%d response=%v", code, response)
	}
	if result := response["outcome"].(map[string]any)["result"]; result != "consolidated" {
		t.Errorf("consolidated outcome: %v", result)
	}
	if got := base.count(t, "SELECT count(*) FROM observations"); got != 1 {
		t.Errorf("observation rows: got %d, want 1", got)
	}
}

func TestDeniedAfterClose(t *testing.T) {
	base := newBase(t)
	if _, stderr, code := base.run(t, "", "start", "--session", "aif-denied"); code != 0 {
		t.Fatalf("start: code=%d stderr=%s", code, stderr)
	}
	closed, code := base.aif(t,
		`{"aif":"0.2","operation":"close","input":{"session":"aif-denied","summary":"ciclo cerrado"}}`)
	if code != 0 || closed["result"] != "executed" {
		t.Fatalf("close: code=%d response=%v", code, closed)
	}
	response, code := base.aif(t,
		`{"aif":"0.2","operation":"learning","input":{"session":"aif-denied","title":"T","body":"B"}}`)
	if code != 4 || response["result"] != "denied" {
		t.Fatalf("denied: code=%d response=%v", code, response)
	}
	if reason, _ := response["reason"].(string); reason == "" {
		t.Errorf("denied without reason: %v", response)
	}
}

func TestMissingSessionFailsWithUsage(t *testing.T) {
	base := newBase(t)
	base.env = withoutEnv(base.env, "OPENCODE_SESSION_ID")
	response, code := base.aif(t,
		`{"aif":"0.2","operation":"learning","input":{"title":"T","body":"B"}}`)
	if code != 1 || response["result"] != "failed" {
		t.Fatalf("missing session: code=%d response=%v", code, response)
	}
	if response["code"] != "E_USAGE" {
		t.Errorf("missing session code: %v", response["code"])
	}
}

func TestCommitUnknownExitsWithItsCodeAndDoesNotRetry(t *testing.T) {
	base := newBase(t)
	if _, stderr, code := base.run(t, "", "start", "--session", "aif-unknown"); code != 0 {
		t.Fatalf("start: code=%d stderr=%s", code, stderr)
	}
	stubDir := t.TempDir()
	logPath := filepath.Join(stubDir, "calls.log")
	stub := filepath.Join(stubDir, "db-ai")
	script := `#!/usr/bin/env bash
printf '%s\n' "$1" >> ` + logPath + `
if [ "$1" = "find" ]; then
  case "$2" in
    */sessions)
      printf '{"version":1,"ok":true,"data":{"rows":[{"path":"%s","fields":{"id":{"kind":"text","data":"aif-unknown"},"goal":{"kind":"text","data":"objetivo"},"status":{"kind":"text","data":"open"},"summary":{"kind":"null","data":""}},"etag":"sha256:x"}]},"meta":{}}\n' "$2"
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
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	base.env = withEnv(base.env, "PATH", stubDir+":"+os.Getenv("PATH"))
	response, code := base.aif(t,
		`{"aif":"0.2","operation":"checkpoint","input":{"session":"aif-unknown","state":"s","next_steps":"n"}}`)
	if code != 1 || response["result"] != "failed" {
		t.Fatalf("commit unknown: code=%d response=%v", code, response)
	}
	if response["code"] != "E_COMMIT_UNKNOWN" {
		t.Errorf("commit unknown code: %v", response["code"])
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
		t.Errorf("batch invocations: got %d, want 1", batches)
	}
}

func withoutEnv(env []string, name string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, name+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}

func withEnv(env []string, name, value string) []string {
	return append(withoutEnv(env, name), name+"="+value)
}
