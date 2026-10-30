package devloop

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/stopyellingatme/spinup/internal/gen/serviceclient" // Register the service-clients post-step.
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/services/compose"
)

// servicesFile is the dev Compose file of the sidecar services, relative to the app root.
const servicesFile = ".spinup/compose.services.yml"

// writeServicesCompose writes .spinup/compose.services.yml for the declared services.
func writeServicesCompose(root, app string, list []services.Service, lgtm bool) error {
	data, err := compose.Dev(compose.DevInput{App: app, Services: list, LGTM: lgtm})
	if err != nil {
		return fmt.Errorf("render dev services: %w\nTry: correct [[services]] in spinup.toml", err)
	}
	// compose.Dev writes build contexts relative to deploy/. The dev command passes
	// --project-directory <root>, and Compose then resolves every relative path against
	// the root. So the contexts must start at the root.
	data = []byte(strings.ReplaceAll(string(data), "context: ../"+services.Dir+"/", "context: "+services.Dir+"/"))
	data = []byte(strings.Replace(string(data), "# The paths are relative to deploy/, the directory of the first -f file.",
		"# The paths are relative to the app root, the Compose project directory.", 1))
	return writeDevFile(root, "compose.services.yml", data)
}

// serviceEnv returns SERVICE_<NAME>_URL for each declared service, in declaration order.
func serviceEnv(list []services.Service) []string {
	env := make([]string, 0, len(list))
	for _, s := range list {
		env = append(env, services.URLEnv(s.Name)+"="+services.DevURL(s))
	}
	return env
}

// serviceOpenAPI returns the OpenAPI file of each service that declares one, relative
// to the app root with forward slashes.
func serviceOpenAPI(list []services.Service) []string {
	var files []string
	for _, s := range list {
		if s.OpenAPI != "" {
			files = append(files, path.Join(services.ContextDir(s.Name), filepath.ToSlash(s.OpenAPI)))
		}
	}
	return files
}

// goWatcher polls the Go and SQL sources of the API, like the managed .air.toml, and
// signals Reload after a debounce. The dev command uses it to restart the jobs worker
// after Air rebuilds the API.
type goWatcher struct {
	// Root is the application directory.
	Root string
	// Reload receives one value for each settled change. A full channel drops the value,
	// because one pending reload covers every change.
	Reload chan<- struct{}
	// Out receives scan failures.
	Out io.Writer
	// Interval and Debounce default to 500 ms and 300 ms.
	Interval, Debounce time.Duration
}

// skippedGoDirs mirrors exclude_dir of the managed api/.air.toml.
var skippedGoDirs = []string{"tmp", "node_modules", "internal/gen", "internal/store"}

func (w goWatcher) Run(ctx context.Context) {
	interval, debounce := w.Interval, w.Debounce
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if debounce <= 0 {
		debounce = 300 * time.Millisecond
	}
	out := w.Out
	if out == nil {
		out = io.Discard
	}
	previous, err := goSnapshot(w.Root)
	if err != nil {
		_, _ = fmt.Fprintf(out, "watch: %v\n", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := goSnapshot(w.Root)
			if err != nil {
				_, _ = fmt.Fprintf(out, "watch: %v\n", err)
				continue
			}
			if !sameStamps(previous, current) {
				timer.Reset(debounce)
			}
			previous = current
		case <-timer.C:
			select {
			case w.Reload <- struct{}{}:
			default:
			}
		}
	}
}

func goSnapshot(root string) (map[string]stamp, error) {
	api := filepath.Join(root, "api")
	result := map[string]stamp{}
	err := filepath.WalkDir(api, func(file string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(api, file)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			for _, skip := range skippedGoDirs {
				if relative == skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := filepath.Ext(relative)
		if ext != ".go" && ext != ".sql" || strings.HasSuffix(relative, "_test.go") {
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
		return nil, fmt.Errorf("scan API sources: %w\nTry: check the API source permissions", err)
	}
	return result, nil
}

func sameStamps(a, b map[string]stamp) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
