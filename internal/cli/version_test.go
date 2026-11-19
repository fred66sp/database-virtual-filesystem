package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// versionEnvelope is the shape a consumer parses, independent of the
// production types.
type versionEnvelope struct {
	Version int  `json:"version"`
	OK      bool `json:"ok"`
	Data    struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		State   string `json:"state"`
	} `json:"data"`
}

func TestVersionGolden(t *testing.T) {
	var stdout, stderr bytes.Buffer
	gotExit := Run([]string{"version", "--json", "--request-id", "req-version"}, &stdout, &stderr)
	if gotExit != 0 {
		t.Fatalf("Run(version) exit = %d, want 0 (stdout %q, stderr %q)", gotExit, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	var env versionEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout.String())
	}
	if env.Version != apiVersion || !env.OK {
		t.Errorf("envelope = %+v, want version %d and ok true", env, apiVersion)
	}
	if got, want := env.Data.Version, version; got != want {
		t.Errorf("data.version = %q, want %q", got, want)
	}
	if got, want := env.Data.Commit, "unknown"; got != want {
		t.Errorf("data.commit = %q, want %q without ldflags", got, want)
	}
	if got, want := env.Data.State, "dev"; got != want {
		t.Errorf("data.state = %q, want %q without release ldflags", got, want)
	}

	checkGolden(t, "version.golden", stdout.Bytes())
}

func TestVersionCommitFromLdflags(t *testing.T) {
	const commit = "0123456789abcdef"
	bin := filepath.Join(t.TempDir(), "db-ai")
	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X dbvfs/internal/cli.commit="+commit+" -X dbvfs/internal/cli.buildState=release",
		"./cmd/db-ai")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build with ldflags: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("db-ai version: %v", err)
	}
	var env versionEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON object: %v (%q)", err, out)
	}
	if env.Data.Commit != commit {
		t.Errorf("data.commit = %q, want the injected %q", env.Data.Commit, commit)
	}
	if env.Data.State != "release" {
		t.Errorf("data.state = %q, want release", env.Data.State)
	}
}
