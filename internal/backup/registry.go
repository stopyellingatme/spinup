package backup

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Contract file from the M7 pre-seed (ADR 0011). Main loop only: units do not edit it.

// Env is what a backend needs to reach the database of one environment.
type Env struct {
	// App is the app name. It names the Compose project and /opt/<app>.
	App string
	// Environment is the resolved deploy environment.
	Environment project.Env
	// Config is the project configuration (spinup.toml).
	Config *project.Config
	// Root is the project root on the local machine.
	Root string
	// Remote runs commands on the VM.
	Remote remote.Runner
	// Progress receives the log output of the backend tools. Nil discards it.
	Progress io.Writer
}

// Factory builds a backend for one environment.
type Factory func(Env) (Backend, error)

var (
	backendsMu sync.Mutex
	backends   = map[string]Factory{}
)

// RegisterBackend adds a backend under name ([backups] backend in spinup.toml). Call it from init().
func RegisterBackend(name string, f Factory) {
	backendsMu.Lock()
	defer backendsMu.Unlock()
	if _, ok := backends[name]; ok {
		panic("backup: duplicate backend " + name)
	}
	backends[name] = f
}

// NewBackend builds the named backend. An empty name selects "pgbackrest".
func NewBackend(name string, e Env) (Backend, error) {
	if name == "" {
		name = "pgbackrest"
	}
	backendsMu.Lock()
	f, ok := backends[name]
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	backendsMu.Unlock()
	sort.Strings(names)
	if !ok {
		return nil, fmt.Errorf("unknown backup backend %q (known: %v)\nTry: set [backups] backend in spinup.toml to one of the known backends", name, names)
	}
	return f(e)
}

// Sample is one update of the backup metrics file (ADR 0011 §4). Zero fields are not written.
type Sample struct {
	// Kind and Success record a successful backup of that kind.
	Kind    Kind
	Success time.Time
	// Size is the size of that backup in bytes.
	Size int64
	// Verified records a finished restore test. VerifyOK is its result and RTO its duration.
	Verified time.Time
	VerifyOK bool
	RTO      time.Duration
}

// MetricsPath returns the backup metrics file on the VM.
func MetricsPath(app string) string { return "/opt/" + app + "/backups/metrics/backup.prom" }

// recordMetrics is replaced by the implementation of unit M7.2b (metrics.go).
var recordMetrics = func(context.Context, remote.Runner, string, Sample) error { return nil }

// RecordMetrics updates the backup metrics file on the VM. It is the only writer of the file.
func RecordMetrics(ctx context.Context, r remote.Runner, app string, s Sample) error {
	return recordMetrics(ctx, r, app, s)
}
