package cli

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

const alphaSchema = `
CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO authors VALUES (1, 'Ada');
CREATE TABLE tasks (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	note TEXT,
	payload BLOB,
	big INTEGER,
	ratio REAL,
	author_id INTEGER REFERENCES authors(id)
);
INSERT INTO tasks VALUES (1, 'uno ñ / 100% ~ ~e', NULL, x'00ff', 9223372036854775807, 1.25, 1);
INSERT INTO tasks VALUES (2, 'dos', 'note', NULL, 42, 2.5, 1);
CREATE TABLE nopk (x TEXT);
INSERT INTO nopk VALUES ('a'), ('b'), ('c');
CREATE TABLE tags (
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	v TEXT,
	PRIMARY KEY (name, kind)
);
INSERT INTO tags VALUES ('a', 'b', 'v1');
CREATE VIEW task_view AS SELECT id FROM tasks;
CREATE TRIGGER tasks_audit AFTER UPDATE ON tasks BEGIN SELECT 1; END;
`

const betaSchema = `
CREATE TABLE users (id TEXT NOT NULL PRIMARY KEY, email TEXT);
INSERT INTO users VALUES ('u1', 'ada@example.test');
`

type valueJSON struct {
	Kind string `json:"kind"`
	Data string `json:"data"`
}

type rowJSON struct {
	Path   string               `json:"path"`
	Fields map[string]valueJSON `json:"fields"`
	ETag   string               `json:"etag"`
}

type entryJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
}

type listJSON struct {
	Path    string      `json:"path"`
	Entries []entryJSON `json:"entries"`
}

type statJSON struct {
	Type      string               `json:"type"`
	Path      string               `json:"path"`
	Columns   []string             `json:"columns"`
	Keys      map[string]valueJSON `json:"keys"`
	Navigable *bool                `json:"navigable"`
}

type describeJSON struct {
	Table      string   `json:"table"`
	PrimaryKey []string `json:"primary_key"`
	Incoming   []struct {
		Table string `json:"table"`
	} `json:"incoming_references"`
	Triggers     []string `json:"triggers"`
	Capabilities struct {
		Readable  bool `json:"readable"`
		Writable  bool `json:"writable"`
		Navigable bool `json:"navigable"`
	} `json:"capabilities"`
	Limitations []string `json:"limitations"`
}

type mountsJSON struct {
	Mounts []struct {
		Alias string `json:"alias"`
		File  string `json:"file"`
		Mode  string `json:"mode"`
	} `json:"mounts"`
}

type envelopeJSON struct {
	Version int             `json:"version"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Meta    struct {
		Truncated  bool   `json:"truncated"`
		NextCursor string `json:"next_cursor"`
		RequestID  string `json:"request_id"`
	} `json:"meta"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newFixture(t *testing.T, schema string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	return path
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// runCLI executes one invocation in-process and requires the contract's single
// JSON object on stdout with a clean stderr.
func runCLI(t *testing.T, args ...string) (envelopeJSON, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("Run(%q) stderr = %q, want empty", args, stderr.String())
	}
	var env envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("Run(%q) stdout is not one JSON object: %v (%q)", args, err, stdout.String())
	}
	if env.Version != apiVersion {
		t.Errorf("Run(%q) version = %d, want %d", args, env.Version, apiVersion)
	}
	if !env.OK && env.Error == nil {
		t.Fatalf("Run(%q) failed envelope without error", args)
	}
	return env, code
}

func mustOK(t *testing.T, env envelopeJSON, code int, args ...string) json.RawMessage {
	t.Helper()
	if code != 0 || !env.OK {
		t.Fatalf("Run(%q) = exit %d, envelope %+v; want success", args, code, env)
	}
	return env.Data
}

