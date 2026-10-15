package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

// CallTimeout bounds each handshake or method call.
const CallTimeout = 60 * time.Second

// Client invokes a resolved, trusted exec plugin. Each method starts a fresh process.
// The plugin's verified filesystem supplies the executable, never PATH or its source directory.
type Client struct {
	Plugin plugin.Loaded
	Root   string
	Runner execx.Runner
	Stderr io.Writer
	// Timeout can shorten the default call deadline. Values over 60 seconds are ignored.
	Timeout time.Duration
}

// Describe returns the plugin's generators and checks after an API handshake.
func (c *Client) Describe(ctx context.Context) (sdk.DescribeResult, error) {
	var result sdk.DescribeResult
	err := c.call(ctx, "describe", nil, &result)
	return result, err
}

// Plan returns proposed files without applying them.
func (c *Client) Plan(ctx context.Context, p sdk.PlanParams) (sdk.PlanResult, error) {
	var result sdk.PlanResult
	err := c.call(ctx, "plan", p, &result)
	return result, err
}

// Check returns findings for one described check.
func (c *Client) Check(ctx context.Context, p sdk.CheckParams) (sdk.CheckResult, error) {
	var result sdk.CheckResult
	err := c.call(ctx, "check", p, &result)
	return result, err
}

func (c *Client) failure(action string, err error) error {
	return fmt.Errorf("plugin %s %s failed: %w\nTry: verify the plugin binary and its API 1 protocol", c.Plugin.Meta.Name, action, err)
}

