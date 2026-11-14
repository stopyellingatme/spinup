package live

import (
	"net/http"
	"testing"
	"time"
)

// nullWriter is a response writer that makes no allocation.
type nullWriter struct {
	header http.Header
	n      int
}

func (w *nullWriter) Header() http.Header                { return w.header }
func (w *nullWriter) WriteHeader(int)                    {}
func (w *nullWriter) Write(p []byte) (int, error)        { w.n += len(p); return len(p), nil }
func (w *nullWriter) FlushError() error                  { return nil }
func (w *nullWriter) SetWriteDeadline(_ time.Time) error { return nil }

// TestStreamHotPathAllocs checks that a wake, a read, the writes, and the
// flush of a batch make no allocation (ADR 0033 sections 6.3 and 9.2).
func TestStreamHotPathAllocs(t *testing.T) {
	f := newFeed("notes", 1024)
	s := f.subscribe(1)
	w := &nullWriter{header: http.Header{}}
	out := &streamWriter{w: w, rc: http.NewResponseController(w), timeout: time.Second}
	frames := make([][]byte, streamBatch)
	patches := make([][]byte, 2000)
	for i := range patches {
		patches[i] = patchFrame(1, int64(i+1), []byte(`[{"op":"touch","id":"1"}]`))
	}
	seq := 0
	allocs := testing.AllocsPerRun(1000, func() {
		f.append(1, int64(seq+1), patches[seq])
		seq++
		<-s.wake
		n, state := f.read(s, frames)
		if state != readOK || n != 1 {
			t.Fatalf("read %d %v", n, state)
		}
		if err := out.batch(frames[:n]); err != nil {
			t.Fatal(err)
		}
		clear(frames[:n])
	})
	if allocs != 0 {
		t.Fatalf("the hot path makes %v allocations, want 0", allocs)
	}
	if w.n == 0 {
		t.Fatal("no byte was written")
	}
}

// TestStreamAgeJitter checks the bounds of the random stream age.
func TestStreamAgeJitter(t *testing.T) {
	for range 1000 {
		if d := streamAge(time.Minute); d < 48*time.Second || d > time.Minute {
			t.Fatalf("age %s is outside 80 to 100 percent of 1m", d)
		}
	}
}
