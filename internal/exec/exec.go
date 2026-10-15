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
	"path/filepath"
	"runtime"
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

	// ReplaceEnv uses only Env instead of extending the parent environment.
	// A nil Env supplies an empty environment when ReplaceEnv is true.
	// On Windows, os/exec also supplies SYSTEMROOT when it is absent.
	ReplaceEnv bool

	// Interactive preserves the parent's process group for terminal reads on Unix.
	// Timeout or cancellation kills only the child. Descendants can survive.
	// On Windows, cancellation always kills only the direct child.
	Interactive bool
}

// processWaitDelay bounds pipe draining after cancellation or child exit.
const processWaitDelay = 2 * time.Second

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
	path, err := lookPath(c)
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
	command.Env = append([]string{}, c.Env...)
	if !c.ReplaceEnv {
		command.Env = append(os.Environ(), c.Env...)
	}
	command.Stdin = c.Stdin
	command.WaitDelay = processWaitDelay
	configureProcess(command, c.Interactive)
	command.Cancel = func() error { return killProcess(command) }

	var stdout, stderr bytes.Buffer
	var sinkMu sync.Mutex
	stdoutSink := newRedactingSink(c.Stdout, c.Redact, &sinkMu)
	stderrSink := newRedactingSink(c.Stderr, c.Redact, &sinkMu)
	command.Stdout = io.MultiWriter(&stdout, stdoutSink)
	command.Stderr = io.MultiWriter(&stderr, stderrSink)

	started := time.Now()
	err = command.Start()
	if err == nil {
		// os/exec stops watching the context when the direct child exits.
		// Continue watching until Wait finishes draining the inherited pipes.
		killed := make(chan struct{})
		stop := context.AfterFunc(runCtx, func() {
			_ = killProcess(command)
			close(killed)
		})
		err = command.Wait()
		if !stop() {
			<-killed
		}
	}
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

func lookPath(c Cmd) (string, error) {
	dir, err := filepath.Abs(c.Dir)
	if err != nil {
		return "", fmt.Errorf("resolve command directory %q: %w\nTry: check the working directory", c.Dir, err)
	}
	resolve := func(name string) (string, error) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return osexec.LookPath(name)
	}
	if strings.ContainsAny(c.Name, "/"+string(filepath.Separator)) {
		if path, err := resolve(c.Name); err == nil {
			return path, nil
		}
		return "", &NotFoundError{Name: c.Name}
	}
	searchPath := os.Getenv("PATH")
	if c.ReplaceEnv {
		searchPath = ""
	}
	pathSet := false
	for _, entry := range c.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			searchPath = value
			pathSet = true
		}
	}
	if c.Dir == "" && !pathSet && !c.ReplaceEnv {
		return LookPath(c.Name)
	}
	for _, entry := range filepath.SplitList(searchPath) {
		if path, err := resolve(filepath.Join(entry, c.Name)); err == nil {
			return path, nil
		}
	}
	return "", &NotFoundError{Name: c.Name}
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
	for _, secret := range orderedSecrets(secrets) {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	return value
}

func orderedSecrets(secrets []string) []string {
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(first, second int) bool {
		return len(secrets[first]) > len(secrets[second])
	})
	return secrets
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
	stages  []redactionStage
	writeMu *sync.Mutex
}

func newRedactingSink(dst io.Writer, secrets []string, writeMu *sync.Mutex) *redactingSink {
	sink := &redactingSink{dst: dst, writeMu: writeMu}
	for _, secret := range orderedSecrets(secrets) {
		if secret != "" {
			sink.stages = append(sink.stages, redactionStage{secret: secret})
		}
	}
	return sink
}

func (s *redactingSink) Write(data []byte) (int, error) {
	if s.dst == nil {
		return len(data), nil
	}
	s.drain(data, false)
	return len(data), nil
}

func (s *redactingSink) Flush() {
	if s.dst == nil {
		return
	}
	s.drain(nil, true)
}

func (s *redactingSink) drain(data []byte, final bool) {
	// Apply the same ordered replacements as captured output, one stage per secret.
	for index := range s.stages {
		data = s.stages[index].redact(data, final)
	}
	if len(data) == 0 {
		return
	}
	s.writeMu.Lock()
	_, _ = s.dst.Write(data)
	s.writeMu.Unlock()
}

type redactionStage struct {
	secret  string
	pending []byte
}

func (s *redactionStage) redact(data []byte, final bool) []byte {
	s.pending = append(s.pending, data...)
	limit := len(s.pending)
	if !final {
		// Preserve greedy, non-overlapping matches before retaining a partial tail.
		// A tail must never begin inside a complete match.
		matched := 0
		for {
			index := bytes.Index(s.pending[matched:], []byte(s.secret))
			if index < 0 {
				break
			}
			matched += index + len(s.secret)
		}
		limit -= longestSecretPrefixSuffix(s.pending[matched:], s.secret)
	}
	output := []byte(strings.ReplaceAll(string(s.pending[:limit]), s.secret, "***"))
	s.pending = append(s.pending[:0], s.pending[limit:]...)
	return output
}

func longestSecretPrefixSuffix(data []byte, secret string) int {
	longest := 0
	for length := 1; length < len(secret) && length <= len(data); length++ {
		if bytes.HasSuffix(data, []byte(secret[:length])) {
			longest = length
		}
	}
	return longest
}
