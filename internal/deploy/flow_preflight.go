package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// requiredSecrets are the secrets that the deploy pack reads (docs/deploy.md).
var requiredSecrets = []string{"DATABASE_URL", "POSTGRES_PASSWORD"}

// RequiredSecrets returns the secret names that a deploy of the project needs: the deploy pack
// secrets, plus GRAFANA_ADMIN_PASSWORD when the monitoring stack is active. Provisioning uses it
// to generate the missing values (ADR 0017).
func RequiredSecrets(root string, config *project.Config) ([]string, error) {
	m, err := loadMonitoring(root, config)
	if err != nil {
		return nil, err
	}
	return requiredFor(m), nil
}

func requiredFor(m monitoringStack) []string {
	need := slices.Clone(requiredSecrets)
	if m.skip == "" {
		need = append(need, "GRAFANA_ADMIN_PASSWORD")
	}
	return need
}

// local is the result of the preflight: the project, the environment, and the commit.
type local struct {
	root       string
	app        string
	env        project.Env
	envFile    EnvFile
	sha        string
	secrets    map[string]string
	web        bool
	sidecars   []services.Service // the [[services]] of spinup.toml (ADR 0022 §2)
	jobs       bool               // the jobs module is on: compose.services.yml also feeds the worker
	otel       bool               // the monitoring module is on with a profile other than none
	files      []releaseFile
	monitoring monitoringStack
	migrations []migration
	origin     string // PUBLIC_ORIGIN
	site       string // PUBLIC_HOST
	behavior   int    // SPINUP_BEHAVIOR_VERSION
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func preflightError(why, try string) error {
	return &ui.Error{What: "Deploy preflight failed", Why: why, Try: try}
}

// preflight runs the local checks of step 1. It changes nothing.
// confirm is false for --plan, which changes nothing and needs no confirmation.
func preflight(ctx context.Context, o Options, d Deps, out io.Writer, confirm bool) (*local, error) {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return nil, preflightError("spinup.toml was not found", "run spinup deploy inside a spinup project, or pass --dir")
	}
	config, err := project.Load(root)
	if err != nil {
		return nil, preflightError(stripTry(err.Error()), "correct spinup.toml")
	}
	if err := config.Validate(); err != nil {
		return nil, preflightError("spinup.toml is not valid: "+stripTry(err.Error()), "correct spinup.toml")
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return nil, preflightError(stripTry(err.Error()), "pass --env <name> with an environment of spinup.toml")
	}
	if env.Host == "" {
		return nil, preflightError(fmt.Sprintf("environment %q has no host", env.Name), "set host in [env."+env.Name+"] of spinup.toml")
	}
	l := &local{root: root, app: config.App.Name, env: env, behavior: config.EffectiveBehaviorVersion(), sidecars: config.Services}
	l.jobs = slices.Contains(config.Modules, "jobs")
	// The same rule as the OTel block of the api service in deploy/compose.yml.
	l.otel = slices.Contains(config.Modules, "monitoring") && config.Monitoring.Profile != "none"
	if l.origin, l.site, err = publicOrigin(env.PublicURL, env.Host); err != nil {
		return nil, preflightError(stripTry(err.Error()), "set public_url in [env."+env.Name+"] of spinup.toml")
	}
	if l.monitoring, err = loadMonitoring(root, config); err != nil {
		return nil, err
	}
	if l.monitoring.skip == "" {
		if _, err := l.monitoring.env("", "", env.PublicURL); err != nil {
			return nil, err
		}
	}

	// A clean tree makes the release reproducible from its git SHA.
	status, err := d.Exec.Run(ctx, runexec.Cmd{Name: "git", Args: []string{"status", "--porcelain"}, Dir: root})
	if err != nil {
		return nil, preflightError("git status failed: "+stripTry(err.Error()), "run spinup deploy inside a git repository with at least one commit")
	}
	if len(strings.TrimSpace(string(status.Stdout))) > 0 {
		if !o.AllowDirty {
			return nil, preflightError("the git tree has uncommitted changes", "commit the changes, or pass --allow-dirty")
		}
		fmt.Fprintln(out, "Warning: the git tree has uncommitted changes (--allow-dirty)")
	}
	head, err := d.Exec.Run(ctx, runexec.Cmd{Name: "git", Args: []string{"rev-parse", "HEAD"}, Dir: root})
	if err != nil {
		return nil, preflightError("git rev-parse HEAD failed: "+stripTry(err.Error()), "commit the work, and run spinup deploy again")
	}
	l.sha = strings.TrimSpace(string(head.Stdout))
	if !shaPattern.MatchString(l.sha) {
		return nil, preflightError(fmt.Sprintf("git returned the invalid commit %q", l.sha), "check the git repository")
	}

	if err := d.Check(ctx, root, out); err != nil {
		return nil, preflightError("spinup check found problems: "+stripTry(err.Error()), "correct the problems that spinup check reports")
	}
	findings, err := d.Lint(root)
	if err != nil {
		return nil, preflightError("db lint failed: "+stripTry(err.Error()), "check the migration files")
	}
	for _, f := range findings {
		fmt.Fprintf(out, "%s:%d: %s: %s\n", f.File, f.Line, f.Rule, f.Message)
	}
	if len(findings) > 0 {
		if !o.AllowDestructive {
			return nil, preflightError(fmt.Sprintf("db lint found %d unsafe migration changes", len(findings)), "split the contract step into a later release, or pass --allow-destructive")
		}
		fmt.Fprintf(out, "Warning: deploying %d unsafe migration changes (--allow-destructive)\n", len(findings))
	}

	if l.envFile, err = LoadEnvFile(root, env.Name); err != nil {
		return nil, preflightError(stripTry(err.Error()), "create deploy/environments/"+env.Name+".toml")
	}
	if l.files, err = deployFiles(root); err != nil {
		return nil, preflightError(stripTry(err.Error()), "restore the deploy pack files with spinup restore")
	}
	_, statErr := os.Stat(filepath.Join(root, "deploy", "Dockerfile.web"))
	l.web = statErr == nil
	if l.migrations, err = migrations(root); err != nil {
		return nil, preflightError(stripTry(err.Error()), "check api/db/migrations")
	}

	if l.secrets, err = d.Decrypt(ctx, root, env.Name); err != nil {
		return nil, preflightError("the secrets of "+env.Name+" cannot be decrypted: "+stripTry(err.Error()), "run spinup secrets init --env "+env.Name+", or check the key in ~/.config/spinup/keys")
	}
	for _, name := range requiredFor(l.monitoring) {
		if _, ok := l.secrets[name]; !ok {
			return nil, preflightError("the secret "+name+" is missing in secrets/"+env.Name+".enc.yaml", "run spinup secrets set --env "+env.Name+" "+name)
		}
	}
	// A service mounts each of its secrets as one file. A missing file fails the start of
	// the color, so check the names before anything changes on the VM.
	for _, s := range l.sidecars {
		for _, name := range s.Secrets {
			if _, ok := l.secrets[name]; !ok {
				return nil, preflightError("the secret "+name+" of service "+s.Name+" is missing in secrets/"+env.Name+".enc.yaml", "run spinup secrets set --env "+env.Name+" "+name+", or remove it from secrets of the service in spinup.toml")
			}
		}
	}

	if confirm {
		if err := project.ConfirmProtected(env, "deploy", o.Confirm); err != nil {
			return nil, &ui.Error{What: "Deploy not confirmed", Why: stripTry(err.Error()), Try: "pass --confirm-env " + env.Name + ", type the environment name, or pass --yes in CI"}
		}
	}
	return l, nil
}

