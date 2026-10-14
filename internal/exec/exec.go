package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cmd describes a subprocess invocation.
type Cmd struct {
	Name    string
	Args    []string
	Dir     string
	Env     []string
	Stdin   io.Reader
	Timeout time.Duration
	Redact  []string
	Stdout  io.Writer
	Stderr  io.Writer
}

// String returns a shell-quoted command line without secret values.
func (c Cmd) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	parts = append(parts, c.Args...)
	for index, part := range parts {
		parts[index] = shellQuote(redactString(part, c.Redact))
	}
	return strings.Join(parts, " ")
}

// Result holds the redacted output and execution details from a command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// Runner runs a command.
type Runner interface {
	Run(context.Context, Cmd) (Result, error)
}

// OSRunner runs commands through the operating system.
type OSRunner struct{}

// Run runs c and captures its redacted output.
func (OSRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	path, err := LookPath(c.Name)
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	cancel := func() {}
	if c.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.Timeout)
	}
	defer cancel()

	command := osexec.CommandContext(runCtx, path, c.Args...)
	command.Dir = c.Dir
	command.Env = append(os.Environ(), c.Env...)
	command.Stdin = c.Stdin
	configureProcess(command)
	command.Cancel = func() error { return killProcess(command) }

	var stdout, stderr bytes.Buffer
	var sinkMu sync.Mutex
	stdoutSink := newRedactingSink(c.Stdout, c.Redact, &sinkMu)
	stderrSink := newRedactingSink(c.Stderr, c.Redact, &sinkMu)
	command.Stdout = io.MultiWriter(&stdout, stdoutSink)
	command.Stderr = io.MultiWriter(&stderr, stderrSink)

	started := time.Now()
	err = command.Run()
	duration := time.Since(started)
	stdoutSink.Flush()
	stderrSink.Flush()

	result := Result{
		Stdout:   redactBytes(stdout.Bytes(), c.Redact),
		Stderr:   redactBytes(stderr.Bytes(), c.Redact),
		Duration: duration,
	}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if runCtx.Err() != nil {
		return result, fmt.Errorf("command %s stopped: %w\nTry: inspect the command timeout or context", c.String(), runCtx.Err())
	}
	if err == nil {
		return result, nil
	}

	var exitError *osexec.ExitError
	if errors.As(err, &exitError) {
		return result, &ExitError{
			Cmd:        c.String(),
			Code:       result.ExitCode,
			StderrTail: stderrTail(string(result.Stderr)),
		}
	}
	return result, fmt.Errorf("run command %s: %w\nTry: check the command name and working directory", c.String(), err)
}

// ExitError reports a command that returned a nonzero exit code.
type ExitError struct {
	Cmd        string
	Code       int
	StderrTail string
}

// Error returns the command failure description.
func (e *ExitError) Error() string {
	if e.StderrTail == "" {
		return fmt.Sprintf("command %s failed with exit code %d\nTry: inspect the command output and retry.", e.Cmd, e.Code)
	}
	return fmt.Sprintf("command %s failed with exit code %d: %s\nTry: inspect the command output and retry.", e.Cmd, e.Code, e.StderrTail)
}

// NotFoundError reports a command that the system cannot find.
type NotFoundError struct {
	Name string
}

// Error returns guidance for a missing command.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("command %q was not found. Try: install %s, or run spinup doctor", e.Name, e.Name)
}

// LookPath finds name on the system executable path.
func LookPath(name string) (string, error) {
	path, err := osexec.LookPath(name)
	if err != nil {
		return "", &NotFoundError{Name: name}
	}
	return path, nil
}

// RunJSON runs c and decodes its standard output as JSON into v.
func RunJSON(ctx context.Context, r Runner, c Cmd, v any) error {
	result, err := r.Run(ctx, c)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(result.Stdout, v); err != nil {
		return fmt.Errorf("decode JSON from %s: %w\nTry: check the command output", c.String(), err)
	}
	return nil
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return !isShellSafe(r) }) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isShellSafe(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", r)
}

func redactBytes(value []byte, secrets []string) []byte {
	return []byte(redactString(string(value), secrets))
}

func redactString(value string, secrets []string) string {
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(first, second int) bool {
		return len(secrets[first]) > len(secrets[second])
	})
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	return value
}

func stderrTail(stderr string) string {
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	return strings.Join(lines, "\n")
}

type redactingSink struct {
	dst     io.Writer
	buffer  bytes.Buffer
	secrets []string
	writeMu *sync.Mutex
}

func newRedactingSink(dst io.Writer, secrets []string, writeMu *sync.Mutex) *redactingSink {
	return &redactingSink{dst: dst, secrets: append([]string(nil), secrets...), writeMu: writeMu}
}

func (s *redactingSink) Write(data []byte) (int, error) {
	if s.dst == nil {
		return len(data), nil
	}
	written, err := s.buffer.Write(data)
	if err != nil {
		return written, err
	}
	s.drain(false)
	return len(data), nil
}

func (s *redactingSink) Flush() {
	if s.dst == nil {
		return
	}
	s.drain(true)
}

func (s *redactingSink) drain(final bool) {
	if s.buffer.Len() == 0 {
		return
	}
	data := s.buffer.Bytes()
	limit := len(data)
	if !final {
		limit -= longestSecretPrefixSuffix(data, s.secrets)
	}
	if limit == 0 {
		return
	}
	s.writeMu.Lock()
	_, _ = s.dst.Write(redactBytes(data[:limit], s.secrets))
	s.writeMu.Unlock()
	s.buffer.Next(limit)
}

func longestSecretPrefixSuffix(data []byte, secrets []string) int {
	longest := 0
	for _, secret := range secrets {
		for length := 1; length < len(secret) && length <= len(data); length++ {
			if bytes.HasSuffix(data, []byte(secret[:length])) && length > longest {
				longest = length
			}
		}
	}
	return longest
}
