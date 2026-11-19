package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAppendSerializesConcurrentWriters proves the log is one JSON object per
// line under contention and that every line carries a UTC timestamp.
func TestAppendSerializesConcurrentWriters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	const writers = 32

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Append(dir, Record{
				RequestID: "req",
				Actor:     "agent",
				Operation: "write",
				Alias:     "work",
				Resource:  "/work/tasks/1/title",
				Status:    "attempt",
			}); err != nil {
				t.Errorf("Append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != writers {
		t.Fatalf("audit log has %d lines, want %d", len(lines), writers)
	}
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", i+1, err, line)
		}
		if !strings.HasSuffix(rec.Timestamp, "Z") {
			t.Errorf("line %d timestamp = %q, want UTC (Z)", i+1, rec.Timestamp)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("stat audit log: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("audit log permissions = %o, want 600", got)
	}
}
