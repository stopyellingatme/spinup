package devloop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func writeInput(t *testing.T, root, path, content string) {
	t.Helper()
	path = filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestWatcherDebounceAndRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeInput(t, root, "api/db/queries/todos.sql", "select 1")
	writeInput(t, root, "api/openapi/openapi.yaml", "openapi: 3.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan []gen.PostStep, 4)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- (Watcher{Root: root, Interval: 10 * time.Millisecond, Debounce: 100 * time.Millisecond, Out: &out, Dispatch: func(_ context.Context, steps []gen.PostStep) error {
			calls <- append([]gen.PostStep(nil), steps...)
			return errors.New("test failure. Try: fix inputs")
		}}).Run(ctx)
	}()
	// The watcher takes its first snapshot when the goroutine starts. A burst of
	// writes before that moment would become the baseline. So repeat the burst
	// until a dispatch arrives. Each burst has no sleep, so the writes join one
	// debounce window. Each burst changes the file size, so the poll sees it.
	var steps []gen.PostStep
	deadline := time.After(10 * time.Second)
	for size := 2; steps == nil; size += 3 {
		for i := 0; i < 3; i++ {
			writeInput(t, root, "api/db/queries/todos.sql", strings.Repeat("x", size+i))
		}
		select {
		case steps = <-calls:
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			t.Fatal("SQL dispatch missing")
		}
	}
	if !reflect.DeepEqual(steps, []gen.PostStep{gen.PostSQLC}) {
		t.Fatal(steps)
	}
	writeInput(t, root, "api/openapi/paths/todos.yaml", "/todos: {}")
	select {
	case steps := <-calls:
		if !reflect.DeepEqual(steps, []gen.PostStep{gen.PostOpenAPI, gen.PostClient}) {
			t.Fatal(steps)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenAPI dispatch missing after failure")
	}
	writeInput(t, root, "api/openapi/bundle.gen.yaml", "generated: true")
	select {
	case steps := <-calls:
		t.Fatalf("unexpected generated-file dispatch: %v", steps)
	case <-time.After(120 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("test failure")) {
		t.Fatal("failure was not logged")
	}
}

func TestWatcherDeletionAndFilters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{"api/db/queries/todos.sql", "api/db/queries/nested/ignored.sql", "api/openapi/schemas/todo.yaml", "api/openapi/bundle.gen.yaml", "api/openapi/readme.md"} {
		writeInput(t, root, path, "input")
	}
	before, err := snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("watched inputs: %v", before)
	}
	if err := os.Remove(filepath.Join(root, "api/db/queries/todos.sql")); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := changes(before, after, nil); !reflect.DeepEqual(got, []gen.PostStep{gen.PostSQLC}) {
		t.Fatal(got)
	}
}

func TestWatcherUsesPostStepRegistry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := manifest.New()
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	const step gen.PostStep = "m4-dev-watcher-test"
	calls := 0
	runner := &exec.Fake{}
	genrun.RegisterPostStep(step, func(_ context.Context, got string, files []string, r exec.Runner) error {
		calls++
		if got != root || len(files) != 0 || r != runner {
			t.Errorf("dispatch arguments: %s %v %v", got, files, r)
		}
		return nil
	})
	if err := (Watcher{Root: root, Runner: runner}).dispatch(context.Background(), []gen.PostStep{step}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("registry calls: %d", calls)
	}
	current, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, current) {
		t.Fatal("watcher changed lock")
	}
}
