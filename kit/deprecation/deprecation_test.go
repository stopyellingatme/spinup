package deprecation

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func reset() { seen = sync.Map{} }

func TestWarnOncePerKey(t *testing.T) {
	reset()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if !Warn(logger, "a", "a is deprecated. Use b instead.") {
		t.Fatal("first Warn did not log")
	}
	if Warn(logger, "a", "a is deprecated. Use b instead.") {
		t.Fatal("second Warn logged")
	}
	if !Warn(logger, "b", "b is deprecated") {
		t.Fatal("new key did not log")
	}
	out := buf.String()
	if got := strings.Count(out, "level=WARN"); got != 2 {
		t.Fatalf("warnings = %d, want 2\n%s", got, out)
	}
	if !strings.Contains(out, "deprecated=a") {
		t.Fatalf("missing key attribute\n%s", out)
	}
}

func TestWarnNilLogger(t *testing.T) {
	reset()
	if !Warn(nil, "nil-logger", "message") {
		t.Fatal("Warn with nil logger did not log")
	}
}

func TestWarnConcurrent(t *testing.T) {
	reset()
	var count atomic.Int32
	var wg sync.WaitGroup
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if Warn(logger, "shared", "message") {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("logged %d times, want 1", count.Load())
	}
}
