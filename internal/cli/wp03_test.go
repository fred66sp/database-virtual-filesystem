package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dbvfs/internal/state"
)

type findJSON struct {
	Path string    `json:"path"`
	Rows []rowJSON `json:"rows"`
}

type grepJSON struct {
	Path    string `json:"path"`
	Entries []struct {
		Path     string `json:"path"`
		Row      string `json:"row"`
		Column   string `json:"column"`
		Position int    `json:"position"`
	} `json:"entries"`
}

type sessionJSON struct {
	Session string `json:"session"`
	Path    string `json:"path"`
}

// setupAlpha mounts the shared fixture against isolated config and state dirs.
func setupAlpha(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	alpha := newFixture(t, alphaSchema)
	if _, code := runCLI(t, "mount", alpha, "/alpha"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	return alpha
}

func TestWP03FindFiltersAndPagination(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "find", "/alpha/tasks", "--where", "id>=2")
	found := decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks", "--where", "id>=2"))
	if len(found.Rows) != 1 || found.Rows[0].Path != "/alpha/tasks/2" {
		t.Errorf("find id>=2 = %+v, want row 2", found)
	}

	env, code = runCLI(t, "find", "/alpha/tasks", "--where", "note is-null")
	found = decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks", "--where", "note is-null"))
	if len(found.Rows) != 1 || found.Rows[0].Path != "/alpha/tasks/1" {
		t.Errorf("find note is-null = %+v, want row 1", found)
	}

	env, code = runCLI(t, "find", "/alpha/tasks", "--where", "title=uno ñ / 100% ~ ~e")
	found = decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks"))
	if len(found.Rows) != 1 || found.Rows[0].Path != "/alpha/tasks/1" {
		t.Errorf("find text value = %+v, want row 1", found)
	}

	env, code = runCLI(t, "find", "/alpha/tasks", "--limit", "1")
	page := decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks", "--limit", "1"))
	if len(page.Rows) != 1 || !env.Meta.Truncated || env.Meta.NextCursor == "" {
		t.Fatalf("first page = %+v, truncated %v, cursor %q; want one row and a cursor", page, env.Meta.Truncated, env.Meta.NextCursor)
	}
	cursor := env.Meta.NextCursor

	env, code = runCLI(t, "find", "/alpha/tasks", "--limit", "1", "--cursor", cursor)
	page = decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/tasks", "--cursor", cursor))
	if len(page.Rows) != 1 || page.Rows[0].Path != "/alpha/tasks/2" {
		t.Errorf("second page = %+v, want row 2", page)
	}

	env, code = runCLI(t, "find", "/alpha/tasks", "--limit", "1", "--cursor", cursor+"x")
	mustCode(t, env, code, CodeUsage, "tampered cursor")
	env, code = runCLI(t, "find", "/alpha/tasks", "--limit", "1", "--where", "id>=2", "--cursor", cursor)
	mustCode(t, env, code, CodeUsage, "cursor from another filter")

	env, code = runCLI(t, "find", "/alpha/nopk", "--limit", "1")
	plain := decode[findJSON](t, mustOK(t, env, code, "find", "/alpha/nopk", "--limit", "1"))
	if len(plain.Rows) != 1 || !env.Meta.Truncated || env.Meta.NextCursor != "" {
		t.Errorf("non-navigable page = %+v, truncated %v, cursor %q; want a truncated first page and no cursor", plain, env.Meta.Truncated, env.Meta.NextCursor)
	}
	env, code = runCLI(t, "find", "/alpha/nopk", "--cursor", "abc")
	mustCode(t, env, code, CodeUnsupported, "cursor on a non-navigable table")

	for _, args := range [][]string{
		{"find", "/alpha/tasks", "--where", "id"},
		{"find", "/alpha/tasks", "--where", "bogus=1"},
		{"find", "/alpha"},
		{"find", "/alpha/tasks/1"},
	} {
		env, code = runCLI(t, args...)
		if code == 0 {
			t.Errorf("Run(%q) succeeded, want an error", args)
		}
	}
}

