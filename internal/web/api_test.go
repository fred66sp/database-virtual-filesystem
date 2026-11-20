package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dbvfs/internal/app"
	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"

	_ "modernc.org/sqlite"
)

const testSchema = `
CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, kind TEXT);
INSERT INTO items VALUES (1, 'alpha', 'a');
INSERT INTO items VALUES (2, 'beta', 'b');
CREATE TABLE secret (id INTEGER PRIMARY KEY, value TEXT);
INSERT INTO secret VALUES (1, 'hidden');
`

// newTestService builds an isolated service over a real SQLite mount, like the
// CLI tests do, without touching the developer's config or state directories.
func newTestService(t *testing.T) app.Service {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "fixture.db")
	conn, err := sql.Open("sqlite", "file:"+db+"?mode=rwc")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := conn.Exec(testSchema); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	configDir := filepath.Join(dir, "config")
	err = state.Update(configDir, func(cfg *state.Config) error {
		cfg.Mounts["alpha"] = state.Mount{File: db, Mode: "ro"}
		return nil
	})
	if err != nil {
		t.Fatalf("register mount: %v", err)
	}
	return app.Service{
		ConfigDir: configDir,
		StateDir:  filepath.Join(dir, "state"),
		AuditDir:  filepath.Join(dir, "audit"),
		Timeout:   5 * time.Second,
	}
}

// recorded captures what a handler hands to the §10 renderer. Envelope shape
// and status mapping are the CLI's responsibility and are tested there.
type recorded struct {
	data      any
	truncated bool
	next      string
	err       error
}

func newRecorder() (WriteResponse, *recorded) {
	rec := &recorded{}
	return func(w http.ResponseWriter, data any, truncated bool, nextCursor string, err error) {
		rec.data, rec.truncated, rec.next, rec.err = data, truncated, nextCursor, err
	}, rec
}

// doGet issues one GET and records what the handler rendered.
func doGet(t *testing.T, h http.Handler, rec *recorded, target string) *httptest.ResponseRecorder {
	t.Helper()
	*rec = recorded{}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
	return rr
}

