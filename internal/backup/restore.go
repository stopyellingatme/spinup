package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/maintain"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// RestoreOptions selects the target, destination, and destructive restore guards.
type RestoreOptions struct {
	Options
	// Target selects a backup or recovery time.
	Target Target
	// Into selects new or in-place restoration.
	Into string
	// IKnow permits replacement of the live database.
	IKnow bool
	// NoSafetyBackup skips the backup before replacement.
	NoSafetyBackup bool
	// Plan prints the steps without running them.
	Plan bool
	// Now overrides the maintenance window.
	Now bool
	// Confirm controls the protected environment guard.
	Confirm project.Confirm
	// Clock replaces the current time for maintenance checks.
	Clock func() time.Time
	// Sleep replaces the wait between smoke attempts.
	Sleep func(context.Context, time.Duration) error
	// HTTPGet replaces the deployer's HTTP client for smoke checks.
	HTTPGet func(context.Context, string) (int, []byte, error)
}

// Restore restores the database with the confirmation, lock, and deployment checks.
func Restore(ctx context.Context, o RestoreOptions) (err error) {
	if o.Into != "new" && o.Into != "in-place" {
		return fmt.Errorf("select restore destination: --into must be new or in-place\nTry: pass --into new or --into in-place")
	}
	if err = validateTarget(o.Target); err != nil {
		return err
	}
	backend, s, err := Open(o.Options)
	if err != nil {
		return err
	}
	out, errOut := writers(o.Options)
	if o.Plan {
		if o.Into == "new" {
			fmt.Fprintln(out, "Plan: restore into a new container at 127.0.0.1:5433")
			return nil
		}
		steps := []string{"Check --i-know, protected confirmation, and maintenance window", "Take the deploy lock", "Resolve and check the restore target"}
		if !o.NoSafetyBackup {
			steps = append(steps, "Take a safety backup")
		}
		stopText := "Stop api and web of both colors"
		if len(s.Config.Services) > 0 || slices.Contains(s.Config.Modules, "jobs") {
			stopText = "Stop the app services of both colors"
		}
		steps = append(steps, "Re-check the restore target", stopText)
		if s.Config.Backups.Backend == "pgdump" {
			steps = append(steps, "Restore into a staging database while postgres runs", "Rename the databases and retain the old database", "Print the dropdb command for the old database")
		} else {
			steps = append(steps, "Stop postgres", "Restore the live database", "Start postgres")
		}
		steps = append(steps, "Run migrate with the live release", "Start the live color", "Run the deploy smoke checks", "Release the deploy lock")
		for _, step := range steps {
			fmt.Fprintln(out, "Plan: "+step)
		}
		return nil
	}
	defer s.Close()
	if o.Into == "new" {
		if err = backend.Restore(ctx, o.Target, Into{New: true}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Restored database at 127.0.0.1:5433 on the VM\nConnect: spinup server ssh --env %s -L 5433:127.0.0.1:5433\n", s.Environment.Name)
		if err == nil {
			_, err = fmt.Fprintf(out, "OpenSSH tunnel: %s\n", restoreTunnel(s.Environment))
		}
		return err
	}
	if !o.IKnow {
		return fmt.Errorf("restore the live database: --i-know is required\nTry: inspect --plan, then pass --i-know")
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	inside, err := maintain.InWindow(s.Config.Maintenance.Window, clock())
	if err != nil {
		return err
	}
	if !inside && !o.Now {
		return fmt.Errorf("restore the live database: outside maintenance window %q (UTC)\nTry: wait for the window or pass --now", s.Config.Maintenance.Window)
	}
	if err = project.ConfirmProtected(s.Environment, "restore the live database", o.Confirm); err != nil {
		return fmt.Errorf("confirm database restore: %w\nTry: type the environment name in a terminal, or pass --yes in CI", err)
	}
	fmt.Fprintln(out, "Taking the deploy lock")
	lock, err := deploy.AcquireLock(ctx, s.Remote, s.App, deploy.LockOptions{Release: "backup-restore", Warn: errOut})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		fmt.Fprintln(out, "Releasing the deploy lock")
		err = errors.Join(err, lock.Release(cleanup))
	}()
	ctx, cancel := context.WithCancelCause(ctx)
	stopHeartbeat := restoreHeartbeat(ctx, cancel, lock)
	defer stopHeartbeat()
	defer cancel(nil)
	safety := ""
	defer func() {
		if err != nil && safety != "" {
			command := fmt.Sprintf("spinup backup restore --env %s --into in-place --label %s --i-know --no-safety-backup --now", s.Environment.Name, safety)
			if o.Dir != "" {
				command += " --dir " + remote.Quote(s.Root)
			}
			fmt.Fprintf(errOut, "Safety backup: %s\nRestore it with: %s\n", safety, command)
			err = fmt.Errorf("%w\nSafety backup: %s\nTry: %s", err, safety, command)
		}
	}()
	backend, state, err := observeRestore(s)
	if err != nil {
		return err
	}
	// Resolve every target before a safety backup or any service stop.
	target, err := checkRestoreTarget(ctx, backend, s.Config.Backups.Backend, o.Target)
	if err != nil {
		return err
	}
	rel, err := readLiveRelease(ctx, s)
	if err != nil {
		return err
	}
	if _, err = deploy.LoadEnvFile(s.Root, s.Environment.Name); err != nil {
		return err
	}
	if !o.NoSafetyBackup {
		fmt.Fprintln(out, "Taking a safety backup")
		var result Result
		var backupErr error
		if p, ok := backend.(*PGBackRest); ok {
			result, backupErr = p.backup(ctx, Full, true)
		} else {
			result, backupErr = backend.Backup(ctx, Full)
		}
		if backupErr != nil {
			return backupErr
		}
		safety = result.Label
		fmt.Fprintf(out, "Safety backup: %s\n", safety)
	}
	// A backend can apply retention while taking the safety backup. Never stop
	// services unless the frozen target remains available afterwards.
	if _, err = checkRestoreTarget(ctx, backend, s.Config.Backups.Backend, target); err != nil {
		return err
	}
	stopping, starting := false, false
	defer func() {
		if err != nil && stopping {
			err = restoreRecovery(ctx, s, rel, state, starting, err)
		}
	}()
	fmt.Fprintln(out, "Stopping "+restoreServiceText(s, rel)+" of both colors")
	stopServices := restoreServices(s, rel)
	// Preserve the legacy discovery of an optional web container.
	if len(stopServices) == 1 {
		stopServices = append(stopServices, "web")
	}
	for _, color := range []string{deploy.Blue, deploy.Green} {
		for _, service := range stopServices {
			result, runErr := s.Remote.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + s.App + "-" + color, "--filter", "label=com.docker.compose.service=" + service}})
			if runErr != nil {
				return restoreFailure("find "+color+" "+service, runErr)
			}
			if ids := strings.Fields(string(result.Stdout)); len(ids) > 0 {
				stopping = true
				if _, runErr = s.Remote.Run(ctx, remote.Cmd{Argv: append([]string{"docker", "stop"}, ids...)}); runErr != nil {
					return restoreFailure("stop "+color+" "+service, runErr)
				}
			}
		}
	}
	fmt.Fprintln(out, "Restoring the live database")
	stopping = true // The backend can stop postgres even when no app container ran.
	if state.backend != "pgbackrest" && state.backend != "pgdump" {
		state.touched = true // An unknown backend cannot prove that restarting is safe.
	}
	if err = backend.Restore(ctx, target, Into{InPlace: true}); err != nil {
		return err
	}
	dir := "/opt/" + s.App + "/releases/" + rel.Release
	fmt.Fprintln(out, "Running migrate with the live release")
	args := deploy.ComposeArgv(s.App+"-migrate", dir, []string{dir + "/compose.yml"}, "--profile", "migrate", "run", "--rm", "-T", "migrate")
	if _, err = s.Remote.Run(ctx, remote.Cmd{Argv: args, Stdout: errOut, Stderr: errOut}); err != nil {
		return restoreFailure("migrate the restored database", err)
	}
	fmt.Fprintln(out, "Starting the live color")
	args = liveCompose(s, rel, "up", "-d", "--no-deps", "--wait")
	starting = true
	if _, err = s.Remote.Run(ctx, remote.Cmd{Argv: args, Stdout: errOut, Stderr: errOut}); err != nil {
		return restoreFailure("start the live color", err)
	}
	fmt.Fprintln(out, "Running the deploy smoke checks")
	sleep := o.Sleep
	if sleep == nil {
		sleep = deploy.DefaultDeps().Sleep
	}
	for attempt := 0; attempt < 15; attempt++ {
		if err = restoreSmoke(ctx, s, rel, o.HTTPGet); err == nil {
			return nil
		}
		if attempt < 14 {
			if waitErr := sleep(ctx, 2*time.Second); waitErr != nil {
				return errors.Join(err, waitErr)
			}
		}
	}
	return err
}

