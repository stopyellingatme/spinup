package webtool

import (
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

// Tool describes the package manager and client generator of a web app.
type Tool struct{ manager, client string }

// For selects tooling from answers. Missing answers retain the original defaults.
func For(answers map[string]any) Tool {
	t := Tool{manager: "pnpm", client: "openapi-fetch"}
	if answers["package_manager"] == "bun" {
		t.manager = "bun"
	}
	if answers["api_client"] == "heyapi" {
		t.client = "heyapi"
	}
	return t
}

// FromLock selects tooling from a lock. A nil lock selects the original defaults.
func FromLock(lock *manifest.Lock) Tool {
	if lock == nil {
		return For(nil)
	}
	return For(lock.Answers)
}

// Name returns the package manager executable.
func (t Tool) Name() string { return t.manager }

// HeyAPI reports whether the app uses the generated heyapi client.
func (t Tool) HeyAPI() bool { return t.client == "heyapi" }

// Lockfile returns the package manager lockfile name, relative to web/.
func (t Tool) Lockfile() string {
	if t.manager == "bun" {
		return "bun.lock"
	}
	return "pnpm-lock.yaml"
}

// Install installs dependencies. Bun uses its warmed cache for offline installs.
// Bun has no --offline flag. Frozen installs use the exact cached lockfile packages.
func (t Tool) Install(frozen, offline bool) exec.Cmd {
	args := []string{"install"}
	if frozen {
		args = append(args, "--frozen-lockfile")
	}
	if offline && t.manager == "pnpm" {
		args = append(args, "--offline")
	}
	return exec.Cmd{Name: t.manager, Args: args}
}

// RefreshLock regenerates the lockfile after an upgrade changes dependencies.
func (t Tool) RefreshLock() exec.Cmd {
	c := exec.Cmd{Name: t.manager, Args: []string{"install", "--lockfile-only"}}
	if t.manager == "pnpm" {
		c.Args = append(c.Args, "--prefer-offline", "--ignore-scripts")
	}
	return c
}

// Run runs a package script with optional arguments.
func (t Tool) Run(script string, args ...string) exec.Cmd {
	argv := []string{script}
	if t.manager == "bun" {
		argv = []string{"run", script}
	}
	return exec.Cmd{Name: t.manager, Args: append(argv, args...)}
}

// Exec runs an installed binary without downloading a missing package.
func (t Tool) Exec(binary string, args ...string) exec.Cmd {
	if t.manager == "bun" {
		return exec.Cmd{Name: "bunx", Args: append([]string{"--no-install", binary}, args...)}
	}
	return exec.Cmd{Name: t.manager, Args: append([]string{"exec", binary}, args...)}
}

// Dlx runs a package executable, downloading it when necessary.
func (t Tool) Dlx(pkg string, args ...string) exec.Cmd {
	if t.manager == "bun" {
		return exec.Cmd{Name: "bunx", Args: append([]string{pkg}, args...)}
	}
	return exec.Cmd{Name: t.manager, Args: append([]string{"dlx", pkg}, args...)}
}

// InWeb runs a command in web/. It preserves the original pnpm command spelling.
func (t Tool) InWeb(root string, c exec.Cmd) exec.Cmd {
	if t.manager == "pnpm" {
		c.Dir = root
		c.Args = append([]string{"--dir", "web"}, c.Args...)
	} else {
		c.Dir = filepath.Join(root, "web")
	}
	return c
}

// ClientOutput returns the generated client path relative to web/.
func (t Tool) ClientOutput() string {
	if t.HeyAPI() {
		return "src/lib/api/client"
	}
	return "src/lib/api/schema.d.ts"
}

// Client returns the generator command. Output can be absolute for drift checks.
func (t Tool) Client(output string) exec.Cmd {
	if t.HeyAPI() {
		return t.Exec("openapi-ts", "--input", "../api/openapi/bundle.gen.yaml", "--output", output)
	}
	return t.Schema(output)
}

// Schema generates the shared types used by the resource pages.
func (t Tool) Schema(output string) exec.Cmd {
	return t.Exec("openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o", output)
}