func mustCode(t *testing.T, env envelopeJSON, code int, want Code, args ...string) {
	t.Helper()
	if code != want.Exit() || env.OK || env.Error == nil || env.Error.Code != string(want) {
		t.Fatalf("Run(%q) = exit %d, envelope %+v; want %s (exit %d)", args, code, env, want, want.Exit())
	}
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestWP02MountRegistryAndErrors(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", configDir)
	alpha := newFixture(t, alphaSchema)

	env, code := runCLI(t, "mount", alpha, "/alpha")
	info := decode[struct {
		Alias string `json:"alias"`
		File  string `json:"file"`
		Mode  string `json:"mode"`
	}](t, mustOK(t, env, code, "mount", alpha, "/alpha"))
	canonicalAlpha, err := filepath.EvalSymlinks(alpha)
	if err != nil {
		t.Fatalf("canonical fixture path: %v", err)
	}
	if info.Alias != "alpha" || info.Mode != "ro" || info.File != canonicalAlpha {
		t.Errorf("mount = %+v, want the canonical file and mode ro", info)
	}

	configPath := filepath.Join(configDir, "db-ai", "config.json")
	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("config permissions = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("config directory permissions = %o, want 700", got)
	}

	env, code = runCLI(t, "mounts")
	mounts := decode[mountsJSON](t, mustOK(t, env, code, "mounts"))
	if len(mounts.Mounts) != 1 || mounts.Mounts[0].Alias != "alpha" {
		t.Errorf("mounts = %+v, want one alpha mount", mounts)
	}

	env, code = runCLI(t, "mount", alpha, "/alpha")
	mustCode(t, env, code, CodeConflict, "mount", alpha, "/alpha")

	env, code = runCLI(t, "mount", alpha, "/rw", "--mode", "rw")
	mustCode(t, env, code, CodeDenied, "mount", alpha, "/rw", "--mode", "rw")

	env, code = runCLI(t, "mounts")
	mounts = decode[mountsJSON](t, mustOK(t, env, code, "mounts"))
	if len(mounts.Mounts) != 1 {
		t.Errorf("refused rw mount was registered: %+v", mounts)
	}

	missing := filepath.Join(t.TempDir(), "absent.db")
	env, code = runCLI(t, "mount", missing, "/absent")
	mustCode(t, env, code, CodeNotFound, "mount", missing, "/absent")
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mount created %s (err %v), want no file", missing, err)
	}

	notSQLite := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(notSQLite, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	env, code = runCLI(t, "mount", notSQLite, "/plain")
	mustCode(t, env, code, CodeUsage, "mount", notSQLite, "/plain")

	env, code = runCLI(t, "ls", "/nowhere")
	mustCode(t, env, code, CodeNotFound, "ls", "/nowhere")

	env, code = runCLI(t, "umount", "/alpha")
	mustOK(t, env, code, "umount", "/alpha")
	if _, err := os.Stat(alpha); err != nil {
		t.Errorf("umount touched the database file: %v", err)
	}
	env, code = runCLI(t, "umount", "/alpha")
	mustCode(t, env, code, CodeNotFound, "umount", "/alpha")

	env, code = runCLI(t, "mounts")
	mounts = decode[mountsJSON](t, mustOK(t, env, code, "mounts"))
	if len(mounts.Mounts) != 0 {
		t.Errorf("mounts after umount = %+v, want empty", mounts)
	}
}

func TestWP02ReadShapes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	alpha := newFixture(t, alphaSchema)
	if _, code := runCLI(t, "mount", alpha, "/alpha"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}

	env, code := runCLI(t, "stat", "/")
	if stat := decode[statJSON](t, mustOK(t, env, code, "stat", "/")); stat.Type != "root" || stat.Path != "/" {
		t.Errorf("stat / = %+v, want root", stat)
	}
	env, code = runCLI(t, "stat", "/alpha")
	if stat := decode[statJSON](t, mustOK(t, env, code, "stat", "/alpha")); stat.Type != "mount" || stat.Path != "/alpha" {
		t.Errorf("stat mount = %+v, want the mount path", stat)
	}

	env, code = runCLI(t, "ls")
	root := decode[listJSON](t, mustOK(t, env, code, "ls"))
	if len(root.Entries) != 1 || root.Entries[0] != (entryJSON{Name: "alpha", Type: "mount", Path: "/alpha"}) {
		t.Errorf("ls / = %+v, want the alpha mount", root)
	}

	env, code = runCLI(t, "ls", "/alpha")
	tables := decode[listJSON](t, mustOK(t, env, code, "ls", "/alpha"))
	wantTables := []string{"authors", "nopk", "tags", "tasks"}
	if len(tables.Entries) != len(wantTables) {
		t.Fatalf("ls /alpha = %+v, want tables %v", tables, wantTables)
	}
	for i, name := range wantTables {
		if tables.Entries[i].Name != name || tables.Entries[i].Type != "table" {
			t.Errorf("ls /alpha entry %d = %+v, want table %q", i, tables.Entries[i], name)
		}
	}

	env, code = runCLI(t, "ls", "/alpha/tasks")
	rows := decode[listJSON](t, mustOK(t, env, code, "ls", "/alpha/tasks"))
	if len(rows.Entries) != 2 || rows.Entries[0].Path != "/alpha/tasks/1" || rows.Entries[1].Path != "/alpha/tasks/2" {
		t.Errorf("ls /alpha/tasks = %+v, want rows 1 and 2 in key order", rows)
	}

	env, code = runCLI(t, "ls", "/alpha/tasks", "--limit", "1")
	rows = decode[listJSON](t, mustOK(t, env, code, "ls", "/alpha/tasks", "--limit", "1"))
	if len(rows.Entries) != 1 || !env.Meta.Truncated {
		t.Errorf("ls --limit 1 = %+v, truncated %v; want one entry and truncated true", rows, env.Meta.Truncated)
	}

	env, code = runCLI(t, "ls", "/alpha/nopk")
	mustCode(t, env, code, CodeUnsupported, "ls", "/alpha/nopk")

	env, code = runCLI(t, "ls", "/alpha/tasks", "--cursor", "abc")
	mustCode(t, env, code, CodeUnsupported, "ls", "/alpha/tasks", "--cursor", "abc")

	const compositeKey = "~kW3sia2luZCI6InRleHQiLCJkYXRhIjoiYSJ9LHsia2luZCI6InRleHQiLCJkYXRhIjoiYiJ9XQ"
	env, code = runCLI(t, "cat", "/alpha/tags/"+compositeKey)
	compositeRow := decode[rowJSON](t, mustOK(t, env, code, "cat", "/alpha/tags/"+compositeKey))
	if compositeRow.Fields["v"] != (valueJSON{Kind: "text", Data: "v1"}) {
		t.Errorf("composite key row = %+v, want v1", compositeRow.Fields)
	}

	env, code = runCLI(t, "cat", "/alpha/tasks/1")
	row := decode[rowJSON](t, mustOK(t, env, code, "cat", "/alpha/tasks/1"))
	wantFields := map[string]valueJSON{
		"id":        {Kind: "integer", Data: "1"},
		"title":     {Kind: "text", Data: "uno ñ / 100% ~ ~e"},
		"note":      {Kind: "null", Data: ""},
		"payload":   {Kind: "blob", Data: "AP8="},
		"big":       {Kind: "integer", Data: "9223372036854775807"},
		"ratio":     {Kind: "real", Data: "1.25"},
		"author_id": {Kind: "integer", Data: "1"},
	}
	for name, want := range wantFields {
		if got := row.Fields[name]; got != want {
			t.Errorf("cat field %s = %+v, want %+v", name, got, want)
		}
	}
	if row.Path != "/alpha/tasks/1" || len(row.ETag) != 7+64 {
		t.Errorf("cat row path/etag = %q/%q", row.Path, row.ETag)
	}

	env, code = runCLI(t, "cat", "/alpha/tasks/1/payload")
	if got := decode[valueJSON](t, mustOK(t, env, code, "cat", "/alpha/tasks/1/payload")); got != (valueJSON{Kind: "blob", Data: "AP8="}) {
		t.Errorf("cat field = %+v, want the typed blob wrapper", got)
	}

	env, code = runCLI(t, "cat", "/alpha/tasks/1/note")
	if got := decode[valueJSON](t, mustOK(t, env, code, "cat", "/alpha/tasks/1/note")); got != (valueJSON{Kind: "null", Data: ""}) {
		t.Errorf("cat NULL = %+v, want kind null with empty data", got)
	}

	env, code = runCLI(t, "cat", "/alpha/tasks/999")
	mustCode(t, env, code, CodeNotFound, "cat", "/alpha/tasks/999")

	env, code = runCLI(t, "cat", "/alpha/tasks/1/nope")
	mustCode(t, env, code, CodeNotFound, "cat", "/alpha/tasks/1/nope")

	env, code = runCLI(t, "cat", "/alpha/tasks")
	mustCode(t, env, code, CodeUnsupported, "cat", "/alpha/tasks")

	env, code = runCLI(t, "cat", "/alpha/tasks/abc")
	mustCode(t, env, code, CodeNotFound, "cat", "/alpha/tasks/abc")

	env, code = runCLI(t, "cat", "/alpha/tasks/007")
	mustCode(t, env, code, CodeNotFound, "cat", "/alpha/tasks/007")

	env, code = runCLI(t, "stat", "/alpha/tasks")
	stat := decode[statJSON](t, mustOK(t, env, code, "stat", "/alpha/tasks"))
	if stat.Type != "table" || stat.Navigable == nil || !*stat.Navigable || len(stat.Columns) != 7 {
		t.Errorf("stat table = %+v, want a navigable table with 7 columns", stat)
	}

	env, code = runCLI(t, "stat", "/alpha/tasks/1")
	stat = decode[statJSON](t, mustOK(t, env, code, "stat", "/alpha/tasks/1"))
	if stat.Type != "row" || stat.Keys["id"] != (valueJSON{Kind: "integer", Data: "1"}) {
		t.Errorf("stat row = %+v, want keys.id = 1", stat)
	}

	env, code = runCLI(t, "stat", "/alpha/tasks/1/title")
	stat = decode[statJSON](t, mustOK(t, env, code, "stat", "/alpha/tasks/1/title"))
	if stat.Type != "field" || stat.Path != "/alpha/tasks/1/title" {
		t.Errorf("stat field = %+v, want the field path", stat)
	}

	env, code = runCLI(t, "describe", "/alpha/tasks")
	describe := decode[describeJSON](t, mustOK(t, env, code, "describe", "/alpha/tasks"))
	if describe.Table != "tasks" || len(describe.PrimaryKey) != 1 || describe.PrimaryKey[0] != "id" {
		t.Errorf("describe = %+v, want table tasks with primary key id", describe)
	}
	if !describe.Capabilities.Readable || !describe.Capabilities.Navigable || describe.Capabilities.Writable {
		t.Errorf("describe capabilities = %+v", describe.Capabilities)
	}
	if len(describe.Triggers) != 1 || describe.Triggers[0] != "tasks_audit" {
		t.Errorf("describe triggers = %v, want [tasks_audit]", describe.Triggers)
	}
	if len(describe.Incoming) != 0 {
		t.Errorf("describe incoming = %v, want none", describe.Incoming)
	}

	env, code = runCLI(t, "describe", "/alpha/authors")
	describe = decode[describeJSON](t, mustOK(t, env, code, "describe", "/alpha/authors"))
	if len(describe.Incoming) != 1 || describe.Incoming[0].Table != "tasks" {
		t.Errorf("describe authors incoming = %v, want the tasks reference", describe.Incoming)
	}

	env, code = runCLI(t, "describe", "/alpha/task_view")
	mustCode(t, env, code, CodeNotFound, "describe", "/alpha/task_view")

	env, code = runCLI(t, "describe", "/alpha")
	mustCode(t, env, code, CodeUnsupported, "describe", "/alpha")

	env, code = runCLI(t, "ls", "/alpha/tasks/1")
	fields := decode[listJSON](t, mustOK(t, env, code, "ls", "/alpha/tasks/1"))
	if len(fields.Entries) != 7 || fields.Entries[0].Type != "field" {
		t.Errorf("ls row = %+v, want the seven fields", fields)
	}
}

