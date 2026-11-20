// Package web serves the local, read-only db-ai Visual Explorer: the internal
// /api/ HTTP API over app.Service and the embedded static page. The read-only
// guarantee is structural: this package never references a mutation handler.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"dbvfs/internal/app"
)

//go:embed static
var staticFS embed.FS

// WriteResponse renders one §10 envelope and derives the HTTP status from the
// error code. The CLI supplies the implementation so the success/error shapes
// and the Code→status table have exactly one definition.
type WriteResponse func(w http.ResponseWriter, data any, truncated bool, nextCursor string, err error)

// Options configures Serve. The server is localhost-only by construction: there
// is no bind override.
type Options struct {
	Port    int           // TCP port on 127.0.0.1; 0 asks the OS for one
	Service app.Service   // read-only application service, shared by requests
	Respond WriteResponse // required: §10 envelope renderer
	Open    bool          // open the explorer in the default browser
	Stderr  io.Writer     // startup and browser diagnostics; nil discards
}

const (
	bindHost        = "127.0.0.1"
	shutdownTimeout = 5 * time.Second
)

// Serve binds 127.0.0.1:port, logs the URL and the mount count, and serves until
// ctx is canceled. Shutdown stops accepting connections and waits up to
// shutdownTimeout for in-flight requests; a canceled context is a clean exit.
//
// One app.Service is shared by every request: it is an immutable value (paths
// plus a deadline) whose handlers open and close the store per call, so no
// per-request wiring or mutable state is needed and concurrent requests are safe.
func Serve(ctx context.Context, opts Options) error {
	if opts.Respond == nil {
		return errors.New("web: Respond is required")
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindHost, opts.Port))
	if err != nil {
		return fmt.Errorf("listen on %s:%d: %w", bindHost, opts.Port, err)
	}
	mounts, err := opts.Service.Mounts()
	if err != nil {
		_ = ln.Close()
		return err
	}
	url := "http://" + ln.Addr().String()
	_, _ = fmt.Fprintf(opts.Stderr, "db-ai serve: listening on %s (mounts: %d)\n", url, len(mounts))
	if opts.Open {
		openBrowser(url, opts.Stderr)
	}
	return serve(ctx, ln, NewHandler(opts.Service, opts.Respond))
}

// serve owns the listener until Shutdown. One goroutine runs http.Server.Serve
// and reports on errc; Serve always waits for it, so no goroutine outlives the
// call. https://pkg.go.dev/net/http#Server.Shutdown returns when the listener
// is closed, so a rebind after Serve succeeds.
func serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			<-errc
			return fmt.Errorf("shutdown http server: %w", err)
		}
		<-errc
		return nil
	}
}

// NewHandler builds the read-only surface: one GET route per read operation,
// a 404 for any other /api/ path, and the embedded explorer at /. Because only
// GET handlers exist, a POST to an API path can never reach a mutation.
func NewHandler(svc app.Service, respond WriteResponse) http.Handler {
	a := &api{svc: svc, respond: respond}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ls", a.ls)
	mux.HandleFunc("GET /api/tree", a.tree)
	mux.HandleFunc("GET /api/stat", a.stat)
	mux.HandleFunc("GET /api/describe", a.describe)
	mux.HandleFunc("GET /api/find", a.find)
	mux.HandleFunc("GET /api/count", a.count)
	mux.HandleFunc("GET /api/cat", a.cat)
	mux.HandleFunc("GET /api/grep", a.grep)
	mux.HandleFunc("/api/", http.NotFound)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		// The embedded tree is fixed at build time; a failure is a build defect.
		panic(fmt.Sprintf("web: static assets: %v", err))
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	return securityHeaders(mux)
}

// securityHeaders adds the minimum headers from the approved design: no MIME
// sniffing anywhere and no caching of API answers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// openBrowser starts the platform opener best-effort. Any failure is a warning:
// the URL is already logged and the server never blocks on the browser.
func openBrowser(url string, stderr io.Writer) {
	cmd := browserCommand(url)
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(stderr, "db-ai serve: cannot open browser: %v\n", err)
		return
	}
	// The opener is its own process; reap it in the background so no zombie
	// outlives the server without delaying startup.
	go func() { _ = cmd.Wait() }()
}

func browserCommand(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return exec.Command("xdg-open", url)
	}
}
