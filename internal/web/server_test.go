package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// listenURL matches the single startup line, capturing the bound URL.
var listenURL = regexp.MustCompile(`(?m)^db-ai serve: listening on (http://127\.0\.0\.1:\d+) \(mounts: \d+\)$`)

// startupLog signals the bound URL as soon as Serve logs it, so the test never
// sleeps waiting for a server.
type startupLog struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	urls chan string
}

func newStartupLog() *startupLog { return &startupLog{urls: make(chan string, 1)} }

func (l *startupLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	if m := listenURL.FindStringSubmatch(l.buf.String()); m != nil {
		select {
		case l.urls <- m[1]:
		default:
		}
	}
	return len(p), nil
}

func echoRespond(w http.ResponseWriter, data any, _ bool, _ string, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(data)
}

// TestServeLifecycle starts on an ephemeral port, serves a smoke request,
// shuts down on context cancellation and proves the listener was released.
func TestServeLifecycle(t *testing.T) {
	svc := newTestService(t)
	logw := newStartupLog()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{Port: 0, Service: svc, Respond: echoRespond, Stderr: logw})
	}()

	var base string
	select {
	case base = <-logw.urls:
	case err := <-done:
		t.Fatalf("Serve exited before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the startup log")
	}
	if !strings.Contains(logw.buf.String(), "(mounts: 1)") {
		t.Errorf("startup log = %q, want one mount", logw.buf.String())
	}

	resp, err := http.Get(base + "/api/ls?path=/")
	if err != nil {
		t.Fatalf("smoke request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read smoke response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("smoke status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("smoke Cache-Control = %q, want no-store", got)
	}
	if !bytes.Contains(body, []byte(`"alpha"`)) {
		t.Errorf("smoke body = %s, want the alpha mount", body)
	}

	indexResp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("index request: %v", err)
	}
	indexBody, err := io.ReadAll(indexResp.Body)
	_ = indexResp.Body.Close()
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if indexResp.StatusCode != http.StatusOK || !bytes.Contains(indexBody, []byte("db-ai Visual Explorer")) {
		t.Errorf("index = %d %q, want the explorer placeholder", indexResp.StatusCode, indexBody)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within the shutdown timeout")
	}

	ln, err := net.Listen("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("rebind %s after shutdown: %v (listener not released)", base, err)
	}
	_ = ln.Close()
}

func TestServeRequiresResponder(t *testing.T) {
	err := Serve(context.Background(), Options{Service: newTestService(t)})
	if err == nil {
		t.Fatal("Serve without a responder succeeded, want an error")
	}
}
