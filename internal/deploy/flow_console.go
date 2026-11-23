package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/consoleinstall"
	"github.com/stopyellingatme/spinup/internal/project"
)

type consoleStack struct {
	selected    bool
	spec        consoleinstall.Spec
	base, query []byte
}

// loadConsole reads exactly the two selected Compose files, never local host scripts or .env files.
func loadConsole(root string, c *project.Config, env project.Env, origin string) (consoleStack, error) {
	var stack consoleStack
	if !slices.Contains(c.Modules, "console") {
		return stack, nil
	}
	stack.selected = true
	stack.spec = consoleinstall.Spec{App: c.App.Name, Env: env.Name, Origin: origin, Profile: "absent", Backend: "none", Jobs: slices.Contains(c.Modules, "jobs"), Image: "spinup-api:preflight"}
	if slices.Contains(c.Modules, "monitoring") {
		stack.spec.Profile = c.Monitoring.Profile
	}
	if slices.Contains(c.Modules, "backups") {
		stack.spec.Backend = c.Backups.Backend
		if stack.spec.Backend == "" {
			stack.spec.Backend = "pgbackrest"
		}
	}
	for _, file := range []struct {
		name string
		data *[]byte
	}{{"console/compose.console.yml", &stack.base}, {"console/compose.query.yml", &stack.query}} {
		path := filepath.Join(root, filepath.FromSlash(file.name))
		info, err := os.Lstat(path)
		if err != nil {
			return stack, preflightError("cannot read "+file.name+": "+err.Error(), "restore or merge the console pack files")
		}
		if !info.Mode().IsRegular() || info.Size() > 128<<10 {
			return stack, preflightError(file.name+" is not a bounded regular file", "restore or merge the console pack files")
		}
		*file.data, err = os.ReadFile(path)
		if err != nil {
			return stack, preflightError("cannot read "+file.name+": "+err.Error(), "restore or merge the console pack files")
		}
	}
	if err := consoleinstall.Validate(stack.spec, stack.base, stack.query); err != nil {
		return stack, preflightError(stripTry(err.Error()), "restore or merge the console pack files")
	}
	data, err := os.ReadFile(filepath.Join(root, "deploy", "Dockerfile.api"))
	if err != nil {
		return stack, preflightError("cannot read deploy/Dockerfile.api: "+err.Error(), "restore or merge the console build and copy instructions")
	}
	dockerfile := strings.ReplaceAll(string(data), "\\\n", " ")
	build := regexp.MustCompile(`(?m)^RUN\s+CGO_ENABLED=0\s+go build\s+[^\n]*-o /out/console\s+\./cmd/console\s*$`)
	copyInstruction := regexp.MustCompile(`(?m)^COPY\s+--from=build\s+/out/console\s+/console\s*$`)
	if !build.MatchString(dockerfile) || !copyInstruction.MatchString(dockerfile) {
		return stack, preflightError("deploy/Dockerfile.api has no console build or copy instruction", "restore or merge the console build and copy instructions")
	}
	return stack, nil
}

func (f *flow) consoleSpec(ctx context.Context) (consoleinstall.Spec, error) {
	spec := f.l.console.spec
	// Installation follows the recorded release, including a resumed deployment.
	if !ValidReleaseID(f.release) {
		return spec, fmt.Errorf("cannot install console: invalid recorded release ID\nTry: inspect deploy.state before resuming")
	}
	rel, err := readRelease(ctx, f.vm, f.l.app, f.release)
	if err != nil {
		return spec, err
	}
	if rel == nil {
		return spec, fmt.Errorf("cannot install console: the recorded release is missing\nTry: spinup deploy --resume --env %s", f.l.env.Name)
	}
	count := 0
	for _, image := range rel.Images {
		if image.Service == "api" {
			count++
			spec.Image = image.Ref
		}
	}
	if count != 1 || spec.Image == "" {
		return spec, fmt.Errorf("cannot install console: the recorded release has no unique resolved API image\nTry: inspect release.json before resuming")
	}
	return spec, nil
}

func (f *flow) installConsole(ctx context.Context) error {
	if !f.l.console.selected {
		return nil
	}
	spec, err := f.consoleSpec(ctx)
	if err != nil {
		return err
	}
	if err := consoleinstall.Install(ctx, f.vm, spec, f.l.console.base); err != nil {
		return err
	}
	// Collection must not delay deployment while it holds the deploy lock.
	if err := consoleinstall.StartSample(ctx, f.vm, f.l.app); err != nil {
		fmt.Fprintf(f.errOut, "Console sample delayed. Try: spinup status --env %s\n", f.l.env.Name)
	}
	return nil
}

func (f *flow) reconcileConsole(ctx context.Context) error {
	if !f.l.console.selected {
		return nil
	}
	spec, err := f.consoleSpec(ctx)
	if err != nil {
		return err
	}
	return consoleinstall.ReconcileQuery(ctx, f.vm, spec, f.l.console.query)
}

func (f *flow) sampleConsole(ctx context.Context) {
	if !f.l.console.selected {
		return
	}
	if err := consoleinstall.ForceRefresh(ctx, f.vm, f.l.app); err != nil {
		fmt.Fprintf(f.errOut, "Console sample delayed. Try: spinup status --env %s\n", f.l.env.Name)
	}
}

func (f *flow) planConsole() {
	if !f.l.console.selected {
		return
	}
	fmt.Fprintf(f.out, "  Install console/compose.console.yml, collect.sh, probe.conf, and .env in %s/console.\n", f.root())
	fmt.Fprintln(f.out, "  Preserve console/data and its prior sample. Serialize configuration with sample.lock.")
	fmt.Fprintln(f.out, "  Use the recorded API image. Start console with up -d --no-deps --wait console.")
	fmt.Fprintln(f.out, "  Verify Docker publishes only 127.0.0.1:7410. Install the user timer when available. Publish an initial sample.")
	if f.l.console.spec.Profile == "lite" || f.l.console.spec.Profile == "full" {
		fmt.Fprintln(f.out, "  After monitoring, start console-query with the base and query overlay files.")
	} else {
		fmt.Fprintln(f.out, "  Remove an obsolete relay by exact project and service labels.")
	}
	fmt.Fprintln(f.out, "  After post is recorded done, force a final sample with containers and successful history.")
}
