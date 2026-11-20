package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dbvfs/internal/app"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

// Contract limits mirror the CLI (DESIGN §10): a query page is capped at 1000
// records and a tree at four levels.
const (
	defaultRequestTimeout = 5 * time.Second
	maxLimit              = 1000
	maxDepth              = 4
)

// api translates query parameters into app.Service calls. It holds no state:
// app.Service is an immutable value whose handler opens and closes the store
// per call, so one instance serves concurrent requests safely.
type api struct {
	svc     app.Service
	respond WriteResponse
}

func (a *api) ls(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	// The CLI rejects ls --cursor with E_UNSUPPORTED: a directory listing has
	// no stable keyset. The API answers the same instead of ignoring it.
	if r.URL.Query().Get("cursor") != "" {
		a.respond(w, nil, false, "", codedUnsupported("ls does not support pagination cursors"))
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, truncated, err := a.svc.LS(ctx, p, limit)
	a.respond(w, data, truncated, "", err)
}

func (a *api) tree(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	depth, err := depthParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, truncated, err := a.svc.Tree(ctx, p, depth, limit)
	a.respond(w, data, truncated, "", err)
}

func (a *api) stat(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, err := a.svc.Stat(ctx, p)
	a.respond(w, data, false, "", err)
}

func (a *api) describe(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, err := a.svc.Describe(ctx, p)
	a.respond(w, data, false, "", err)
}

func (a *api) find(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	where, err := whereParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	fields, err := fieldsParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	order, err := orderParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, truncated, next, err := a.svc.Find(ctx, p, where, fields, order, limit, r.URL.Query().Get("cursor"))
	a.respond(w, data, truncated, next, err)
}

func (a *api) count(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	where, err := whereParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	fields, err := fieldsParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	order, err := orderParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	// grep --count: counting the matches of a literal search is the same grant
	// as the search itself (REF-03), and like the CLI it takes no where/fields.
	op := "find"
	var search *string
	if text := r.URL.Query().Get("text"); text != "" {
		op = "grep"
		where, fields, order = nil, nil, ""
		search = &text
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, err := a.svc.Count(ctx, p, op, where, order, fields, search)
	a.respond(w, data, false, "", err)
}

func (a *api) cat(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	fields, err := fieldsParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, err := a.svc.Cat(ctx, p, fields)
	a.respond(w, data, false, "", err)
}

func (a *api) grep(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		a.respond(w, nil, false, "", err)
		return
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	data, truncated, next, err := a.svc.Grep(ctx, p, r.URL.Query().Get("text"), limit, r.URL.Query().Get("cursor"))
	a.respond(w, data, truncated, next, err)
}

// requestContext bounds one request with the service deadline. The HTTP client
// going away cancels it too, through r.Context().
func (a *api) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := a.svc.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return context.WithTimeout(r.Context(), timeout)
}

// pathParam decodes the required absolute virtual path with the core codec, so
// the API accepts exactly what the CLI accepts (DESIGN §4).
func pathParam(r *http.Request) (vfs.Path, error) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		return vfs.Path{}, fmt.Errorf("%w: query parameter %q is required", vfs.ErrInvalidPath, "path")
	}
	return vfs.Parse(raw)
}

// codedUnsupported builds an E_UNSUPPORTED error from an API-side contract
// check, the way the CLI does for a flag a command does not take.
func codedUnsupported(msg string) error {
	return fmt.Errorf("%w: %s", storage.ErrUnsupported, msg)
}

func limitParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil // the application applies its documented default
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: query parameter %q must be an integer of at least 1", storage.ErrInvalid, "limit")
	}
	if n > maxLimit {
		return 0, fmt.Errorf("%w: limit exceeds maximum %d", storage.ErrLimit, maxLimit)
	}
	return n, nil
}

func depthParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("depth")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: query parameter %q must be an integer of at least 1", storage.ErrInvalid, "depth")
	}
	if n > maxDepth {
		return 0, fmt.Errorf("%w: depth exceeds maximum %d", storage.ErrLimit, maxDepth)
	}
	return n, nil
}

// whereParam accepts where=EXPR repeated. The expressions are typed and
// validated by the application, exactly as --where is.
func whereParam(r *http.Request) ([]string, error) {
	values := r.URL.Query()["where"]
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%w: query parameter %q requires a non-empty expression", storage.ErrInvalid, "where")
		}
		out = append(out, value)
	}
	return out, nil
}

// fieldsParam mirrors the CLI --fields: one flag, comma-separated, no
// trimming (parseFields validates the columns against the catalog). A second
// fields= is the API form of the CLI's "flag given more than once".
func fieldsParam(r *http.Request) ([]string, error) {
	values := r.URL.Query()["fields"]
	switch len(values) {
	case 0:
		return nil, nil
	case 1:
		return strings.Split(values[0], ","), nil
	}
	return nil, fmt.Errorf("%w: query parameter %q given more than once", storage.ErrInvalid, "fields")
}

// orderParam mirrors the CLI --order: optional, but a present-but-empty value
// is the usage error "--order requires a non-empty list".
func orderParam(r *http.Request) (string, error) {
	raw := r.URL.Query().Get("order")
	if raw == "" {
		return "", nil
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%w: query parameter %q requires a non-empty list", storage.ErrInvalid, "order")
	}
	return raw, nil
}
