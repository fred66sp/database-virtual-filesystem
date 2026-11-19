package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

// envelopeView is the shape a consumer parses; the tests check it independently
// of the production types.
type envelopeView struct {
	Version int  `json:"version"`
	OK      bool `json:"ok"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "db-ai-config")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRunGolden(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantFile string
		wantJSON bool
	}{
		{"help root", []string{"--help"}, 0, "help_root.golden", false},
		{"help command", []string{"ls", "--help"}, 0, "help_ls.golden", false},
		{"help command flags", []string{"mount", "--help"}, 0, "help_mount.golden", false},
		{"missing command", nil, 2, "error_missing_command.golden", true},
		{"missing argument", []string{"cat"}, 2, "error_missing_argument.golden", true},
		{"unknown command", []string{"bogus"}, 2, "error_unknown_command.golden", true},
		{"unknown flag", []string{"ls", "/work", "--bogus"}, 2, "error_unknown_flag.golden", true},
		{"unknown flag before command", []string{"--bogus", "ls"}, 2, "error_unknown_flag.golden", true},
		{"relative path", []string{"ls", "work"}, 2, "error_relative_path.golden", true},
		{"nul path", []string{"cat", "/work/tasks/\x00"}, 2, "error_nul_path.golden", true},
		{"non-canonical escape", []string{"cat", "/work/tasks/a%2fb"}, 2, "error_noncanonical_escape.golden", true},
		{"empty segment", []string{"cat", "/work//tasks"}, 2, "error_empty_segment.golden", true},
		{"unsupported batch", []string{"batch", "--data", "-"}, 8, "error_unsupported_batch.golden", true},
		{"touch without mount", []string{"touch", "/work/tasks", "--data", "{}"}, 3, "error_find_no_mount.golden", true},
		{"find without mount", []string{"find", "/work/tasks"}, 3, "error_find_no_mount.golden", true},
		{"json flag accepted", []string{"--json", "find", "/work/tasks"}, 3, "error_find_no_mount.golden", true},
		{"rm without apply", []string{"rm", "/work/tasks/TK-001", "--if-match", "sha256:abc"}, 2, "error_missing_apply.golden", true},
		{"dry-run with apply", []string{"rm", "/work/tasks/TK-001", "--if-match", "sha256:abc", "--apply", "--dry-run"}, 2, "error_dry_run_apply.golden", true},
		{"limit too high", []string{"ls", "/work", "--limit", "1001"}, 8, "error_limit.golden", true},
		{"depth too high", []string{"tree", "/work", "--depth", "5"}, 8, "error_depth.golden", true},
		{"bad alias", []string{"mount", "/tmp/tasks.db", "/a.b"}, 2, "error_bad_alias.golden", true},
		{"cd without session", []string{"cd", "/work"}, 2, "error_cd_session.golden", true},
		{"unknown flag wins over help", []string{"ls", "--help", "--bogus"}, 2, "error_unknown_flag.golden", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			gotExit := Run(tc.args, &stdout, &stderr)
			if gotExit != tc.wantExit {
				t.Fatalf("Run(%q) exit = %d, want %d (stdout %q)", tc.args, gotExit, tc.wantExit, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			checkGolden(t, tc.wantFile, stdout.Bytes())
			if !tc.wantJSON {
				return
			}
			var env envelopeView
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
				t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout.String())
			}
			if env.Version != apiVersion || env.OK || env.Error == nil {
				t.Fatalf("envelope = %+v, want version %d, ok false and an error", env, apiVersion)
			}
			code := Code(env.Error.Code)
			if code == "" || env.Error.Message == "" {
				t.Fatalf("error = %+v, want code and message", env.Error)
			}
			if got := code.Exit(); got != gotExit {
				t.Errorf("envelope code %s maps to exit %d, but process exit was %d", code, got, gotExit)
			}
		})
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s\n got: %q\nwant: %q", name, got, want)
	}
}

func TestAllCommandsHaveHelp(t *testing.T) {
	for _, spec := range commands {
		t.Run(spec.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := Run([]string{spec.name, "--help"}, &stdout, &stderr); got != 0 {
				t.Fatalf("Run(%s --help) exit = %d, want 0 (stderr %q)", spec.name, got, stderr.String())
			}
			if !bytes.Contains(stdout.Bytes(), []byte("Usage:\n  db-ai "+spec.usage)) {
				t.Errorf("help for %s does not show its usage line", spec.name)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestParseDetails(t *testing.T) {
	inv, err := parse([]string{"ls"})
	if err != nil {
		t.Fatalf("parse(ls): %v", err)
	}
	if inv.path == nil || inv.path.Kind != vfs.KindRoot {
		t.Errorf("ls without PATH path = %+v, want root", inv.path)
	}
	if inv.timeout != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", inv.timeout)
	}

	if inv, err = parse([]string{"mount", "tasks.db", "/work"}); err != nil {
		t.Fatalf("parse(mount): %v", err)
	}
	if inv.mode != "ro" || inv.alias != "work" || inv.args[0] != "tasks.db" {
		t.Errorf("mount invocation = %+v, want mode ro and alias work", inv)
	}

	if inv, err = parse([]string{"export", "/work/tasks"}); err != nil {
		t.Fatalf("parse(export): %v", err)
	}
	if inv.format != "json" {
		t.Errorf("export default format = %q, want json", inv.format)
	}

	if inv, err = parse([]string{"find", "/work/tasks", "--where", " status=pending ", "--where", "id=1", "--limit=5"}); err != nil {
		t.Fatalf("parse(find): %v", err)
	}
	if len(inv.where) != 2 || inv.where[0] != "status=pending" || inv.limit != 5 {
		t.Errorf("find invocation = %+v, want trimmed where and limit 5", inv)
	}

	if inv, err = parse([]string{"grep", "/work/tasks", "--", "--literal"}); err != nil {
		t.Fatalf("parse(grep --): %v", err)
	}
	if len(inv.args) != 2 || inv.args[1] != "--literal" {
		t.Errorf("args after -- = %q, want the literal text", inv.args)
	}

	if inv, err = parse([]string{"ls", "tasks", "--session", "s1"}); err != nil {
		t.Fatalf("parse(ls relative): %v", err)
	}
	if !inv.pathRelative || inv.pathRaw != "tasks" || inv.path != nil {
		t.Errorf("relative path invocation = %+v, want a deferred resolution", inv)
	}

	if inv, err = parse([]string{"ls", "--session", "s1"}); err != nil {
		t.Fatalf("parse(ls with session): %v", err)
	}
	if !inv.pathFromCwd || inv.path != nil {
		t.Errorf("optional path invocation = %+v, want the cwd to stand in", inv)
	}

	if inv, err = parse([]string{"-h"}); err != nil || !inv.help || inv.spec != nil {
		t.Errorf("parse(-h) = %+v, %v; want root help", inv, err)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Code
	}{
		{"flag without value", []string{"ls", "--limit"}, CodeUsage},
		{"duplicate flag", []string{"ls", "--limit", "5", "--limit", "6"}, CodeUsage},
		{"repeated where with empty one", []string{"find", "/w", "--where", "a=1", "--where", " "}, CodeUsage},
		{"bool with value", []string{"ls", "--json=1"}, CodeUsage},
		{"unknown single dash flag", []string{"ls", "-x"}, CodeUsage},
		{"empty flag name", []string{"ls", "--=1"}, CodeUsage},
		{"timeout zero", []string{"ls", "/w", "--timeout", "0"}, CodeUsage},
		{"timeout high", []string{"ls", "/w", "--timeout", "61"}, CodeUsage},
		{"timeout not a number", []string{"ls", "/w", "--timeout", "5s"}, CodeUsage},
		{"limit zero", []string{"ls", "/w", "--limit", "0"}, CodeUsage},
		{"depth zero", []string{"tree", "/w", "--depth", "0"}, CodeUsage},
		{"mode invalid", []string{"mount", "tasks.db", "/w", "--mode", "rwx"}, CodeUsage},
		{"format invalid", []string{"export", "/w/t", "--format", "csv"}, CodeUsage},
		{"where empty", []string{"find", "/w", "--where", "  "}, CodeUsage},
		{"touch without data", []string{"touch", "/w/t"}, CodeUsage},
		{"write without if-match", []string{"write", "/w/t/1", "--data", "{}"}, CodeUsage},
		{"too many arguments", []string{"pwd", "extra"}, CodeUsage},
		{"path too deep", []string{"cat", "/w/t/1/field/extra"}, CodeUsage},
		{"alias not a mount point", []string{"mount", "tasks.db", "/w/t"}, CodeUsage},
		{"alias relative", []string{"umount", "w"}, CodeUsage},
		{"apply on ls", []string{"ls", "/w", "--apply"}, CodeUsage},
		{"dry-run on ls", []string{"ls", "/w", "--dry-run"}, CodeUsage},
		{"invalid session id", []string{"cd", "/w", "--session", "bad/id"}, CodeUsage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(tc.args)
			if err == nil {
				t.Fatalf("parse(%q) = nil error, want %s", tc.args, tc.want)
			}
			if got := CodedError(err).Code; got != tc.want {
				t.Errorf("parse(%q) code = %s (%v), want %s", tc.args, got, err, tc.want)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	if _, err := parse([]string{"ls", "/w", "--limit", "1000"}); err != nil {
		t.Errorf("parse(--limit 1000) error: %v", err)
	}
	if _, err := parse([]string{"tree", "/w", "--depth", "4"}); err != nil {
		t.Errorf("parse(--depth 4) error: %v", err)
	}
	for _, value := range []string{"1", "60"} {
		if _, err := parse([]string{"ls", "/w", "--timeout", value}); err != nil {
			t.Errorf("parse(--timeout %s) error: %v", value, err)
		}
	}
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		code Code
		exit int
	}{
		{CodeOK, 0},
		{CodeUsage, 2},
		{CodeNotFound, 3},
		{CodeStaleSession, 3},
		{CodeDenied, 4},
		{CodeConflict, 5},
		{CodeConstraint, 6},
		{CodeBusy, 7},
		{CodeTimeout, 7},
		{CodeUnsupported, 8},
		{CodeLimit, 8},
		{CodeInternal, 9},
		{CodeCommitUnknown, 10},
	}
	for _, tc := range tests {
		if got := tc.code.Exit(); got != tc.exit {
			t.Errorf("%s.Exit() = %d, want %d", tc.code, got, tc.exit)
		}
	}
	if got := Code("E_UNKNOWN").Exit(); got != 9 {
		t.Errorf("unknown code exit = %d, want 9 (internal)", got)
	}
}

func TestCodedError(t *testing.T) {
	base := errors.New("base")
	coded := &Error{Code: CodeConflict, Message: "changed elsewhere", Err: base}
	if got := CodedError(fmt.Errorf("write: %w", coded)); got != coded {
		t.Errorf("CodedError did not preserve the coded error: %+v", got)
	}
	if !errors.Is(coded, base) {
		t.Error("coded error lost its cause")
	}

	_, vfsErr := vfs.Parse("/work/a%2fb")
	if got := CodedError(fmt.Errorf("path: %w", vfsErr)); got.Code != CodeUsage {
		t.Errorf("vfs error code = %s, want %s", got.Code, CodeUsage)
	}
	if got := CodedError(fmt.Errorf("deadline: %w", context.DeadlineExceeded)); got.Code != CodeTimeout || got.Code.Exit() != 7 {
		t.Errorf("deadline code = %s, want E_TIMEOUT", got.Code)
	}
	if got := CodedError(os.ErrNotExist); got.Code != CodeNotFound {
		t.Errorf("os.ErrNotExist code = %s, want E_NOT_FOUND", got.Code)
	}
	if got := CodedError(context.Canceled); got.Code != CodeInternal {
		t.Errorf("context.Canceled code = %s, want E_INTERNAL", got.Code)
	}
	internal := CodedError(errors.New("boom"))
	if internal.Code != CodeInternal || internal.Message != "internal error" {
		t.Errorf("unclassified error = %+v, want generic E_INTERNAL", internal)
	}
	if internal.Err == nil {
		t.Error("internal error lost its cause")
	}
}

func TestSuccessEnvelope(t *testing.T) {
	data := map[string]any{
		"path":   "/work/tasks/TK-001",
		"fields": map[string]storage.Value{"status": {Kind: "text", Data: "pending"}},
		"etag":   "sha256:abc",
	}
	got, err := json.Marshal(successEnvelope(data, meta{RequestID: "req-1"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"version":1,"ok":true,"data":{"etag":"sha256:abc","fields":{"status":{"kind":"text","data":"pending"}},"path":"/work/tasks/TK-001"},"meta":{"truncated":false,"request_id":"req-1"}}`
	if string(got) != want {
		t.Errorf("envelope = %s\nwant      = %s", got, want)
	}
}