func restoreFailure(step string, err error) error {
	return fmt.Errorf("%s: %w\nTry: inspect the server logs and restore the safety backup if needed", step, err)
}

func restoreHeartbeat(ctx context.Context, cancel context.CancelCauseFunc, lock *deploy.Lock) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := lock.Refresh(ctx); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return func() { close(done); <-stopped }
}

func readLiveRelease(ctx context.Context, s *Session) (deploy.Release, error) {
	var rel deploy.Release
	result, err := s.Remote.Run(ctx, remote.Cmd{Argv: []string{"cat", "/opt/" + s.App + "/current/release.json"}})
	if err != nil {
		return rel, restoreFailure("read the live release", err)
	}
	if err = json.Unmarshal(result.Stdout, &rel); err != nil {
		return rel, restoreFailure("decode the live release", err)
	}
	if !deploy.ValidReleaseID(rel.Release) || (rel.Color != deploy.Blue && rel.Color != deploy.Green) || len(rel.GitSHA) < 12 {
		return rel, fmt.Errorf("read the live release: invalid release, color, or git SHA\nTry: inspect current/release.json on the VM")
	}
	return rel, nil
}

func hasWeb(c *project.Config) bool {
	for _, m := range c.Modules {
		if m == "web" {
			return true
		}
	}
	return false
}

