package remote

import (
	"bytes"
	"context"
	"io/fs"
	"sync"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// UploadCall records an uploaded file, including a private copy of its bytes.
type UploadCall struct {
	Data []byte
	Path string
	Mode fs.FileMode
}

// Fake is an ordered, scripted remote Runner. Its zero value is ready for use.
// Expect, ExpectUpload, and ExpectClose share one ordered expectation queue.
type Fake struct {
	script  runexec.Fake
	mu      sync.Mutex
	calls   []Cmd
	uploads []UploadCall
	closes  int
}

// Expect adds a remote command expectation, matched by its argv prefix.
func (f *Fake) Expect(argv ...string) *runexec.Expectation { return f.script.Expect("remote", argv...) }

// ExpectUpload adds an upload expectation, matched by its destination path.
func (f *Fake) ExpectUpload(path string) *runexec.Expectation { return f.script.Expect("upload", path) }

// ExpectClose adds a connection close expectation.
func (f *Fake) ExpectClose() *runexec.Expectation { return f.script.Expect("close") }

// Run records c and returns its scripted result, with stream output and redaction.
func (f *Fake) Run(ctx context.Context, c Cmd) (runexec.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cloneCmd(c))
	f.mu.Unlock()
	return f.script.Run(ctx, runexec.Cmd{Name: "remote", Args: c.Argv, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Timeout: c.Timeout, Redact: c.Redact})
}

// Upload records a copy of data and returns the scripted response.
func (f *Fake) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	f.mu.Lock()
	f.uploads = append(f.uploads, UploadCall{Data: bytes.Clone(data), Path: path, Mode: mode})
	f.mu.Unlock()
	_, err := f.script.Run(ctx, runexec.Cmd{Name: "upload", Args: []string{path}, Redact: []string{string(data)}})
	return err
}

// Close records a close call and returns the scripted response.
func (f *Fake) Close(ctx context.Context) error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	_, err := f.script.Run(ctx, runexec.Cmd{Name: "close"})
	return err
}

// Calls returns copies of the recorded remote commands.
func (f *Fake) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Cmd, len(f.calls))
	for i, c := range f.calls {
		out[i] = cloneCmd(c)
	}
	return out
}

// Uploads returns copies of the recorded uploads, including their bytes.
func (f *Fake) Uploads() []UploadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]UploadCall(nil), f.uploads...)
	for i := range out {
		out[i].Data = bytes.Clone(out[i].Data)
	}
	return out
}

// CloseCalls returns the number of connection close calls.
func (f *Fake) CloseCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.closes }

// AssertDone fails t if an expectation remains unused.
func (f *Fake) AssertDone(t testing.TB) { t.Helper(); f.script.AssertDone(t) }

func cloneCmd(c Cmd) Cmd {
	c.Argv = append([]string(nil), c.Argv...)
	c.Redact = append([]string(nil), c.Redact...)
	return c
}