func TestRequestID(t *testing.T) {
	if got, err := requestID("req-1"); err != nil || got != "req-1" {
		t.Errorf("requestID(req-1) = %q, %v", got, err)
	}
	got, err := requestID("")
	if err != nil {
		t.Fatalf("requestID: %v", err)
	}
	if len(got) != 32 {
		t.Errorf("generated request ID %q has length %d, want 32 hex chars", got, len(got))
	}
	other, err := requestID("")
	if err != nil || other == got {
		t.Errorf("generated request IDs are not unique: %q, %q (%v)", got, other, err)
	}
}

func TestBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "db-ai")
	build := exec.Command("go", "build", "-o", bin, "./cmd/db-ai")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantFile string
	}{
		{"help", []string{"--help"}, 0, "help_root.golden"},
		{"usage error", []string{"cat", "/work/tasks/a%2fb"}, 2, "error_noncanonical_escape.golden"},
		{"unsupported", []string{"batch", "--data", "-"}, 8, "error_unsupported_batch.golden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				if tc.wantExit != 0 {
					t.Fatalf("exit = 0, want %d", tc.wantExit)
				}
			case errors.As(err, &exitErr):
				if got := exitErr.ExitCode(); got != tc.wantExit {
					t.Fatalf("exit = %d, want %d (stderr %q)", got, tc.wantExit, stderr.String())
				}
			default:
				t.Fatalf("run: %v", err)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			checkGolden(t, tc.wantFile, stdout.Bytes())
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func FuzzParseArgs(f *testing.F) {
	for _, seed := range []struct{ a, b, c string }{
		{"ls", "/work", "--limit"},
		{"cat", "/work/tasks/TK-001", ""},
		{"--help", "", ""},
		{"rm", "/work/tasks/TK-001", "--apply"},
		{"mv", "a", "--if-match=x"},
		{"--", "-x", "/"},
		{"grep", "/w/t", "--"},
		{"mount", "tasks.db", "/a.b"},
	} {
		f.Add(seed.a, seed.b, seed.c)
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		args := []string{a, b, c}
		var stdout, stderr bytes.Buffer
		code := Run(args, &stdout, &stderr)
		if !knownExit(code) {
			t.Fatalf("Run(%q) exit = %d, which is not a documented exit status", args, code)
		}
		if code == 0 {
			if stdout.Len() == 0 {
				t.Fatalf("Run(%q) succeeded with empty stdout", args)
			}
			return
		}
		var env envelopeView
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
			t.Fatalf("Run(%q) stdout is not one JSON object: %v (%q)", args, err, stdout.String())
		}
		if env.Version != apiVersion || env.OK || env.Error == nil || env.Error.Code == "" {
			t.Fatalf("Run(%q) envelope = %+v, want a versioned error", args, env)
		}
		if got := Code(env.Error.Code).Exit(); got != code {
			t.Fatalf("Run(%q) envelope code %s maps to %d, process exit %d", args, env.Error.Code, got, code)
		}
	})
}

func knownExit(code int) bool {
	switch code {
	case 0, 2, 3, 4, 5, 6, 7, 8, 9, 10:
		return true
	}
	return false
}
