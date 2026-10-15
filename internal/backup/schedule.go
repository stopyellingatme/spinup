package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// ScheduleOptions selects the environment and the schedule operation.
type ScheduleOptions struct {
	Options
	// Plan prints the files and commands without connecting to the VM.
	Plan bool
	// Remove disables and deletes the timers and their services.
	Remove bool
}

const systemdProbe = `command -v systemctl >/dev/null 2>&1 && [ "$(cat /proc/1/comm)" = systemd ]`

// Schedule installs or removes the backup timers on the VM.
func Schedule(ctx context.Context, o ScheduleOptions) error {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("find backup schedule project: %w\nTry: run this command inside a spinup project", err)
	}
	config, err := project.Load(root)
	if err != nil {
		return fmt.Errorf("load backup schedule project: %w\nTry: correct spinup.toml", err)
	}
	if o.Env == "" {
		return fmt.Errorf("select backup environment: --env is missing\nTry: pass --env <environment>")
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return err
	}
	var files []vfs.File
	var pgconfig []byte
	backend := config.Backups.Backend
	if backend == "" {
		backend = "pgbackrest"
	}
	if !o.Remove {
		if !slices.Contains(config.Modules, "backups") {
			return fmt.Errorf("install backup schedule: the project does not use the backups module\nTry: add the backups module to the project")
		}
		files, err = scheduleFiles(config, env)
		if err != nil {
			return err
		}
		if backend == "pgbackrest" {
			pgconfig, err = ReadConfig(root)
			if err != nil {
				return err
			}
			retention, err := Retention(config)
			if err != nil {
				return err
			}
			pgconfig = WithRetention(pgconfig, retention)
		}
	}
	out, errOut := writers(o.Options)
	timers := scheduleTimers(config.App.Name)
	if o.Plan {
		if o.Remove {
			_, err = fmt.Fprintf(out, "sudo -n systemctl disable --now %s\nremove /etc/systemd/system/spinup-%s-backup-{full,diff,verify}.{service,timer}\nsudo -n systemctl daemon-reload\n", strings.Join(timers, " "), config.App.Name)
			return err
		}
		for _, f := range files {
			if _, err = fmt.Fprintf(out, "# %s\n%s\n", f.Path, f.Data); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(out, "# /opt/%s/backups/backend\n%s\nsudo -n systemctl daemon-reload\nsudo -n systemctl enable --now %s\n", config.App.Name, backend, strings.Join(timers, " "))
		return err
	}
	r := o.Remote
	if r == nil {
		host, err := remote.HostFromEnv(env)
		if err != nil {
			return err
		}
		runner := o.Runner
		if runner == nil {
			runner = runexec.OSRunner{}
		}
		r = remote.New(host, runner, remote.Options{})
		defer func() { _ = r.Close(context.Background()) }()
	}
	if _, err = r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-ec", systemdProbe}}); err != nil {
		return fmt.Errorf("install backup schedule: the VM needs systemctl and systemd as PID 1: %w\nTry: use a VM that boots systemd, or run deploy/backups/run.sh with another scheduler", err)
	}
	if !o.Remove {
		for _, file := range files {
			if !strings.HasSuffix(file.Path, ".timer") {
				continue
			}
			for line := range bytes.SplitSeq(file.Data, []byte("\n")) {
				if calendar, ok := strings.CutPrefix(string(line), "OnCalendar="); ok {
					if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"systemd-analyze", "calendar", "--", calendar}, Stderr: errOut}); err != nil {
						return fmt.Errorf("validate backup calendar %q: %w\nTry: correct [backups] schedule_full, schedule_diff, or schedule_verify in spinup.toml", calendar, err)
					}
				}
			}
		}
	}
	for _, argv := range schedulePrivileges(config.App.Name, o.Remove) {
		// A plain sudo -l can list a password-protected command. Inspect the
		// matching rule's authentication option instead of accepting exit 0.
		result, err := r.Run(ctx, remote.Cmd{Argv: append([]string{"sudo", "-n", "-ll", "--"}, argv...), Stderr: errOut})
		if err == nil && env.User != "root" && !sudoWithoutPassword(string(result.Stdout)) {
			err = fmt.Errorf("the matching sudo rule requires authentication")
		}
		if err != nil {
			hint := "re-run spinup server bootstrap for this environment"
			if o.Remove && argv[0] == "rm" {
				hint = "re-run spinup server bootstrap for this environment, which permits rm -f of these six unit files"
			}
			return fmt.Errorf("check backup schedule sudo access (%s): %w\nTry: %s", strings.Join(argv, " "), err, hint)
		}
	}

	run := func(argv ...string) error {
		_, err := r.Run(ctx, remote.Cmd{Argv: argv, Stderr: errOut})
		if err != nil {
			return fmt.Errorf("change backup schedule (%s): %w\nTry: check SSH access and passwordless sudo, then repeat backup schedule", strings.Join(argv, " "), err)
		}
		return nil
	}
	if o.Remove {
		return run(append([]string{"sh", "-ec", scheduleRemoveScript, "spinup-remove", "/etc/systemd/system"}, timers...)...)
	}
	base := "/opt/" + config.App.Name + "/backups"
	if err = ensureMetricsDir(ctx, r, config.App.Name); err != nil {
		return err
	}
	upload := func(data []byte, dest string, mode os.FileMode) error {
		if err := r.Upload(ctx, data, dest, mode); err != nil {
			return fmt.Errorf("upload backup schedule %s: %w\nTry: check the deploy user's permissions and disk space", dest, err)
		}
		return nil
	}
	// The executable precedes the units, so a newly enabled timer always has its script.
	for _, f := range files {
		if f.Path == "deploy/backups/run.sh" {
			if err = upload(f.Data, base+"/run.sh", 0755); err != nil {
				return err
			}
		}
	}
	if err = upload([]byte(backend+"\n"), base+"/backend", 0644); err != nil {
		return err
	}
	if backend == "pgbackrest" {
		if err = upload(pgconfig, RemoteConfigPath(config.App.Name), 0600); err != nil {
			return err
		}
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "deploy/systemd/") {
			continue
		}
		name := path.Base(f.Path)
		staged := base + "/" + name
		if err = upload(f.Data, staged, 0644); err != nil {
			return err
		}
		if err = run("sudo", "-n", "install", "-m", "0644", staged, "/etc/systemd/system/"+name); err != nil {
			return err
		}
		if err = run("rm", "-f", staged); err != nil {
			return err
		}
	}
	if err = run("sudo", "-n", "systemctl", "daemon-reload"); err != nil {
		return err
	}
	for _, timer := range timers {
		if err = run("sudo", "-n", "systemctl", "enable", "--now", timer); err != nil {
			return err
		}
	}
	_, err = r.Run(ctx, remote.Cmd{Argv: append([]string{"systemctl", "list-timers", "--all", "--no-pager"}, timers...), Stdout: out, Stderr: errOut})
	if err != nil {
		return fmt.Errorf("list backup timers: %w\nTry: run systemctl list-timers on the VM", err)
	}
	return nil
}