func TestWP03GrepAndExport(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "grep", "/alpha/tasks", "dos")
	found := decode[grepJSON](t, mustOK(t, env, code, "grep", "/alpha/tasks", "dos"))
	if len(found.Entries) != 1 {
		t.Fatalf("grep dos = %+v, want one entry", found)
	}
	entry := found.Entries[0]
	if entry.Path != "/alpha/tasks/2/title" || entry.Row != "/alpha/tasks/2" || entry.Column != "title" || entry.Position != 1 {
		t.Errorf("grep entry = %+v, want /alpha/tasks/2/title title at position 1", entry)
	}

	env, code = runCLI(t, "grep", "/alpha/tasks", "ñ")
	found = decode[grepJSON](t, mustOK(t, env, code, "grep", "/alpha/tasks", "ñ"))
	if len(found.Entries) != 1 || found.Entries[0].Position != 5 {
		t.Errorf("grep ñ = %+v, want one match at character 5", found)
	}

	env, code = runCLI(t, "grep", "/alpha/tasks", "")
	mustCode(t, env, code, CodeUsage, "grep with an empty literal")

	env, code = runCLI(t, "export", "/alpha/tasks", "--where", "id=2")
	exported := decode[findJSON](t, mustOK(t, env, code, "export", "/alpha/tasks", "--where", "id=2"))
	if len(exported.Rows) != 1 || exported.Rows[0].Path != "/alpha/tasks/2" {
		t.Errorf("export json = %+v, want row 2", exported)
	}

	lines, code := runLines(t, "export", "/alpha/tasks", "--limit", "1", "--format", "jsonl")
	if code != 0 {
		t.Fatalf("export jsonl exit = %d", code)
	}
	if len(lines) != 2 {
		t.Fatalf("export jsonl produced %d lines, want one row and one meta trailer: %q", len(lines), lines)
	}
	row := decode[rowJSON](t, json.RawMessage(lines[0]))
	if row.Path != "/alpha/tasks/1" {
		t.Errorf("jsonl first row = %+v, want row 1", row)
	}
	var trailer struct {
		Meta struct {
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &trailer); err != nil {
		t.Fatalf("jsonl trailer is not JSON: %v (%q)", err, lines[1])
	}
	if !trailer.Meta.Truncated || trailer.Meta.NextCursor == "" {
		t.Errorf("jsonl trailer = %+v, want truncation and a cursor", trailer.Meta)
	}
}

func TestWP03Sessions(t *testing.T) {
	setupAlpha(t)

	env, code := runCLI(t, "cd", "/alpha/tasks", "--session", "s1")
	info := decode[sessionJSON](t, mustOK(t, env, code, "cd", "/alpha/tasks", "--session", "s1"))
	if info.Session != "s1" || info.Path != "/alpha/tasks" {
		t.Errorf("cd = %+v, want s1 at /alpha/tasks", info)
	}
	env, code = runCLI(t, "cd", "/alpha", "--session", "s2")
	info = decode[sessionJSON](t, mustOK(t, env, code, "cd", "/alpha", "--session", "s2"))
	if info.Path != "/alpha" {
		t.Errorf("cd s2 = %+v, want /alpha", info)
	}

	for session, want := range map[string]string{"s1": "/alpha/tasks", "s2": "/alpha"} {
		env, code = runCLI(t, "pwd", "--session", session)
		info = decode[sessionJSON](t, mustOK(t, env, code, "pwd", "--session", session))
		if info.Path != want {
			t.Errorf("pwd %s = %q, want %q", session, info.Path, want)
		}
	}

	env, code = runCLI(t, "cd", "tasks", "--session", "s2")
	info = decode[sessionJSON](t, mustOK(t, env, code, "cd", "tasks", "--session", "s2"))
	if info.Path != "/alpha/tasks" {
		t.Errorf("relative cd = %+v, want /alpha/tasks", info)
	}

	env, code = runCLI(t, "ls", "--session", "s1")
	entries := decode[listJSON](t, mustOK(t, env, code, "ls", "--session", "s1"))
	if entries.Path != "/alpha/tasks" || len(entries.Entries) != 2 {
		t.Errorf("ls with session = %+v, want the cwd rows", entries)
	}

	env, code = runCLI(t, "cd", "/alpha", "--session", "s3")
	mustOK(t, env, code, "cd", "/alpha", "--session", "s3")
	env, code = runCLI(t, "find", "tasks", "--where", "id=2", "--session", "s3")
	found := decode[findJSON](t, mustOK(t, env, code, "find", "tasks", "--session", "s3"))
	if len(found.Rows) != 1 || found.Rows[0].Path != "/alpha/tasks/2" {
		t.Errorf("find with a relative path = %+v, want row 2", found)
	}

	env, code = runCLI(t, "cd", "/alpha/tasks/1/title", "--session", "s1")
	mustCode(t, env, code, CodeUsage, "cd into a field")
	env, code = runCLI(t, "cd", "/alpha/tasks/999", "--session", "s1")
	mustCode(t, env, code, CodeNotFound, "cd into a missing row")
	env, code = runCLI(t, "cd", "/alpha/nope", "--session", "s1")
	mustCode(t, env, code, CodeNotFound, "cd into a missing table")
	env, code = runCLI(t, "pwd", "--session", "absent")
	mustCode(t, env, code, CodeStaleSession, "pwd of a missing session")
	env, code = runCLI(t, "cd", "/alpha")
	mustCode(t, env, code, CodeUsage, "cd without a session")

	sessions, err := state.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(sessions, "s1.json"))
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("session permissions = %o, want 600", got)
	}

	env, code = runCLI(t, "umount", "/alpha")
	mustOK(t, env, code, "umount", "/alpha")
	env, code = runCLI(t, "pwd", "--session", "s1")
	mustCode(t, env, code, CodeStaleSession, "pwd after umount")
}