// targets returns the image targets of the release: api, web when present, then the
// sidecar services of spinup.toml. It is the order of the build and of release.json.
func (l *local) targets() []string {
	all := ColorServices(l.web, sidecarSpecs(l.sidecars))
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.Name
	}
	return out
}

// sidecar reports whether name is a sidecar service of spinup.toml.
func (l *local) sidecar(name string) bool {
	return slices.ContainsFunc(l.sidecars, func(s services.Service) bool { return s.Name == name })
}

// sidecarSpecs returns the release records of the declared services, without image refs.
func sidecarSpecs(list []services.Service) []ReleaseService {
	if len(list) == 0 {
		return nil
	}
	out := make([]ReleaseService, len(list))
	for i, s := range list {
		out[i] = ReleaseService{Name: s.Name, Port: s.Port, Health: s.Health}
	}
	return out
}

// envPairs returns the variables of the release .env file in a fixed order.
// Only api, web, and the worker (WORKER_REPLICAS, with the jobs module) read them. compose.services.yml holds the literal values of the
// sidecar services, because rollback changes only SPINUP_COLOR in .env.
func (l *local) envPairs(color string, images []Image, uid, gid string) [][2]string {
	ref := func(service string) string {
		for _, img := range images {
			if img.Service == service {
				return img.Ref
			}
		}
		return ""
	}
	limit := func(service string) ResourceLimits {
		r := l.envFile.Resources[service]
		if r.CPUs == "" {
			r.CPUs = "1.0"
		}
		if r.Memory == "" {
			r.Memory = "512m"
		}
		return r
	}
	builtin := ColorServices(l.web, nil)
	pairs := [][2]string{{"SPINUP_COLOR", color}}
	for _, s := range builtin {
		pairs = append(pairs, [2]string{strings.ToUpper(s.Name) + "_IMAGE", ref(s.Name)})
	}
	pairs = append(pairs,
		[2]string{"PUBLIC_ORIGIN", l.origin},
		[2]string{"PUBLIC_HOST", l.site},
		[2]string{"SPINUP_BEHAVIOR_VERSION", strconv.Itoa(max(l.behavior, 1))},
		[2]string{"DEPLOY_UID", uid},
		[2]string{"DEPLOY_GID", gid},
	)
	for _, s := range builtin {
		up := strings.ToUpper(s.Name)
		lim := limit(s.Name)
		pairs = append(pairs, [2]string{up + "_REPLICAS", strconv.Itoa(l.replicas(s.Name))}, [2]string{up + "_CPUS", lim.CPUs}, [2]string{up + "_MEMORY", lim.Memory})
	}
	if l.jobs {
		// compose.yml reads ${WORKER_REPLICAS:-1} for the worker service.
		pairs = append(pairs, [2]string{"WORKER_REPLICAS", strconv.Itoa(l.replicas("worker"))})
	}
	pg := l.envFile.Resources["postgres"]
	if pg.CPUs == "" {
		pg.CPUs = "1.0"
	}
	if pg.Memory == "" {
		pg.Memory = "1g"
	}
	return append(pairs, [2]string{"POSTGRES_CPUS", pg.CPUs}, [2]string{"POSTGRES_MEMORY", pg.Memory})
}

// replicas returns the replica count of a service in deploy/environments/<env>.toml.
// A missing or smaller value gives 1.
func (l *local) replicas(service string) int {
	return max(l.envFile.Replicas[service], 1)
}

// sameSHA reports whether a release ID belongs to the commit.
func sameSHA(release, sha string) bool {
	return ValidReleaseID(release) && strings.HasPrefix(sha, strings.SplitN(release, "-r", 2)[0][17:])
}

// secretNames returns the sorted secret names.
func secretNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