func scheduleTimers(app string) []string {
	timers := []string{}
	for _, kind := range []string{"full", "diff", "verify"} {
		timers = append(timers, "spinup-"+app+"-backup-"+kind+".timer")
	}
	return timers
}

func scheduleFiles(c *project.Config, env project.Env) ([]vfs.File, error) {
	user := env.User
	if user == "" {
		user = "deploy"
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]*\$?$`).MatchString(user) {
		return nil, fmt.Errorf("render backup services: invalid deploy user %q\nTry: set a Unix user name in the environment configuration", user)
	}
	for _, value := range []string{c.Backups.ScheduleFull, c.Backups.ScheduleDiff, c.Backups.ScheduleVerify} {
		if (value != "" && strings.TrimSpace(value) == "") || strings.ContainsAny(value, "\r\n\x00\\%") {
			return nil, fmt.Errorf("render backup timer: invalid calendar value\nTry: use a single systemd OnCalendar expression without escapes")
		}
	}
	if c.Backups.Backend != "" && c.Backups.Backend != "pgbackrest" && c.Backups.Backend != "pgdump" {
		return nil, fmt.Errorf("render backup script: unknown backend %q\nTry: set [backups] backend to pgbackrest or pgdump", c.Backups.Backend)
	}
	retention, err := Retention(c)
	if err != nil {
		return nil, err
	}
	if retention == 0 {
		retention = 4
		if c.Backups.Backend == "pgdump" {
			retention = 7
		}
	}
	pack, err := templates.Load(templates.Embedded(), "backups")
	if err != nil {
		return nil, err
	}
	selected := []templates.FileSpec{}
	for _, file := range pack.Files {
		if strings.HasPrefix(file.Path, "deploy/systemd/") || file.Path == "deploy/backups/run.sh" {
			selected = append(selected, file)
		}
	}
	pack.Files = selected
	plan, err := render.Render(render.Input{Packs: []*templates.Pack{pack}, Answers: map[string]any{"backup_retention": fmt.Sprint(retention), "app_name": c.App.Name, "deploy_user": user, "schedule_full": c.Backups.ScheduleFull, "schedule_diff": c.Backups.ScheduleDiff, "schedule_verify": c.Backups.ScheduleVerify}})
	if err != nil {
		return nil, err
	}
	return plan.Files, nil
}

// Queries use sudo -l so the permission check has no systemd side effects.
func schedulePrivileges(app string, remove bool) [][]string {
	commands := [][]string{{"systemctl", "daemon-reload"}}
	timers := scheduleTimers(app)
	if remove {
		for _, timer := range timers {
			commands = append(commands, []string{"systemctl", "disable", "--now", timer})
		}
		args := []string{"rm", "-f"}
		for _, timer := range timers {
			args = append(args, "/etc/systemd/system/"+timer, "/etc/systemd/system/"+strings.TrimSuffix(timer, ".timer")+".service")
		}
		return append(commands, args)
	}
	for _, timer := range timers {
		commands = append(commands, []string{"systemctl", "enable", "--now", timer})
		for _, name := range []string{strings.TrimSuffix(timer, ".timer") + ".service", timer} {
			commands = append(commands, []string{"install", "-m", "0644", "/opt/" + app + "/backups/" + name, "/etc/systemd/system/" + name})
		}
	}
	return commands
}

// Missing timers are harmless. Cleanup and reload still run after a disable error.
const scheduleRemoveScript = `dir=$1
shift
failed=0
for timer do
    if ! sudo -n systemctl disable --now "$timer"; then
        state=$(systemctl show --property=LoadState --value "$timer") || :
        [ "$state" = not-found ] || failed=1
    fi
done
for timer do
    set -- "$@" "$dir/$timer" "$dir/${timer%.timer}.service"
    shift
done
sudo -n rm -f "$@" || failed=1
sudo -n systemctl daemon-reload || failed=1
exit "$failed"`

func sudoWithoutPassword(listing string) bool {
	allowed := false
	for _, field := range strings.FieldsFunc(listing, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == ',' }) {
		switch field {
		case "!authenticate":
			allowed = true
		case "authenticate":
			allowed = false
		}
	}
	return allowed
}