func TestWP03SessionsParallel(t *testing.T) {
	setupAlpha(t)
	type result struct {
		env  envelopeJSON
		code int
	}
	run := func(session, path string) result {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"cd", path, "--session", session}, &stdout, &stderr)
		var env envelopeJSON
		_ = json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env)
		return result{env: env, code: code}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var results [2]result
	targets := [2][2]string{{"p1", "/alpha/tasks"}, {"p2", "/alpha/authors"}}
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = run(targets[i][0], targets[i][1])
		}(i)
	}
	close(start)
	wg.Wait()

	for i, want := range targets {
		if results[i].code != 0 || !results[i].env.OK {
			t.Fatalf("parallel cd %s = exit %d, %+v", want[0], results[i].code, results[i].env)
		}
	}
	for _, want := range targets {
		env, code := runCLI(t, "pwd", "--session", want[0])
		info := decode[sessionJSON](t, mustOK(t, env, code, "pwd", "--session", want[0]))
		if info.Path != want[1] {
			t.Errorf("pwd %s after parallel cd = %q, want %q", want[0], info.Path, want[1])
		}
	}
}

// TestWP03ReadPolicyDeniesFieldBypass is the FR-13 read-side criterion: a
// filter on a prohibited field is E_DENIED, not a way around the policy.
func TestWP03ReadPolicyDeniesFieldBypass(t *testing.T) {
	setupAlpha(t)
	dir, err := state.Dir()
	if err != nil {
		t.Fatalf("state.Dir: %v", err)
	}
	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = []state.Policy{
			{Ops: []string{"cat"}, Prefix: "/alpha/tasks/1/title"},
			{Ops: []string{"ls"}, Prefix: "/alpha"},
		}
		cfg.Mounts["alpha"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}

	env, code := runCLI(t, "find", "/alpha/tasks", "--where", "note=x")
	mustCode(t, env, code, CodeDenied, "find on a prohibited column")
	// The denial happens before the catalog is available, so it names the
	// denied path rather than the filter's column.
	if !strings.Contains(env.Error.Message, "/alpha/tasks") {
		t.Errorf("denial message = %q, want it to name the denied path", env.Error.Message)
	}

	env, code = runCLI(t, "find", "/alpha/tasks")
	mustCode(t, env, code, CodeDenied, "find without a table grant")

	env, code = runCLI(t, "cat", "/alpha/tasks/1/title")
	if got := decode[valueJSON](t, mustOK(t, env, code, "cat", "/alpha/tasks/1/title")); got.Kind != "text" {
		t.Errorf("cat granted field = %+v, want text", got)
	}
	env, code = runCLI(t, "cat", "/alpha/tasks/1/note")
	mustCode(t, env, code, CodeDenied, "cat of a prohibited field")
	env, code = runCLI(t, "cat", "/alpha/tasks/1")
	mustCode(t, env, code, CodeDenied, "cat of a row with only a field grant")
	env, code = runCLI(t, "cd", "/alpha/tasks", "--session", "p1")
	mustOK(t, env, code, "cd with an ls grant")

	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = append(m.Allow, state.Policy{Ops: []string{"find"}, Prefix: "/alpha/tasks"})
		cfg.Mounts["alpha"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("grant find: %v", err)
	}
	env, code = runCLI(t, "find", "/alpha/tasks", "--where", "note=x")
	mustOK(t, env, code, "find after an explicit table grant")
}

