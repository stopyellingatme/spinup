package exec

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
)

// Fake is an ordered, scripted Runner for tests.
type Fake struct {
	mu           sync.Mutex
	expectations []*fakeExpectation
	calls        []Cmd
}

// Expect adds an expected command call.
func (f *Fake) Expect(name string, argsPrefix ...string) *Expectation {
	f.mu.Lock()
	defer f.mu.Unlock()
	expectation := &fakeExpectation{
		name:       name,
		argsPrefix: append([]string(nil), argsPrefix...),
	}
	f.expectations = append(f.expectations, expectation)
	return &Expectation{fake: f, expectation: expectation}
}

// Expectation configures the response for an expected command call.
type Expectation struct {
	fake        *Fake
	expectation *fakeExpectation
}

// Return makes an expected call return the supplied output and exit code.
func (e *Expectation) Return(stdout, stderr string, exitCode int) *Expectation {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	expectation := e.expectation
	expectation.stdout = stdout
	expectation.stderr = stderr
	expectation.exitCode = exitCode
	expectation.err = nil
	return e
}

// Fail makes an expected call return err.
func (e *Expectation) Fail(err error) *Expectation {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	e.expectation.err = err
	return e
}

// Run records c and returns its scripted response.
func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cloneCmd(c))
	if len(f.expectations) == 0 {
		f.mu.Unlock()
		return Result{}, fmt.Errorf("unexpected command: %s", c.String())
	}
	expectation := *f.expectations[0]
	if expectation.name != c.Name || !hasPrefix(c.Args, expectation.argsPrefix) {
		f.mu.Unlock()
		return Result{}, fmt.Errorf("unexpected command: %s", c.String())
	}
	f.expectations = f.expectations[1:]
	f.mu.Unlock()

	result := Result{
		Stdout:   redactBytes([]byte(expectation.stdout), c.Redact),
		Stderr:   redactBytes([]byte(expectation.stderr), c.Redact),
		ExitCode: expectation.exitCode,
	}
	f.mu.Lock()
	writeOutput(c.Stdout, result.Stdout)
	writeOutput(c.Stderr, result.Stderr)
	f.mu.Unlock()
	if expectation.err != nil {
		return result, expectation.err
	}
	if result.ExitCode != 0 {
		return result, &ExitError{
			Cmd:        c.String(),
			Code:       result.ExitCode,
			StderrTail: stderrTail(string(result.Stderr)),
		}
	}
	return result, nil
}

// Calls returns a copy of the calls received by f.
func (f *Fake) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]Cmd, len(f.calls))
	for index, call := range f.calls {
		calls[index] = cloneCmd(call)
	}
	return calls
}

// AssertDone fails t when f has an unused expectation.
func (f *Fake) AssertDone(t testing.TB) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.expectations) != 0 {
		t.Errorf("fake has %d unused expectation(s); next command is %s", len(f.expectations), f.expectations[0].name)
	}
}

type fakeExpectation struct {
	name       string
	argsPrefix []string
	stdout     string
	stderr     string
	exitCode   int
	err        error
}

func hasPrefix(values, prefix []string) bool {
	if len(values) < len(prefix) {
		return false
	}
	for index := range prefix {
		if values[index] != prefix[index] {
			return false
		}
	}
	return true
}

func cloneCmd(c Cmd) Cmd {
	c.Args = append([]string(nil), c.Args...)
	c.Env = append([]string(nil), c.Env...)
	c.Redact = append([]string(nil), c.Redact...)
	return c
}

func writeOutput(dst io.Writer, output []byte) {
	if dst != nil && len(output) > 0 {
		_, _ = dst.Write(output)
	}
}
