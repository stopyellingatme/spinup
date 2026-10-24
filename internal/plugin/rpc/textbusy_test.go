package rpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

// busyOnce fails the first start with ETXTBSY, wrapped as the OS runner wraps a start error.
type busyOnce struct {
	inner execx.Runner
	calls atomic.Int32
}

func (b *busyOnce) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if b.calls.Add(1) == 1 {
		return execx.Result{}, fmt.Errorf("run command %s: fork/exec %s: %w", c.Name, c.Name, syscall.ETXTBSY)
	}
	return b.inner.Run(ctx, c)
}

func TestPluginStartRetriesTextFileBusy(t *testing.T) {
	t.Parallel()
	p := buildFake(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "behavior"), []byte("success"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := &busyOnce{inner: execx.OSRunner{}}
	c := Client{Plugin: p, Root: root, Timeout: 10 * time.Second, Runner: runner}
	if _, err := c.Plan(t.Context(), sdk.PlanParams{Generator: "hello", Args: []string{"world"}}); err != nil {
		t.Fatalf("a start that failed with ETXTBSY once must succeed on the retry: %v", err)
	}
	if n := runner.calls.Load(); n < 2 {
		t.Fatalf("runner calls = %d, want a retry", n)
	}
}