// TestWP03OutputLimit proves the 16 MiB cap is E_LIMIT and never a partial
// success (NFR-05).
func TestWP03OutputLimit(t *testing.T) {
	err := writeJSON(io.Discard, successEnvelope(strings.Repeat("x", maxOutputBytes), meta{}))
	if err == nil {
		t.Fatal("writeJSON of an oversized output succeeded")
	}
	if got := CodedError(err).Code; got != CodeLimit {
		t.Errorf("oversized output code = %s, want %s", got, CodeLimit)
	}
}

func runLines(t *testing.T, args ...string) ([]string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("Run(%q) stderr = %q, want empty", args, stderr.String())
	}
	trimmed := strings.TrimSpace(stdout.String())
	if trimmed == "" {
		return nil, code
	}
	return strings.Split(trimmed, "\n"), code
}

// TestWP03BinaryAcceptance is the observable criterion of tk-10 on an existing
// SQLite file: M1 reads, two parallel sessions without contamination, a policy
// bypass denied with E_DENIED, and the database bytes untouched.
func TestWP03BinaryAcceptance(t *testing.T) {
	bin := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "config")
	stateDir := filepath.Join(t.TempDir(), "state")
	alpha := newFixture(t, alphaSchema)
	before := sha256File(t, alpha)

	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir, "XDG_STATE_HOME="+stateDir)
	run := func(args ...string) (envelopeJSON, int) {
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
		var env envelopeJSON
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
			t.Fatalf("binary %q stdout is not JSON: %v (%q)", args, err, stdout.String())
		}
		return env, code
	}

	envJSON, code := run("mount", alpha, "/alpha")
	mustOK(t, envJSON, code, "mount", alpha, "/alpha")

	// Two sessions in two processes, started together.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	specs := [][3]string{
		{"cd", "/alpha/tasks", "par1"},
		{"cd", "/alpha", "par2"},
	}
	start := make(chan struct{})
	for i, spec := range specs {
		wg.Add(1)
		go func(i int, spec [3]string) {
			defer wg.Done()
			<-start
			cmd := exec.Command(bin, spec[0], spec[1], "--session", spec[2])
			cmd.Env = env
			if err := cmd.Run(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					codes[i] = exitErr.ExitCode()
					return
				}
				t.Errorf("parallel cd: %v", err)
				codes[i] = -1
			}
		}(i, spec)
	}
	close(start)
	wg.Wait()
	if codes[0] != 0 || codes[1] != 0 {
		t.Fatalf("parallel cd exits = %v, want both 0", codes)
	}
	for _, want := range [][2]string{{"par1", "/alpha/tasks"}, {"par2", "/alpha"}} {
		envJSON, code = run("pwd", "--session", want[0])
		info := decode[sessionJSON](t, mustOK(t, envJSON, code, "pwd", "--session", want[0]))
		if info.Path != want[1] {
			t.Errorf("pwd %s = %q, want %q", want[0], info.Path, want[1])
		}
	}

	envJSON, code = run("find", "/alpha/tasks", "--where", "note is-null", "--limit", "1")
	page := decode[findJSON](t, mustOK(t, envJSON, code, "find"))
	if len(page.Rows) != 1 || page.Rows[0].Path != "/alpha/tasks/1" {
		t.Errorf("binary find = %+v, want row 1", page)
	}

	envJSON, code = run("grep", "/alpha/tasks", "dos")
	grep := decode[grepJSON](t, mustOK(t, envJSON, code, "grep"))
	if len(grep.Entries) != 1 || grep.Entries[0].Path != "/alpha/tasks/2/title" {
		t.Errorf("binary grep = %+v", grep)
	}

	envJSON, code = run("export", "/alpha/tasks", "--where", "id=1")
	exported := decode[findJSON](t, mustOK(t, envJSON, code, "export"))
	if len(exported.Rows) != 1 {
		t.Errorf("binary export = %+v, want one row", exported)
	}

	// A field-level policy makes the table query, and therefore a filter used
	// to probe a hidden column, E_DENIED.
	dir := filepath.Join(configDir, "db-ai")
	if err := state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = []state.Policy{{Ops: []string{"cat"}, Prefix: "/alpha/tasks/1/title"}}
		cfg.Mounts["alpha"] = m
		return nil
	}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	envJSON, code = run("find", "/alpha/tasks", "--where", "note=x")
	mustCode(t, envJSON, code, CodeDenied, "binary policy bypass")

	if after := sha256File(t, alpha); after != before {
		t.Errorf("database sha256 changed after reads: %s -> %s", before, after)
	}
}
