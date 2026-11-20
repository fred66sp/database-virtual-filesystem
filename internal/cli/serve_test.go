package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dbvfs/internal/state"
	"dbvfs/internal/web"
)

// TestServeDispatch proves serve is dispatched outside the single-shot renderer
// and that its options come from the parsed flags. The server itself is
// injected, so no port is ever bound.
func TestServeDispatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	tests := []struct {
		name     string
		args     []string
		wantPort int
		wantOpen bool
	}{
		{"defaults", []string{"serve"}, defaultPort, false},
		{"port and open", []string{"serve", "--port", "18099", "--open"}, 18099, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := serveFunc
			defer func() { serveFunc = old }()
			var got web.Options
			serveFunc = func(_ context.Context, opts web.Options) error {
				got = opts
				return nil
			}

			var stdout, stderr bytes.Buffer
			if code := Run(tc.args, &stdout, &stderr); code != 0 {
				t.Fatalf("Run(%q) exit = %d, want 0 (stderr %q)", tc.args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("Run(%q) stdout = %q, want empty (serve has no result envelope)", tc.args, stdout.String())
			}
			if got.Port != tc.wantPort || got.Open != tc.wantOpen {
				t.Errorf("options = port %d open %v, want %d/%v", got.Port, got.Open, tc.wantPort, tc.wantOpen)
			}
			if got.Respond == nil || got.Service.ConfigDir == "" {
				t.Errorf("options not wired: %+v", got)
			}
		})
	}
}

func TestServeFailureUsesStandardEnvelope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	old := serveFunc
	defer func() { serveFunc = old }()
	serveFunc = func(context.Context, web.Options) error { return errors.New("bind failed") }

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"serve"}, &stdout, &stderr); code != CodeInternal.Exit() {
		t.Fatalf("exit = %d, want %d", code, CodeInternal.Exit())
	}
	var env envelopeJSON
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if env.OK || env.Error == nil || env.Error.Code != string(CodeInternal) {
		t.Errorf("envelope = %+v, want E_INTERNAL", env)
	}
	// E_INTERNAL keeps the public message generic; the cause is diagnostic.
	if !strings.Contains(stderr.String(), "bind failed") {
		t.Errorf("stderr = %q, want the diagnostic", stderr.String())
	}
}

func TestServeRejectsBadFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	for _, args := range [][]string{
		{"serve", "--port", "0"},
		{"serve", "--port", "70000"},
		{"serve", "--port", "nope"},
		{"serve", "extra"},
	} {
		env, code := runCLI(t, args...)
		mustCode(t, env, code, CodeUsage, args...)
	}
}

// TestServeHTTPContract exercises the production handler (real §10 envelopes and
// the code→status table) against a real temporary mount.
func TestServeHTTPContract(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	alpha := newFixture(t, alphaSchema)
	if _, code := runCLI(t, "mount", alpha, "/alpha"); code != 0 {
		t.Fatalf("mount exit = %d", code)
	}
	svc, err := newService(&invocation{timeout: defaultTimeout})
	if err != nil {
		t.Fatalf("newService: %v", err)
	}
	h := web.NewHandler(svc, serveResponse)

	call := func(method, target string) (*httptest.ResponseRecorder, envelopeJSON) {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
		var env envelopeJSON
		if got := rr.Header().Get("Content-Type"); got == "application/json" {
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s %s body is not JSON: %v (%q)", method, target, err, rr.Body.String())
			}
		}
		return rr, env
	}

	rr, env := call(http.MethodGet, "/api/ls?path=/alpha")
	if rr.Code != http.StatusOK || !env.OK {
		t.Fatalf("GET /api/ls = %d %+v, want 200 with ok:true", rr.Code, env)
	}
	tables := decode[listJSON](t, env.Data)
	if got := len(tables.Entries); got != 4 {
		t.Errorf("ls entries = %d, want the four fixture tables", got)
	}
	if env.Meta.RequestID == "" {
		t.Error("meta.request_id is missing")
	}

	rr, env = call(http.MethodGet, "/api/find?path=/alpha/tasks&limit=1")
	if rr.Code != http.StatusOK || !env.OK {
		t.Fatalf("GET /api/find = %d %+v, want 200", rr.Code, env)
	}
	found := decode[findJSON](t, env.Data)
	if len(found.Rows) != 1 || !env.Meta.Truncated || env.Meta.NextCursor == "" {
		t.Errorf("find page = %+v truncated %v cursor %q, want one truncated row", found, env.Meta.Truncated, env.Meta.NextCursor)
	}

	rr, env = call(http.MethodGet, "/api/stat?path=/nowhere")
	if rr.Code != http.StatusNotFound || env.OK || env.Error == nil || env.Error.Code != string(CodeNotFound) {
		t.Errorf("GET /api/stat (absent) = %d %+v, want 404 E_NOT_FOUND", rr.Code, env)
	}

	setServePolicy(t, []state.Policy{{Ops: []string{"cat"}, Prefix: "/alpha/tasks/1"}})
	rr, env = call(http.MethodGet, "/api/find?path=/alpha/tasks")
	if rr.Code != http.StatusForbidden || env.OK || env.Error == nil || env.Error.Code != string(CodeDenied) {
		t.Errorf("GET /api/find (denied) = %d %+v, want 403 E_DENIED", rr.Code, env)
	}

	for _, target := range []string{"/api/touch", "/api/rm"} {
		if rr, _ := call(http.MethodGet, target); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (no write surface)", target, rr.Code)
		}
	}
	if rr, _ := call(http.MethodPost, "/api/ls?path=/alpha"); rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/ls = %d, want 404 or 405", rr.Code)
	}
}

func setServePolicy(t *testing.T, allow []state.Policy) {
	t.Helper()
	dir, err := state.Dir()
	if err != nil {
		t.Fatalf("state.Dir: %v", err)
	}
	err = state.Update(dir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = allow
		cfg.Mounts["alpha"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
}
