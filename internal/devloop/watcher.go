package devloop

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
)

// Watcher polls generator inputs and debounces changes before registered post-steps.
type Watcher struct {
	// Root is the application directory.
	Root string
	// Runner executes registered post-steps.
	Runner exec.Runner
	// Out receives watcher failures.
	Out io.Writer
	// Interval and Debounce default to 200 ms and 150 ms. A poll of the inputs of a
	// 20-resource app costs well under 1 % of one core at this interval. The debounce
	// usually joins the writes of one editor save into one generation. Writes that straddle
	// a poll can start a second generation, which produces the same output.
	Interval, Debounce time.Duration
	// Dispatch overrides registry dispatch for tests or embedded callers.
	Dispatch func(context.Context, []gen.PostStep) error
	// ServiceOpenAPI lists the OpenAPI files of the sidecar services, relative to Root
	// with forward slashes. A change to one of them runs gen.PostServiceClients.
	ServiceOpenAPI []string
}

// The default timing of the watcher. A query edit waits half an interval on average,
// then the debounce, then sqlc.
const (
	defaultInterval = 200 * time.Millisecond
	defaultDebounce = 150 * time.Millisecond
)

type stamp struct {
	size  int64
	mtime time.Time
}

// Run watches until cancellation. A post-step failure is logged and does not stop watching.
func (w Watcher) Run(ctx context.Context) error {
	interval, debounce := w.Interval, w.Debounce
	if interval <= 0 {
		interval = defaultInterval
	}
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	out := w.Out
	if out == nil {
		out = io.Discard
	}
	dispatch := w.Dispatch
	if dispatch == nil {
		dispatch = w.dispatch
	}
	previous, err := snapshot(w.Root, w.ServiceOpenAPI)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var pending []gen.PostStep
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, err := snapshot(w.Root, w.ServiceOpenAPI)
			if err != nil {
				_, _ = fmt.Fprintf(out, "watch: %v\n", err)
				continue
			}
			changed := changes(previous, current, w.ServiceOpenAPI)
			previous = current
			if len(changed) == 0 {
				continue
			}
			for _, step := range changed {
				if !slices.Contains(pending, step) {
					pending = append(pending, step)
				}
			}
			timer.Reset(debounce)
		case <-timer.C:
			if err := dispatch(ctx, pending); err != nil {
				_, _ = fmt.Fprintf(out, "watch: %v\n", err)
			}
			pending = nil
		}
	}
}

func (w Watcher) dispatch(ctx context.Context, steps []gen.PostStep) error {
	// An empty generator plan lets the generator runner own registry ordering,
	// tool dispatch, and error guidance without writing files or changing the lock.
	g := gen.Generator{Name: "dev-watch", Run: func(context.Context, gen.Input) (*gen.Output, error) {
		return &gen.Output{PostSteps: steps}, nil
	}}
	_, err := genrun.Run(ctx, g, nil, nil, genrun.Options{Root: w.Root, Runner: w.Runner, Out: io.Discard})
	return err
}

// snapshot records the watched generator inputs. extra holds more files, relative to
// root with forward slashes. A missing extra file is not an error.
func snapshot(root string, extra []string) (map[string]stamp, error) {
	result := make(map[string]stamp)
	for _, relative := range extra {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scan dev inputs: %w\nTry: check the service source permissions", err)
		}
		result[relative] = stamp{size: info.Size(), mtime: info.ModTime()}
	}
	for _, directory := range []string{"api/db/queries", "api/openapi"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(directory)), func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !watched(relative) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			result[relative] = stamp{size: info.Size(), mtime: info.ModTime()}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan dev inputs: %w\nTry: check the API source permissions", err)
		}
	}
	return result, nil
}

// watched takes a slash-separated path. The path package keeps the slashes on Windows.
func watched(name string) bool {
	if strings.HasPrefix(name, "api/db/queries/") {
		return path.Ext(name) == ".sql" && path.Dir(name) == "api/db/queries"
	}
	return strings.HasPrefix(name, "api/openapi/") && strings.HasSuffix(name, ".yaml") && path.Base(name) != "bundle.gen.yaml"
}

// changes maps the changed inputs to post-steps. A path in extra is a service OpenAPI file.
func changes(before, after map[string]stamp, extra []string) []gen.PostStep {
	sql, openapi, serviceClients := false, false, false
	changed := func(path string) {
		switch {
		case slices.Contains(extra, path):
			serviceClients = true
		case strings.HasSuffix(path, ".sql"):
			sql = true
		default:
			openapi = true
		}
	}
	for path, value := range before {
		if after[path] != value {
			changed(path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changed(path)
		}
	}
	var steps []gen.PostStep
	if openapi {
		steps = append(steps, gen.PostOpenAPI)
	}
	if sql {
		steps = append(steps, gen.PostSQLC)
	}
	if openapi {
		steps = append(steps, gen.PostClient)
	}
	if serviceClients {
		steps = append(steps, gen.PostServiceClients)
	}
	return steps
}