func (c *Client) binary() (string, func(), error) {
	config, err := pack.ReadExec(c.Plugin.FS)
	if err != nil {
		return "", nil, err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	name := config.Binaries[platform]
	if config.Kind != "exec" || name == "" {
		return "", nil, fmt.Errorf("plugin %s has no exec binary for %s\nTry: obtain a plugin release with a %s binary", c.Plugin.Meta.Name, platform, platform)
	}
	data, err := fs.ReadFile(c.Plugin.FS, name)
	if err != nil {
		return "", nil, c.failure("read binary", err)
	}
	dir, err := os.MkdirTemp("", "spinup-plugin-")
	if err != nil {
		return "", nil, c.failure("stage binary", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(binary, data, 0700); err != nil {
		cleanup()
		return "", nil, c.failure("stage binary", err)
	}
	return binary, cleanup, nil
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	binary, cleanup, err := c.binary()
	if err != nil {
		return err
	}
	defer cleanup()
	env := []string{"SPINUP_PLUGIN_API=1"}
	for _, key := range []string{"PATH", "HOME", "LANG", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	processCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	// Close stdin on cancellation even when the child refuses to read it.
	stop := context.AfterFunc(processCtx, func() { _ = input.CloseWithError(processCtx.Err()) })
	defer stop()
	requests := make(chan sdk.Request, 1)
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		defer writer.Close()
		encoder := json.NewEncoder(writer)
		for {
			select {
			case <-processCtx.Done():
				return
			case request, ok := <-requests:
				if !ok {
					return
				}
				if encoder.Encode(request) != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); _ = input.Close(); <-writeDone }()
	responses := make(chan sdk.Response, 2)
	output := &responseWriter{responses: responses, cancel: cancel}
	stderr := c.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	diagnostic := &prefixWriter{out: stderr, prefix: "[plugin " + c.Plugin.Meta.Name + "] ", start: true, cancel: cancel}
	runner := c.Runner
	if runner == nil {
		runner = execx.OSRunner{}
	}
	exited := make(chan struct{})
	var exitErr error
	go func() {
		_, exitErr = runner.Run(processCtx, execx.Cmd{Name: binary, Env: env, ReplaceEnv: true, Dir: c.Root, Stdin: input, Stdout: output, Stderr: diagnostic})
		close(exited)
	}()
	// Always reap the process before removing its executable.
	defer func() {
		cancel(nil)
		_ = input.Close()
		<-exited
	}()
	timeout := c.Timeout
	if timeout <= 0 || timeout > CallTimeout {
		timeout = CallTimeout
	}
	exchange := func(callCtx context.Context, id int, method string, params, result any) error {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		select {
		case requests <- sdk.Request{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: raw}:
		case <-exited:
			// A completed response can still be queued after the process exits.
		case <-callCtx.Done():
			return context.Cause(callCtx)
		}
		if id == 2 {
			// Close stdin after the final frame so the runner can reap an early exit.
			close(requests)
		}
		var response sdk.Response
		select {
		case response = <-responses:
		case <-callCtx.Done():
			return context.Cause(callCtx)
		case <-exited:
			select {
			case response = <-responses:
			default:
				if cause := context.Cause(processCtx); cause != nil {
					return cause
				}
				if exitErr != nil {
					return fmt.Errorf("process exited without a complete response: %w", exitErr)
				}
				return errors.New("process exited without a complete response")
			}
		}
		if response.JSONRPC != "2.0" || !bytes.Equal(response.ID, []byte(fmt.Sprint(id))) {
			return errors.New("wrong JSON-RPC version or response id")
		}
		if response.Error != nil {
			return response.Error
		}
		if len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
			return errors.New("response has no result")
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
		return nil
	}
	var hello sdk.HandshakeResult
	handshakeCtx, stopHandshake := context.WithTimeout(processCtx, timeout)
	handshakeErr := exchange(handshakeCtx, 1, "handshake", nil, &hello)
	stopHandshake()
	if handshakeErr != nil {
		return c.failure("handshake", handshakeErr)
	}
	if hello.API != sdk.API || hello.Name != c.Plugin.Meta.Name || strings.TrimPrefix(hello.Version, "v") != strings.TrimPrefix(c.Plugin.Meta.Version, "v") {
		return c.failure("handshake", errors.New("API, name, or version mismatch"))
	}
	capabilities := map[string]bool{}
	for _, capability := range hello.Capabilities {
		if capability != sdk.CapabilityGenerator && capability != sdk.CapabilityCheck {
			return c.failure("handshake", fmt.Errorf("unsupported capability %q", capability))
		}
		capabilities[capability] = true
	}
	if method == "plan" && !capabilities[sdk.CapabilityGenerator] || method == "check" && !capabilities[sdk.CapabilityCheck] {
		return c.failure(method, errors.New("required capability is absent"))
	}
	methodCtx, stopMethod := context.WithTimeout(processCtx, timeout)
	defer stopMethod()
	if err := exchange(methodCtx, 2, method, params, result); err != nil {
		return c.failure(method, err)
	}
	if description, ok := result.(*sdk.DescribeResult); ok {
		if len(description.Generators) > 0 && !capabilities[sdk.CapabilityGenerator] || len(description.Checks) > 0 && !capabilities[sdk.CapabilityCheck] {
			return c.failure(method, errors.New("description exceeds advertised capabilities"))
		}
	}
	select {
	case <-exited:
	case <-methodCtx.Done():
		return c.failure(method, context.Cause(methodCtx))
	}
	if exitErr != nil {
		return c.failure(method, exitErr)
	}
	if cause := context.Cause(processCtx); cause != nil {
		return c.failure(method, cause)
	}
	if len(output.pending) != 0 {
		return c.failure(method, errors.New("unterminated response"))
	}
	return nil
}

type responseWriter struct {
	pending   []byte
	count     int
	responses chan<- sdk.Response
	cancel    context.CancelCauseFunc
}

func (w *responseWriter) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 {
		part, rest, found := bytes.Cut(data, []byte{'\n'})
		if len(w.pending)+len(part) > sdk.MaxMessageBytes {
			err := errors.New("response exceeds 16 MiB")
			w.cancel(err)
			return 0, err
		}
		w.pending = append(w.pending, part...)
		if !found {
			return length, nil
		}
		w.count++
		var response sdk.Response
		if w.count > 2 {
			err := errors.New("unexpected extra response")
			w.cancel(err)
			return 0, err
		}
		if err := json.Unmarshal(w.pending, &response); err != nil {
			w.cancel(err)
			return 0, err
		}
		if (len(response.Result) == 0) == (response.Error == nil) {
			err := errors.New("response must contain exactly one result or error")
			w.cancel(err)
			return 0, err
		}
		w.responses <- response
		w.pending = nil
		data = rest
	}
	return length, nil
}

type prefixWriter struct {
	out    io.Writer
	prefix string
	start  bool
	size   int
	cancel context.CancelCauseFunc
}

func (w *prefixWriter) Write(data []byte) (int, error) {
	n := len(data)
	w.size += n
	if w.size > sdk.MaxMessageBytes {
		err := errors.New("stderr exceeds 16 MiB")
		w.cancel(err)
		return 0, err
	}
	for len(data) > 0 {
		if w.start {
			if _, err := io.WriteString(w.out, w.prefix); err != nil {
				return 0, err
			}
			w.start = false
		}
		index := bytes.IndexByte(data, '\n')
		length := len(data)
		if index >= 0 {
			length = index + 1
			w.start = true
		}
		if _, err := w.out.Write(data[:length]); err != nil {
			return 0, err
		}
		data = data[length:]
	}
	return n, nil
}