func decodeData[T any](t *testing.T, rec *recorded) T {
	t.Helper()
	if rec.err != nil {
		t.Fatalf("handler error = %v, want data", rec.err)
	}
	raw, err := json.Marshal(rec.data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

type listShape struct {
	Path    string `json:"path"`
	Entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"entries"`
}

type statShape struct {
	Type      string   `json:"type"`
	Path      string   `json:"path"`
	Columns   []string `json:"columns"`
	Navigable *bool    `json:"navigable"`
}

type describeShape struct {
	Path       string   `json:"path"`
	Table      string   `json:"table"`
	PrimaryKey []string `json:"primary_key"`
	Columns    []struct {
		Name         string `json:"name"`
		DeclaredType string `json:"declared_type"`
		PrimaryKey   int    `json:"primary_key"`
		Nullable     bool   `json:"nullable"`
		Generated    bool   `json:"generated"`
	} `json:"columns"`
	Capabilities struct {
		Readable  bool `json:"readable"`
		Writable  bool `json:"writable"`
		Navigable bool `json:"navigable"`
	} `json:"capabilities"`
}

type findShape struct {
	Path string `json:"path"`
	Rows []struct {
		Path   string          `json:"path"`
		Fields json.RawMessage `json:"fields"`
		ETag   string          `json:"etag"`
	} `json:"rows"`
}

type catShape struct {
	Path   string                     `json:"path"`
	Fields map[string]json.RawMessage `json:"fields"`
	ETag   string                     `json:"etag"`
}

type countShape struct {
	Count int64 `json:"count"`
}

type grepShape struct {
	Path    string `json:"path"`
	Entries []struct {
		Path     string `json:"path"`
		Row      string `json:"row"`
		Column   string `json:"column"`
		Position int    `json:"position"`
	} `json:"entries"`
}

func TestAPICatalog(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	doGet(t, h, rec, "/api/ls?path=/")
	root := decodeData[listShape](t, rec)
	if len(root.Entries) != 1 || root.Entries[0].Name != "alpha" || root.Entries[0].Type != "mount" || root.Entries[0].Path != "/alpha" {
		t.Errorf("ls / = %+v, want the alpha mount", root)
	}

	doGet(t, h, rec, "/api/ls?path=/alpha")
	tables := decodeData[listShape](t, rec)
	if got := entryNames(tables); got != "items,secret" {
		t.Errorf("ls /alpha tables = %q, want items,secret", got)
	}

	doGet(t, h, rec, "/api/stat?path=/alpha/items")
	stat := decodeData[statShape](t, rec)
	if stat.Type != "table" || stat.Path != "/alpha/items" || stat.Navigable == nil || !*stat.Navigable {
		t.Errorf("stat = %+v, want a navigable table", stat)
	}
	if got := strings.Join(stat.Columns, ","); got != "id,name,kind" {
		t.Errorf("stat columns = %q, want id,name,kind", got)
	}

	doGet(t, h, rec, "/api/describe?path=/alpha/items")
	describe := decodeData[describeShape](t, rec)
	if describe.Table != "items" || describe.Path != "/alpha/items" {
		t.Errorf("describe = %+v, want table items", describe)
	}
	if got := strings.Join(describe.PrimaryKey, ","); got != "id" {
		t.Errorf("describe primary key = %q, want id", got)
	}
	if !describe.Capabilities.Readable || !describe.Capabilities.Navigable || describe.Capabilities.Writable {
		t.Errorf("describe capabilities = %+v, want readable and not writable", describe.Capabilities)
	}
	if len(describe.Columns) != 3 || describe.Columns[0].Name != "id" || describe.Columns[0].PrimaryKey != 1 {
		t.Errorf("describe columns = %+v, want id first and primary", describe.Columns)
	}

	doGet(t, h, rec, "/api/tree?path=/alpha&depth=1")
	tree := decodeData[listShape](t, rec)
	if got := entryNames(tree); got != "items,secret" {
		t.Errorf("tree /alpha depth 1 = %q, want items,secret", got)
	}
}

func entryNames(l listShape) string {
	names := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		names[i] = e.Name
	}
	return strings.Join(names, ",")
}

// TestAPIFindContract exercises REF-01 (projection), REF-02 (order) and REF-03
// (keyset pagination and an independent count) through the HTTP surface.
func TestAPIFindContract(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	order := url.QueryEscape("id:desc")
	doGet(t, h, rec, "/api/find?path=/alpha/items&order="+order+"&limit=1")
	page := decodeData[findShape](t, rec)
	if len(page.Rows) != 1 || page.Rows[0].Path != "/alpha/items/2" {
		t.Fatalf("first page = %+v, want row 2", page)
	}
	if !rec.truncated || rec.next == "" {
		t.Fatalf("first page truncated = %v, cursor = %q; want both", rec.truncated, rec.next)
	}
	cursor := rec.next

	doGet(t, h, rec, "/api/find?path=/alpha/items&order="+order+"&limit=1&cursor="+url.QueryEscape(cursor))
	page = decodeData[findShape](t, rec)
	if len(page.Rows) != 1 || page.Rows[0].Path != "/alpha/items/1" || rec.truncated {
		t.Errorf("second page = %+v truncated %v, want row 1 and no more pages", page, rec.truncated)
	}

	doGet(t, h, rec, "/api/find?path=/alpha/items&fields=name")
	projected := decodeData[findShape](t, rec)
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(projected.Rows[0].Fields, &fields); err != nil {
		t.Fatalf("decode projected row: %v", err)
	}
	if len(fields) != 2 {
		t.Errorf("projected fields = %v, want only id and name", fields)
	}
	for _, key := range []string{"id", "name"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("projected fields %v, want key %q", fields, key)
		}
	}
	if _, ok := fields["kind"]; ok {
		t.Errorf("projected fields %v must not include kind", fields)
	}

	doGet(t, h, rec, "/api/find?path=/alpha/items&where=id%3E%3D1&where=id%3C%3D1")
	filtered := decodeData[findShape](t, rec)
	if len(filtered.Rows) != 1 || filtered.Rows[0].Path != "/alpha/items/1" {
		t.Errorf("repeated where = %+v, want row 1", filtered)
	}

	// A count is the whole filtered set: a page limit does not apply.
	doGet(t, h, rec, "/api/count?path=/alpha/items")
	if got := decodeData[countShape](t, rec); got.Count != 2 {
		t.Errorf("count = %d, want 2 regardless of any page limit", got.Count)
	}

	doGet(t, h, rec, "/api/count?path=/alpha/items&where=kind=a")
	if got := decodeData[countShape](t, rec); got.Count != 1 {
		t.Errorf("count kind=a = %d, want 1", got.Count)
	}
}

func TestAPICat(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	doGet(t, h, rec, "/api/cat?path=/alpha/items/1")
	row := decodeData[catShape](t, rec)
	if row.Path != "/alpha/items/1" || row.ETag == "" {
		t.Errorf("cat = %+v, want its path and an etag", row)
	}
	var id struct {
		Kind string `json:"kind"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(row.Fields["id"], &id); err != nil {
		t.Fatalf("decode id: %v", err)
	}
	if id.Kind != "integer" || id.Data != "1" {
		t.Errorf("cat id = %+v, want integer 1", id)
	}

	doGet(t, h, rec, "/api/cat?path=/alpha/items/1&fields=name")
	projected := decodeData[catShape](t, rec)
	if _, ok := projected.Fields["kind"]; ok || len(projected.Fields) != 2 {
		t.Errorf("cat projection = %v, want only id and name", projected.Fields)
	}
}

func TestAPIGrep(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	doGet(t, h, rec, "/api/grep?path=/alpha/items&text=beta")
	found := decodeData[grepShape](t, rec)
	if len(found.Entries) != 1 {
		t.Fatalf("grep beta = %+v, want one entry", found)
	}
	entry := found.Entries[0]
	if entry.Path != "/alpha/items/2/name" || entry.Row != "/alpha/items/2" || entry.Column != "name" || entry.Position != 1 {
		t.Errorf("grep entry = %+v, want /alpha/items/2/name at position 1", entry)
	}
}

// TestAPICountWithSearch pins the grep --count parity: counting matches is the
// same contract as the CLI's, including the ignored where/fields (REF-03).
func TestAPICountWithSearch(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	doGet(t, h, rec, "/api/count?path=/alpha/items&text=beta")
	counted := decodeData[struct {
		Count int64 `json:"count"`
	}](t, rec)
	if counted.Count != 1 {
		t.Errorf("count text=beta = %d, want 1", counted.Count)
	}

	// where is meaningless for a grep count (the CLI cannot express the
	// combination) and the application ignores it: the count stays the
	// search's.
	doGet(t, h, rec, "/api/count?path=/alpha/items&text=beta&where=name:nope")
	if rec.err != nil {
		t.Errorf("count text=beta with where = %v, want the search's count", rec.err)
		return
	}
	counted2 := decodeData[struct {
		Count int64 `json:"count"`
	}](t, rec)
	if counted2.Count != 1 {
		t.Errorf("count text=beta with where = %d, want 1", counted2.Count)
	}
}

func TestAPIErrors(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	tests := []struct {
		name   string
		target string
		want   error
	}{
		{"missing path", "/api/ls", vfs.ErrInvalidPath},
		{"unknown mount", "/api/stat?path=/nope", storage.ErrNotFound},
		{"missing table", "/api/stat?path=/alpha/nope", storage.ErrNotFound},
		{"limit below one", "/api/find?path=/alpha/items&limit=0", storage.ErrInvalid},
		{"limit above cap", "/api/find?path=/alpha/items&limit=1001", storage.ErrLimit},
		{"depth above cap", "/api/tree?path=/alpha&depth=5", storage.ErrLimit},
		{"unknown column", "/api/find?path=/alpha/items&fields=nope", storage.ErrInvalid},
		{"fields repeated", "/api/find?path=/alpha/items&fields=id&fields=title", storage.ErrInvalid},
		{"empty where", "/api/find?path=/alpha/items&where=", storage.ErrInvalid},
		{"empty grep", "/api/grep?path=/alpha/items&text=", storage.ErrInvalid},
		{"empty order", "/api/find?path=/alpha/items&order=%20", storage.ErrInvalid},
		{"ls with cursor", "/api/ls?path=/alpha&cursor=abc", storage.ErrUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doGet(t, h, rec, tc.target)
			if rec.err == nil {
				t.Fatalf("%s: handler succeeded, want %v", tc.target, tc.want)
			}
			if !errors.Is(rec.err, tc.want) {
				t.Errorf("%s: err = %v, want %v", tc.target, rec.err, tc.want)
			}
		})
	}
}

func TestAPIPolicy(t *testing.T) {
	svc := newTestService(t)
	setPolicy(t, svc, []state.Policy{{Ops: []string{"cat"}, Prefix: "/alpha/items/1"}})
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	doGet(t, h, rec, "/api/cat?path=/alpha/items/1")
	if rec.err != nil {
		t.Fatalf("granted cat = %v, want data", rec.err)
	}

	doGet(t, h, rec, "/api/cat?path=/alpha/items/2")
	if !errors.Is(rec.err, storage.ErrDenied) {
		t.Errorf("cat on an ungranted row = %v, want E_DENIED", rec.err)
	}

	for _, target := range []string{
		"/api/find?path=/alpha/items",
		"/api/count?path=/alpha/items",
		"/api/grep?path=/alpha/items&text=alpha",
	} {
		doGet(t, h, rec, target)
		if !errors.Is(rec.err, storage.ErrDenied) {
			t.Errorf("%s = %v, want E_DENIED", target, rec.err)
		}
	}
}

func setPolicy(t *testing.T, svc app.Service, allow []state.Policy) {
	t.Helper()
	err := state.Update(svc.ConfigDir, func(cfg *state.Config) error {
		m := cfg.Mounts["alpha"]
		m.Allow = allow
		cfg.Mounts["alpha"] = m
		return nil
	})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
}

// TestAPIHasNoWriteSurface pins the structural read-only guarantee: unknown and
// mutation-shaped routes do not answer, and non-GET methods are refused.
func TestAPIHasNoWriteSurface(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	for _, target := range []string{"/api/touch", "/api/write", "/api/upsert", "/api/cp", "/api/mv", "/api/rm", "/api/batch"} {
		rr := doGet(t, h, rec, target)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rr.Code)
		}
		if rec.err != nil {
			t.Errorf("GET %s reached a handler: %v", target, rec.err)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, target := range []string{
			"/api/ls", "/api/tree", "/api/stat", "/api/describe",
			"/api/find", "/api/count", "/api/cat", "/api/grep",
		} {
			req := httptest.NewRequest(method, target+"?path=/alpha", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 404 or 405", method, target, rr.Code)
			}
		}
	}
}

func TestAPIHeadersAndStaticIndex(t *testing.T) {
	svc := newTestService(t)
	respond, rec := newRecorder()
	h := NewHandler(svc, respond)

	rr := doGet(t, h, rec, "/api/ls?path=/")
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("api Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("api X-Content-Type-Options = %q, want nosniff", got)
	}

	rr = doGet(t, h, rec, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("index Content-Type = %q, want text/html", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"db-ai Visual Explorer", "Explorador visual — llega en P4", "/api/ls?path=/"} {
		if !strings.Contains(body, want) {
			t.Errorf("index body does not contain %q", want)
		}
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("index X-Content-Type-Options = %q, want nosniff", got)
	}
}