// TestWP02CorruptMountIsUnsupported proves a file replaced or truncated after
// mount is a deterministic E_UNSUPPORTED, not the driver's opaque E_INTERNAL.
func TestWP02CorruptMountIsUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(string) error
	}{
		{"garbage header", func(p string) error {
			return os.WriteFile(p, []byte("not a database"), 0o600)
		}},
		{"truncated", func(p string) error { return os.Truncate(p, 0) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
			alpha := newFixture(t, alphaSchema)
			if _, code := runCLI(t, "mount", alpha, "/alpha"); code != 0 {
				t.Fatalf("mount exit = %d", code)
			}
			if err := tc.corrupt(alpha); err != nil {
				t.Fatalf("corrupt fixture: %v", err)
			}
			env, code := runCLI(t, "ls", "/alpha")
			mustCode(t, env, code, CodeUnsupported, "ls", "/alpha")
		})
	}
}

// buildBinary compiles the repository's CLI once per test that needs it.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "db-ai")
	build := exec.Command("go", "build", "-o", bin, "./cmd/db-ai")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestWP02MountRaceTwoProcesses proves flock serializes registry updates
// across processes: exactly one mount of the same alias succeeds and the
// other answers E_CONFLICT (exit 5) instead of clobbering the config
// (DESIGN §12: two competing processes).
func TestWP02MountRaceTwoProcesses(t *testing.T) {
	bin := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "config")
	alpha := newFixture(t, alphaSchema)

	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cmd := exec.Command(bin, "mount", alpha, "/alpha")
			cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
			err := cmd.Run()
			if err == nil {
				codes[i] = 0
				return
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				codes[i] = exitErr.ExitCode()
				return
			}
			t.Errorf("mount run: %v", err)
		}(i)
	}
	close(start)
	wg.Wait()

	sort.Ints(codes)
	if codes[0] != 0 || codes[1] != 5 {
		t.Errorf("concurrent mount exit codes = %v, want [0 5] (one success, one E_CONFLICT)", codes)
	}
}

