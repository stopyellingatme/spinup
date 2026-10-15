package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

func buildHello(t *testing.T) plugin.Loaded {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "bin", "hello")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	_, err = (execx.OSRunner{}).Run(t.Context(), execx.Cmd{Name: "go", Args: []string{"build", "-o", binary, "."}, Dir: filepath.Join(root, "testdata/plugins/exec-hello/src"), Env: []string{"GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.Loaded{Meta: plugin.Meta{Name: "exec-hello", Version: "1.0.0", API: 1}, FS: fstest.MapFS{"plugin.toml": &fstest.MapFile{Data: []byte("name='exec-hello'\nversion='1.0.0'\napi=1\nspinup='*'\nkind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}")}, "bin/hello": &fstest.MapFile{Data: data, Mode: 0755}}}
}
func TestHelloProcess(t *testing.T) {
	t.Parallel()
	p := buildHello(t)
	var stderr bytes.Buffer
	c := Client{Plugin: p, Root: t.TempDir(), Stderr: &stderr}
	description, err := c.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Generators) != 1 || len(description.Checks) != 1 {
		t.Fatalf("%+v", description)
	}
	result, err := c.Plan(t.Context(), sdk.PlanParams{Generator: "hello", Args: []string{"world"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Files[0].Data) != "Hello, world!\n" {
		t.Fatalf("%+v", result)
	}
	checks, err := c.Check(t.Context(), sdk.CheckParams{Check: "hello"})
	if err != nil || len(checks.Findings) != 1 {
		t.Fatalf("%+v: %v", checks, err)
	}
}
func TestMissingPlatform(t *testing.T) {
	t.Parallel()
	p := plugin.Loaded{Meta: plugin.Meta{Name: "hello"}, FS: fstest.MapFS{"plugin.toml": &fstest.MapFile{Data: []byte("kind='exec'\n[exec]\nbinaries={'js-wasm'='bin/hello'}")}, "bin/hello": &fstest.MapFile{Data: []byte("x"), Mode: 0755}}}
	_, err := (&Client{Plugin: p, Root: t.TempDir()}).Describe(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("%v", err)
	}
}

// A fake runner writes real wire frames and observes the scrubbed launch contract.
type wireRunner struct {
	run func(context.Context, execx.Cmd) (execx.Result, error)
}

func (r wireRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) { return r.run(ctx, c) }

func TestWireFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, response string }{
		{"json", "not json\n"}, {"id", `{"jsonrpc":"2.0","id":99,"result":{}}` + "\n"},
		{"api", `{"jsonrpc":"2.0","id":1,"result":{"name":"hello","version":"1","api":2}}` + "\n"},
		{"huge", strings.Repeat("x", sdk.MaxMessageBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fakeClient(t)
			c.Runner = wireRunner{func(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
				_, err := cmd.Stdout.Write([]byte(tc.response))
				return execx.Result{}, err
			}}
			_, err := c.Describe(t.Context())
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("%v", err)
			}
		})
	}
}
func fakeClient(t *testing.T) *Client {
	t.Helper()
	return &Client{Root: t.TempDir(), Plugin: plugin.Loaded{Meta: plugin.Meta{Name: "hello", Version: "1", API: 1}, FS: fstest.MapFS{"plugin.toml": &fstest.MapFile{Data: []byte("kind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}")}, "bin/hello": &fstest.MapFile{Data: []byte("x"), Mode: fs.FileMode(0755)}}}}
}
func TestCallTimeout(t *testing.T) {
	t.Parallel()
	c := fakeClient(t)
	c.Timeout = 10 * time.Millisecond
	c.Runner = wireRunner{func(ctx context.Context, _ execx.Cmd) (execx.Result, error) {
		<-ctx.Done()
		return execx.Result{}, ctx.Err()
	}}
	_, err := c.Describe(t.Context())
	if err == nil {
		t.Fatal("timeout ignored")
	}
}

func TestPlanAfterHandshakeExit(t *testing.T) {
	t.Parallel()
	exitErr := errors.New("exit status 7")
	for _, tc := range []struct {
		name string
		err  error
	}{{"clean", nil}, {"nonzero", exitErr}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fakeClient(t)
			c.Timeout = time.Second
			c.Runner = wireRunner{func(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
				result, _ := json.Marshal(sdk.HandshakeResult{Name: "hello", Version: "1", API: 1, Capabilities: []string{sdk.CapabilityGenerator}})
				if err := json.NewEncoder(cmd.Stdout).Encode(sdk.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: result}); err != nil {
					return execx.Result{}, err
				}
				return execx.Result{}, tc.err
			}}
			// Exercise both select orders when the handshake and exit are ready together.
			for attempt := 0; attempt < 100; attempt++ {
				start := time.Now()
				_, err := c.Plan(t.Context(), sdk.PlanParams{Generator: "hello"})
				if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("attempt %d: want process exit error, got %v", attempt, err)
				}
				if tc.err != nil && !errors.Is(err, tc.err) {
					t.Fatalf("attempt %d: lost exit status: %v", attempt, err)
				}
				if elapsed := time.Since(start); elapsed >= c.Timeout/2 {
					t.Fatalf("attempt %d: exit took %s, timeout %s", attempt, elapsed, c.Timeout)
				}
			}
		})
	}
}
