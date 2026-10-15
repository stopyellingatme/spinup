package deploy

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Hook is a named action registered by a deployment integration.
type Hook struct {
	Name string
	Run  func(context.Context, *HookContext) error
}

// HookContext supplies the environment, release, remote connection, and output.
type HookContext struct {
	Env          project.Env
	App, Release string
	Remote       remote.Runner
	Out          io.Writer
	// Results lets a hook report facts the deployer records in release.json. The backup hook
	// sets ResultBackupTime and ResultMigrationsSafe (used by rollback --with-db-plan).
	Results map[string]string
}

// Keys of HookContext.Results that the deployer records.
const (
	ResultBackupTime     = "pre_deploy_backup_time" // RFC 3339, UTC
	ResultMigrationsSafe = "migrations_safe"        // "true" or "false"
)

type hookRegistry struct {
	mu    sync.Mutex
	hooks map[string]Hook
}

var preDeploy = &hookRegistry{}
var postRelease = &hookRegistry{}

func (r *hookRegistry) register(h Hook) {
	if strings.TrimSpace(h.Name) == "" || h.Run == nil {
		panic("deploy hook needs a nonempty name and Run")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hooks == nil {
		r.hooks = map[string]Hook{}
	}
	if _, ok := r.hooks[h.Name]; ok {
		panic("duplicate deploy hook: " + h.Name)
	}
	r.hooks[h.Name] = h
}

func (r *hookRegistry) list() []Hook {
	r.mu.Lock()
	defer r.mu.Unlock()
	hooks := make([]Hook, 0, len(r.hooks))
	for _, h := range r.hooks {
		hooks = append(hooks, h)
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })
	return hooks
}

// RegisterPreDeploy registers a hook that runs before deployment.
// It panics for an empty or duplicate name, or a nil Run function.
func RegisterPreDeploy(h Hook) { preDeploy.register(h) }

// RegisterPostRelease registers a hook that runs after release publication.
// It panics for an empty or duplicate name, or a nil Run function.
func RegisterPostRelease(h Hook) { postRelease.register(h) }

// PreDeployHooks returns a copy of the pre-deploy hooks, sorted by name.
func PreDeployHooks() []Hook { return preDeploy.list() }

// PostReleaseHooks returns a copy of the post-release hooks, sorted by name.
func PostReleaseHooks() []Hook { return postRelease.list() }

// RunHooks runs hooks in name order and stops at the first failure.
// It sorts a copy and does not change the supplied slice.
func RunHooks(ctx context.Context, hooks []Hook, c *HookContext) error {
	ordered := append([]Hook(nil), hooks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for _, h := range ordered {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("deploy hook %q stopped: %w\nTry: retry the deploy", h.Name, err)
		}
		if h.Run == nil {
			return fmt.Errorf("deploy hook %q has no Run function\nTry: correct the hook registration", h.Name)
		}
		if err := h.Run(ctx, c); err != nil {
			return fmt.Errorf("deploy hook %q failed: %w\nTry: correct the hook failure and retry the deploy", h.Name, err)
		}
	}
	return nil
}