// TestWP02StatMountMissingFile proves stat answers about the resource, not
// just the registry: a file removed after mount is E_NOT_FOUND (exit 3), the
// same as any read of that mount.
func TestWP02StatMountMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	alpha := newFixture(t, alphaSchema)
	if _, code := runCLI(t, "mount", alpha, "/alpha"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	if err := os.Remove(alpha); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	env, code := runCLI(t, "stat", "/alpha")
	mustCode(t, env, code, CodeNotFound, "stat", "/alpha")
}

// TestWP02BinaryAcceptance is the observable criterion of tk-7: mount two
// databases, list both, read typed values, and prove reads leave the files
// untouched.
func TestWP02BinaryAcceptance(t *testing.T) {
	bin := buildBinary(t)

	configDir := filepath.Join(t.TempDir(), "config")
	alpha := newFixture(t, alphaSchema)
	beta := newFixture(t, betaSchema)
	beforeAlpha := sha256File(t, alpha)
	beforeBeta := sha256File(t, beta)

	run := func(args ...string) (envelopeJSON, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configDir)
		var stdout, stderr bytes.Buffer
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

	for _, m := range []struct{ file, alias string }{{alpha, "/alpha"}, {beta, "/beta"}} {
		env, code := run("mount", m.file, m.alias)
		mustOK(t, env, code, "mount", m.file, m.alias)
	}

	env, code := run("mounts")
	mounts := decode[mountsJSON](t, mustOK(t, env, code, "mounts"))
	if len(mounts.Mounts) != 2 || mounts.Mounts[0].Alias != "alpha" || mounts.Mounts[1].Alias != "beta" {
		t.Errorf("mounts = %+v, want alpha then beta", mounts)
	}

	env, code = run("ls")
	root := decode[listJSON](t, mustOK(t, env, code, "ls"))
	if len(root.Entries) != 2 || root.Entries[0].Name != "alpha" || root.Entries[1].Name != "beta" {
		t.Errorf("ls / = %+v, want both mounts", root)
	}

	for _, path := range []string{"/alpha", "/beta", "/alpha/tasks"} {
		env, code = run("ls", path)
		mustOK(t, env, code, "ls", path)
	}

	env, code = run("cat", "/alpha/tasks/1")
	row := decode[rowJSON](t, mustOK(t, env, code, "cat", "/alpha/tasks/1"))
	if row.Fields["note"] != (valueJSON{Kind: "null", Data: ""}) ||
		row.Fields["payload"] != (valueJSON{Kind: "blob", Data: "AP8="}) ||
		row.Fields["big"] != (valueJSON{Kind: "integer", Data: "9223372036854775807"}) ||
		row.Fields["title"] != (valueJSON{Kind: "text", Data: "uno ñ / 100% ~ ~e"}) {
		t.Errorf("cat row = %+v, want NULL, BLOB, int64 and text preserved", row.Fields)
	}

	env, code = run("cat", "/beta/users/u1")
	row = decode[rowJSON](t, mustOK(t, env, code, "cat", "/beta/users/u1"))
	if row.Fields["email"] != (valueJSON{Kind: "text", Data: "ada@example.test"}) {
		t.Errorf("cat beta = %+v", row.Fields)
	}

	env, code = run("stat", "/alpha/tasks")
	mustOK(t, env, code, "stat", "/alpha/tasks")
	env, code = run("describe", "/alpha/tasks")
	mustOK(t, env, code, "describe", "/alpha/tasks")

	if after := sha256File(t, alpha); after != beforeAlpha {
		t.Errorf("alpha.db sha256 changed after reads: %s -> %s", beforeAlpha, after)
	}
	if after := sha256File(t, beta); after != beforeBeta {
		t.Errorf("beta.db sha256 changed after reads: %s -> %s", beforeBeta, after)
	}

	configPath := filepath.Join(configDir, "db-ai", "config.json")
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config permissions = %o, want 600", got)
	}

	missing := filepath.Join(t.TempDir(), "absent.db")
	env, code = run("mount", missing, "/absent")
	mustCode(t, env, code, CodeNotFound, "mount", missing, "/absent")
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mount created %s (err %v)", missing, err)
	}

	env, code = run("umount", "/beta")
	mustOK(t, env, code, "umount", "/beta")
	if _, err := os.Stat(beta); err != nil {
		t.Errorf("umount deleted the database: %v", err)
	}
}