func restoreSmoke(ctx context.Context, s *Session, rel deploy.Release, get func(context.Context, string) (int, []byte, error)) error {
	if get == nil {
		get = deploy.DefaultDeps().HTTPGet
	}
	origin := strings.TrimRight(s.Environment.PublicURL, "/")
	if origin == "" {
		origin = "http://" + s.Environment.Host
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("run smoke checks: invalid public URL\nTry: correct public_url in spinup.toml")
	}
	origin = u.Scheme + "://" + u.Host
	envFile, err := deploy.LoadEnvFile(s.Root, s.Environment.Name)
	if err != nil {
		return err
	}
	for _, path := range append([]string{"/healthz"}, envFile.SmokeURLs...) {
		status, _, getErr := get(ctx, origin+path)
		if getErr != nil {
			return restoreFailure("smoke GET "+path, getErr)
		}
		if status < 200 || status > 299 {
			return fmt.Errorf("smoke GET %s: HTTP %d\nTry: inspect the live release logs", path, status)
		}
	}
	var body []byte
	if hasWeb(s.Config) {
		caddy, runErr := s.Remote.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + s.App, "--filter", "label=com.docker.compose.service=caddy"}})
		if runErr != nil {
			return restoreFailure("find Caddy for smoke check", runErr)
		}
		ids := strings.Fields(string(caddy.Stdout))
		if len(ids) != 1 {
			return fmt.Errorf("smoke GET /version: Caddy is not running\nTry: inspect the Caddy service")
		}
		result, runErr := s.Remote.Run(ctx, remote.Cmd{Argv: []string{"docker", "exec", ids[0], "wget", "-q", "-T", "5", "-O", "-", "http://api-" + rel.Color + ":8080/version"}})
		if runErr != nil {
			return restoreFailure("smoke GET /version", runErr)
		}
		body = result.Stdout
	} else {
		status, data, getErr := get(ctx, origin+"/version")
		if getErr != nil {
			return restoreFailure("smoke GET /version", getErr)
		}
		if status != 200 {
			return fmt.Errorf("smoke GET /version: HTTP %d\nTry: inspect the live API logs", status)
		}
		body = data
	}
	var version struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &version) != nil || !strings.Contains(version.Version, rel.GitSHA[:12]) {
		return fmt.Errorf("smoke GET /version: the version does not match live release %s\nTry: inspect the live API image", rel.Release)
	}
	return nil
}

func restoreTunnel(env project.Env) string {
	args := []string{"ssh", "-N", "-L", "5433:127.0.0.1:5433"}
	if config := os.Getenv("SPINUP_SSH_CONFIG"); config != "" {
		args = append(args, "-F", remote.Quote(config))
	}
	if env.Port != 0 {
		args = append(args, "-p", strconv.Itoa(env.Port))
	}
	host := env.Host
	if env.User != "" {
		host = env.User + "@" + host
	}
	return strings.Join(append(args, remote.Quote(host)), " ")
}
