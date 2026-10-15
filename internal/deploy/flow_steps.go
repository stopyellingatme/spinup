package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/build"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/secrets"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Deploy runs spinup deploy: a new release, a --plan, or a --resume (ADR 0009 §2).
// Each step is journaled in /opt/<app>/deploy.state and is safe to repeat.
func Deploy(ctx context.Context, o Options, d Deps) error {
	o.fill()
	d.fill()
	out := &redactor{w: o.Out}
	errOut := &redactor{w: o.ErrOut}
	l, err := preflight(ctx, o, d, out, !o.Plan)
	if err != nil {
		return err
	}
	values := redactValues(l.secrets)
	out.values, errOut.values = values, values
	f := &flow{o: o, d: d, l: l, out: out, errOut: errOut, redact: values}
	if o.Plan {
		return f.plan(ctx)
	}
	return f.run(ctx)
}

type flow struct {
	o      Options
	d      Deps
	l      *local
	out    io.Writer
	errOut io.Writer
	redact []string

	vm       remote.Runner
	lock     *Lock
	journal  *Journal
	release  string
	images   []Image
	color    string
	uid, gid string
	refresh  time.Time
}

func (f *flow) root() string       { return "/opt/" + f.l.app }
func (f *flow) releaseDir() string { return f.root() + "/releases/" + f.release }

// cmd runs a remote command with the secret values redacted.
func (f *flow) cmd(ctx context.Context, stream bool, argv ...string) (runexec.Result, error) {
	c := remote.Cmd{Argv: argv, Redact: f.redact}
	if stream {
		c.Stdout, c.Stderr = f.out, f.out
	}
	return f.vm.Run(ctx, c)
}

func (f *flow) run(ctx context.Context) (err error) {
	if !f.o.Resume {
		if err := f.prepare(ctx); err != nil {
			return err
		}
	}
	if err := f.connect(ctx); err != nil {
		return err
	}
	defer func() { _ = f.vm.Close(context.WithoutCancel(ctx)) }()

	f.lock, err = AcquireLock(ctx, f.vm, f.l.app, LockOptions{Owner: f.d.User(), Release: f.release, TTL: f.o.LockTTL, Now: f.d.Now, Warn: f.errOut})
	if err != nil {
		return err
	}
	f.refresh = f.d.Now()
	defer func() {
		if rerr := f.lock.Release(context.WithoutCancel(ctx)); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()

	start := 0
	if f.o.Resume {
		if start, err = f.resume(ctx); err != nil || start < 0 {
			return err
		}
	} else if err := f.begin(ctx); err != nil {
		return err
	}

	steps := []struct {
		name string
		run  func(context.Context) (string, error)
	}{
		{"hooks", f.hooks}, {"transport", f.transport}, {"upload", f.upload}, {"edge", f.edge},
		{"migrate", f.migrate}, {"switch", f.doSwitch}, {"smoke", f.smoke}, {"record", f.record}, {"post", f.post},
	}
	for _, s := range steps {
		if slices.Index(Steps, s.name) < start {
			continue
		}
		if err := f.keepLock(ctx); err != nil {
			return deployError(s.name, f.l.env.Name, err)
		}
		if err := f.journal.Start(ctx, s.name); err != nil {
			return deployError(s.name, f.l.env.Name, err)
		}
		detail, serr := s.run(ctx)
		if serr != nil {
			derr := deployError(s.name, f.l.env.Name, serr)
			if jerr := f.journal.Fail(context.WithoutCancel(ctx), s.name, errors.New(redactString(serr.Error(), f.redact))); jerr != nil {
				return errors.Join(derr, jerr)
			}
			return derr
		}
		if err := f.journal.Done(ctx, s.name, redactString(detail, f.redact)); err != nil {
			return deployError(s.name, f.l.env.Name, err)
		}
	}
	fmt.Fprintf(f.out, "Deployed release %s to %s (%s color)\n", f.release, f.l.env.Name, f.color)
	return nil
}

func redactString(s string, values []string) string {
	return (&redactor{values: values}).redact(s)
}

// keepLock refreshes the lock when a third of its TTL has passed.
func (f *flow) keepLock(ctx context.Context) error {
	if f.d.Now().Sub(f.refresh) < f.o.LockTTL/3 {
		return nil
	}
	if err := f.lock.Refresh(ctx); err != nil {
		return err
	}
	f.refresh = f.d.Now()
	return nil
}

// prepare chooses the release ID and builds the images (step 2). It runs before the lock.
func (f *flow) prepare(ctx context.Context) error {
	switch {
	case f.o.Tag != "":
		if !sameSHA(f.o.Tag, f.l.sha) {
			return &ui.Error{What: "Invalid --tag", Why: fmt.Sprintf("%q is not a release ID of HEAD (%s)", f.o.Tag, f.l.sha[:12]), Try: "pass --tag <YYYYMMDDTHHMMSSZ>-<12-char HEAD SHA>"}
		}
		f.release = f.o.Tag
	case f.o.SkipBuild:
		tag, err := f.latestTag(ctx)
		if err != nil {
			return err
		}
		f.release = tag
	default:
		id, err := ReleaseID(f.d.Now(), f.l.sha)
		if err != nil {
			return err
		}
		f.release = id
	}
	if f.o.SkipBuild {
		imgs, err := f.existingImages()
		if err != nil {
			return err
		}
		f.images = imgs
		fmt.Fprintf(f.out, "Using the images of release %s (--skip-build)\n", f.release)
		return nil
	}
	fmt.Fprintf(f.out, "Building release %s\n", f.release)
	built, err := f.d.Build(ctx, f.d.Exec, build.ImageOptions{
		Root: f.l.root, App: f.l.app, Tag: f.release, Platform: f.o.Platform,
		Push: f.l.env.Transport == TransportRegistry, Targets: f.l.services(), PublicOrigin: f.l.origin,
	})
	if err != nil {
		return err
	}
	for i, img := range built {
		if i >= len(f.l.services()) {
			break
		}
		f.images = append(f.images, Image{Service: f.l.services()[i], Tag: img.Ref, Digest: img.Digest})
	}
	if len(f.images) != len(f.l.services()) {
		return &ui.Error{What: "Cannot build images", Why: "the build returned the wrong number of images", Try: "run spinup build --image and check the output"}
	}
	return nil
}

// latestTag finds the newest local release ID of HEAD for --skip-build without --tag.
func (f *flow) latestTag(ctx context.Context) (string, error) {
	if f.l.env.Transport == TransportRegistry {
		return "", &ui.Error{What: "Cannot use --skip-build", Why: "the registry transport needs the release ID of the pushed images", Try: "pass --tag <release-id> with --skip-build"}
	}
	result, err := f.d.Exec.Run(ctx, runexec.Cmd{Name: "docker", Args: []string{"image", "ls", "--format", "{{.Tag}}", f.l.app + "-api"}})
	if err != nil {
		return "", &ui.Error{What: "Cannot use --skip-build", Why: stripTry(err.Error()), Try: "check the local Docker engine", Err: err}
	}
	best := ""
	for _, tag := range strings.Fields(string(result.Stdout)) {
		if sameSHA(tag, f.l.sha) && tag > best {
			best = tag
		}
	}
	if best == "" {
		return "", &ui.Error{What: "Cannot use --skip-build", Why: "no local " + f.l.app + "-api image has a release ID of HEAD " + f.l.sha[:12], Try: "deploy without --skip-build"}
	}
	return best, nil
}

// existingImages returns the image references of the release for --skip-build.
func (f *flow) existingImages() ([]Image, error) {
	prefix := ""
	if f.l.env.Transport == TransportRegistry {
		owner, err := moduleOwner(filepath.Join(f.l.root, "api", "go.mod"))
		if err != nil {
			return nil, err
		}
		prefix = "ghcr.io/" + owner + "/"
	}
	var out []Image
	for _, s := range f.l.services() {
		out = append(out, Image{Service: s, Tag: prefix + f.l.app + "-" + s + ":" + f.release})
	}
	return out, nil
}

// moduleOwner returns the GitHub owner of the API module, as spinup build --push uses it.
func moduleOwner(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", &ui.Error{What: "Cannot find the registry owner", Why: err.Error(), Try: "check api/go.mod", Err: err}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			parts := strings.Split(strings.Trim(fields[1], "\"`"), "/")
			if len(parts) >= 3 && parts[0] == "github.com" && parts[1] != "" {
				return strings.ToLower(parts[1]), nil
			}
			break
		}
	}
	return "", &ui.Error{What: "Cannot find the registry owner", Why: "the API module path does not start with github.com/<owner>", Try: "deploy without --skip-build"}
}

// probeScript checks the app directory and prints the IDs of the deploy user.
const probeScript = `if [ ! -d "$1" ] || [ ! -w "$1" ]; then exit 3; fi
id -u
id -g`

// connect opens the SSH connection and checks the app directory on the VM (step 3).
func (f *flow) connect(ctx context.Context) error {
	vm, err := f.d.Dial(ctx, f.l.env, f.o.AcceptNewHostKey)
	if err != nil {
		return &ui.Error{What: "Cannot connect to " + f.l.env.Name, Why: stripTry(err.Error()), Try: "check host, user, and port in spinup.toml and your SSH config", Err: err}
	}
	f.vm = vm
	result, err := f.cmd(ctx, false, "sh", "-c", probeScript, "spinup-probe", f.root())
	if err != nil {
		_ = vm.Close(context.WithoutCancel(ctx))
		if result.ExitCode == 3 {
			return &ui.Error{What: "The VM is not ready", Why: f.root() + " is missing or not writable by the deploy user", Try: "run spinup server bootstrap --env " + f.l.env.Name}
		}
		return &ui.Error{What: "Cannot connect to " + f.l.env.Name, Why: stripTry(err.Error()), Try: "check host, user, and port in spinup.toml and your SSH config", Err: err}
	}
	ids := strings.Fields(string(result.Stdout))
	if len(ids) != 2 || !isNumber(ids[0]) || !isNumber(ids[1]) {
		_ = vm.Close(context.WithoutCancel(ctx))
		return &ui.Error{What: "Cannot read the deploy user", Why: "id -u and id -g returned unexpected output", Try: "check the shell of the deploy user"}
	}
	f.uid, f.gid = ids[0], ids[1]
	return nil
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// rotateJournalScript keeps the previous journal as deploy.state.prev.
const rotateJournalScript = `if [ -e "$1" ]; then mv -f -- "$1" "$1.prev"; fi`

// begin starts the journal of a new release and records the local steps.
func (f *flow) begin(ctx context.Context) error {
	entries, err := ReadJournal(ctx, f.vm, f.l.app)
	if err != nil {
		return err
	}
	if n := len(entries); n > 0 {
		last := entries[n-1].Release
		if step, ok := NextStep(entries, Steps, last); ok && last != f.release {
			fmt.Fprintf(f.errOut, "Warning: the deploy of release %s stopped before step %s. This deploy replaces it. To continue it instead, run spinup deploy --resume\n", last, step)
		}
	}
	if _, err := f.cmd(ctx, false, "sh", "-c", rotateJournalScript, "spinup-journal", f.root()+"/deploy.state"); err != nil {
		return fmt.Errorf("rotate the deploy journal: %w", err)
	}
	f.journal = &Journal{Remote: f.vm, App: f.l.app, Release: f.release, Now: f.d.Now}
	if err := f.journal.Done(ctx, "preflight", f.l.sha); err != nil {
		return err
	}
	return f.journal.Done(ctx, "build", imageDetail(f.images))
}

// resume restores the state of the journaled release and returns the index of its next step.
// It returns -1 when the release is complete.
func (f *flow) resume(ctx context.Context) (int, error) {
	entries, err := ReadJournal(ctx, f.vm, f.l.app)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, &ui.Error{What: "Nothing to resume", Why: "the deploy journal is empty", Try: "run spinup deploy --env " + f.l.env.Name}
	}
	f.release = entries[len(entries)-1].Release
	next, ok := NextStep(entries, Steps, f.release)
	if !ok {
		fmt.Fprintf(f.out, "Release %s is already deployed. Nothing to resume.\n", f.release)
		return -1, nil
	}
	if !sameSHA(f.release, f.l.sha) {
		return 0, &ui.Error{What: "Cannot resume release " + f.release, Why: "HEAD is " + f.l.sha[:12] + ", but the release was built from another commit", Try: "check out the commit of the release, or run spinup deploy without --resume"}
	}
	f.journal = &Journal{Remote: f.vm, App: f.l.app, Release: f.release, Now: f.d.Now}
	done := func(step string) (string, bool) {
		detail, found := "", false
		for _, e := range entries {
			if e.Release == f.release && e.Step == step {
				detail, found = e.Detail, e.Status == "done"
			}
		}
		return detail, found
	}
	for _, step := range []string{"build", "transport"} {
		if detail, ok := done(step); ok && detail != "" {
			var imgs []Image
			if err := json.Unmarshal([]byte(detail), &imgs); err != nil {
				return 0, &ui.Error{What: "Cannot resume release " + f.release, Why: "the journal of step " + step + " is not valid", Try: "run spinup deploy without --resume"}
			}
			f.images = imgs
		}
	}
	if _, ok := done("upload"); ok {
		rel, err := readRelease(ctx, f.vm, f.l.app, f.release)
		if err != nil {
			return 0, err
		}
		if rel == nil {
			return 0, &ui.Error{What: "Cannot resume release " + f.release, Why: "release.json of the release is missing on the VM", Try: "run spinup deploy without --resume"}
		}
		f.color, f.images = rel.Color, rel.Images
	}
	if len(f.images) == 0 {
		return 0, &ui.Error{What: "Cannot resume release " + f.release, Why: "the journal has no images of the release", Try: "run spinup deploy without --resume"}
	}
	fmt.Fprintf(f.out, "Resuming release %s at step %s\n", f.release, next)
	return slices.Index(Steps, next), nil
}

// hooks runs the pre-deploy hooks (step 4).
func (f *flow) hooks(ctx context.Context) (string, error) {
	cur, err := readRelease(ctx, f.vm, f.l.app, "")
	if err != nil {
		return "", err
	}
	deployed := ""
	if cur != nil {
		deployed = cur.MigrationVersion
	}
	hooks := f.d.PreDeploy()
	if len(hooks) == 0 {
		return "no hooks", nil
	}
	hctx := WithHookOptions(ctx, HookOptions{SkipBackup: f.o.SkipBackup, Root: f.l.root, PendingMigrations: pendingMigrations(f.l.migrations, deployed)})
	if err := RunHooks(hctx, hooks, &HookContext{Env: f.l.env, App: f.l.app, Release: f.release, Remote: f.vm, Out: f.out}); err != nil {
		return "", err
	}
	names := make([]string, len(hooks))
	for i, h := range hooks {
		names[i] = h.Name
	}
	slices.Sort(names)
	return strings.Join(names, ","), nil
}

// transport moves the images to the VM (step 5).
func (f *flow) transport(ctx context.Context) (string, error) {
	t := transporter{exec: f.d.Exec, vm: f.vm, redact: f.redact, out: f.out}
	images, err := t.transport(ctx, f.l.env.Transport, f.images)
	if err != nil {
		return "", err
	}
	f.images = images
	return imageDetail(images), nil
}

// releaseSnapshot returns release.json for the release.
func (f *flow) releaseSnapshot() Release {
	transport := f.l.env.Transport
	if transport == "" {
		transport = TransportSSH
	}
	return Release{
		Release: f.release, App: f.l.app, Env: f.l.env.Name, GitSHA: f.l.sha, Color: f.color, Transport: transport,
		Images: f.images, MigrationVersion: migrationVersion(f.l.migrations), User: f.d.User(),
		CreatedAt: releaseTime(f.release), Secrets: SecretHashes(f.l.secrets),
	}
}

// releaseFiles returns the files of the release directory, without release.json.
func (f *flow) releaseFiles() ([]releaseFile, error) {
	env, err := dotEnv(f.l.envPairs(f.color, f.images, f.uid, f.gid))
	if err != nil {
		return nil, err
	}
	files := append(slices.Clone(f.l.files), releaseFile{Path: ".env", Data: env, Mode: 0644})
	slices.SortFunc(files, func(a, b releaseFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// upload writes the release directory and pushes the secrets (step 6).
func (f *flow) upload(ctx context.Context) (string, error) {
	live, err := ReadUpstream(ctx, f.vm, f.l.app)
	if err != nil {
		return "", err
	}
	f.color = OtherColor(LiveColor(live))
	files, err := f.releaseFiles()
	if err != nil {
		return "", err
	}
	dir := f.releaseDir()
	if _, err := f.cmd(ctx, false, append([]string{"mkdir", "-p", "--"}, releaseDirs(dir, files)...)...); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	fmt.Fprintf(f.out, "Uploading release %s (%s color)\n", f.release, f.color)
	for _, file := range files {
		if err := f.vm.Upload(ctx, file.Data, dir+"/"+file.Path, file.Mode); err != nil {
			return "", fmt.Errorf("upload %s: %w", file.Path, err)
		}
	}
	fmt.Fprintf(f.out, "Pushing %d secrets\n", len(f.l.secrets))
	if err := secrets.Push(ctx, uploader{vm: f.vm, redact: f.redact}, f.l.app, f.l.secrets); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f.releaseSnapshot(), "", "  ")
	if err != nil {
		return "", err
	}
	// release.json is written last: its presence marks a complete release directory.
	if err := f.vm.Upload(ctx, append(data, '\n'), dir+"/release.json", 0644); err != nil {
		return "", fmt.Errorf("upload release.json: %w", err)
	}
	return f.color, nil
}

// uploader adapts remote.Runner to secrets.Uploader.
type uploader struct {
	vm     remote.Runner
	redact []string
}

func (u uploader) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return u.vm.Upload(ctx, data, path, mode)
}

func (u uploader) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	return u.vm.Run(ctx, remote.Cmd{Argv: append([]string{c.Name}, c.Args...), Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Timeout: c.Timeout, Redact: append(slices.Clone(u.redact), c.Redact...)})
}

// edgeSyncScript copies the Caddy and Postgres configuration of the release into the stable
// directories. Caddyfile.next waits for validation. Directories are replaced as a whole, so a
// file removed from the release is removed on the VM too.
const edgeSyncScript = `set -eu
root=$1
rel=$2
mkdir -p -- "$root/caddy" "$root/postgres" "$root/data/caddy/data" "$root/data/caddy/config"
cp -- "$rel/Caddyfile" "$root/caddy/Caddyfile.next"
sync_dir() {
	rm -rf -- "$2.next"
	if [ -d "$1" ]; then cp -R -- "$1" "$2.next"; else mkdir -- "$2.next"; fi
	rm -rf -- "$2"
	mv -- "$2.next" "$2"
}
sync_dir "$rel/Caddyfile.d" "$root/caddy/Caddyfile.d"
sync_dir "$rel/postgres/conf.d" "$root/postgres/conf.d"
if [ -f "$rel/postgres/postgresql.conf" ]; then
	cp -- "$rel/postgres/postgresql.conf" "$root/postgres/postgresql.conf.next"
	mv -f -- "$root/postgres/postgresql.conf.next" "$root/postgres/postgresql.conf"
fi`

// edge syncs the Caddy and Postgres configuration and starts the base project (Caddy and Postgres).
// A color switch never restarts it.
func (f *flow) edge(ctx context.Context) (string, error) {
	root, dir := f.root(), f.releaseDir()
	if _, err := f.cmd(ctx, false, "sh", "-c", edgeSyncScript, "spinup-edge", root, dir); err != nil {
		return "", fmt.Errorf("copy the Caddy and Postgres configuration: %w", err)
	}
	live, err := ReadUpstream(ctx, f.vm, f.l.app)
	if err != nil {
		return "", err
	}
	if live == nil {
		// Caddy imports upstream.caddy. Before the first switch, no color is live.
		if err := f.vm.Upload(ctx, PlaceholderUpstream, root+"/caddy/upstream.caddy", 0644); err != nil {
			return "", fmt.Errorf("write the first upstream.caddy: %w", err)
		}
	}
	caddy, err := listContainers(ctx, f.vm, f.redact, f.l.app, "caddy")
	if err != nil {
		return "", err
	}
	if len(caddy) > 0 {
		if _, err := f.cmd(ctx, true, "docker", "exec", caddy[0], "caddy", "validate", "--config", "/etc/caddy/Caddyfile.next", "--adapter", "caddyfile"); err != nil {
			return "", fmt.Errorf("the new Caddyfile is not valid, and Caddy keeps the old one: %w\nTry: correct deploy/Caddyfile or deploy/Caddyfile.d, commit, and deploy again", brief(err))
		}
	}
	if _, err := f.cmd(ctx, false, "mv", "-f", "--", root+"/caddy/Caddyfile.next", root+"/caddy/Caddyfile"); err != nil {
		return "", fmt.Errorf("install the Caddyfile: %w", err)
	}
	files := make([]string, 0, 2)
	for _, name := range edgeComposeFiles(f.l.files) {
		files = append(files, dir+"/"+name)
	}
	fmt.Fprintln(f.out, "Starting Caddy and Postgres")
	up := ComposeArgv(f.l.app, dir, files, "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(seconds(f.o.HealthTimeout)))
	if _, err := f.cmd(ctx, true, up...); err != nil {
		return "", fmt.Errorf("start Caddy and Postgres: %w", brief(err))
	}
	return "", nil
}

// migrate runs the migrations of the new release as a one-shot (step 7). The kit holds the advisory lock.
func (f *flow) migrate(ctx context.Context) (string, error) {
	dir := f.releaseDir()
	fmt.Fprintln(f.out, "Running the migrations")
	argv := ComposeArgv(f.l.app+"-migrate", dir, []string{dir + "/compose.yml"}, "--profile", "migrate", "run", "--rm", "-T", "migrate")
	if _, err := f.cmd(ctx, true, argv...); err != nil {
		return "", fmt.Errorf("the migrations failed, and the old release is still live: %w", brief(err))
	}
	return migrationVersion(f.l.migrations), nil
}

// doSwitch makes the new color live (step 8, ADR 0003).
func (f *flow) doSwitch(ctx context.Context) (string, error) {
	err := Switch(ctx, f.vm, SwitchOptions{
		App: f.l.app, ReleaseDir: f.releaseDir(), Color: f.color, Services: DefaultServices(f.l.web),
		HealthTimeout: f.o.HealthTimeout, NoAutoRollback: f.o.NoAutoRollback, Redact: f.redact,
		Out: f.out, Now: f.d.Now, Sleep: f.d.Sleep,
	})
	if err != nil {
		return "", err
	}
	return f.color + " live", nil
}

// smokeAttempts and smokeInterval give a new certificate time to arrive.
const (
	smokeAttempts = 15
	smokeInterval = 2 * time.Second
)

// smoke requests /healthz, /version, and the smoke URLs through the public URL (step 9).
func (f *flow) smoke(ctx context.Context) (string, error) {
	paths := append([]string{"/healthz"}, f.l.envFile.SmokeURLs...)
	for _, p := range paths {
		url := f.l.origin + p
		if err := f.retry(ctx, func() error {
			status, _, err := f.d.HTTPGet(ctx, url)
			if err != nil {
				return fmt.Errorf("GET %s: %w", url, err)
			}
			if status < 200 || status > 299 {
				return fmt.Errorf("GET %s answered %d", url, status)
			}
			return nil
		}); err != nil {
			return "", smokeError(err)
		}
		fmt.Fprintf(f.out, "Smoke test passed: %s\n", p)
	}
	want := f.l.sha[:12]
	if err := f.retry(ctx, func() error {
		body, where, err := f.version(ctx)
		if err != nil {
			return err
		}
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &v) != nil || !strings.Contains(v.Version, want) {
			return fmt.Errorf("%s reports version %q, not the new commit %s", where, v.Version, want)
		}
		return nil
	}); err != nil {
		return "", smokeError(err)
	}
	fmt.Fprintf(f.out, "Smoke test passed: /version reports %s\n", want)
	return strconv.Itoa(len(paths)+1) + " checks", nil
}

func smokeError(err error) error {
	return fmt.Errorf("the smoke test failed after the switch, and the new release is live: %w\nTry: run spinup rollback, or correct the problem and run spinup deploy --resume", err)
}

// version reads /version of the new release. An API-only app serves it on the public URL.
// A full app routes / to web, so the check asks the API of the new color through the Caddy container.
func (f *flow) version(ctx context.Context) ([]byte, string, error) {
	if !f.l.web {
		url := f.l.origin + "/version"
		status, body, err := f.d.HTTPGet(ctx, url)
		if err != nil {
			return nil, url, fmt.Errorf("GET %s: %w", url, err)
		}
		if status != 200 {
			return nil, url, fmt.Errorf("GET %s answered %d", url, status)
		}
		return body, url, nil
	}
	caddy, err := listContainers(ctx, f.vm, f.redact, f.l.app, "caddy")
	if err != nil {
		return nil, "", err
	}
	if len(caddy) == 0 {
		return nil, "", errors.New("the Caddy container is not running")
	}
	url := "http://api-" + f.color + ":8080/version"
	result, err := f.cmd(ctx, false, "docker", "exec", caddy[0], "wget", "-q", "-T", "5", "-O", "-", url)
	if err != nil {
		return nil, url, fmt.Errorf("GET %s from Caddy: %w", url, err)
	}
	return result.Stdout, url, nil
}

func (f *flow) retry(ctx context.Context, check func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = check(); err == nil || attempt >= smokeAttempts {
			return err
		}
		if serr := f.d.Sleep(ctx, smokeInterval); serr != nil {
			return errors.Join(err, serr)
		}
	}
}

// listReleasesScript prints the names in the releases directory.
const listReleasesScript = `cd -- "$1" && for d in *; do if [ -d "$d" ]; then printf '%s\n' "$d"; fi; done`

// record moves current, prepends the release to releases.json, and keeps KeepReleases releases (step 10).
func (f *flow) record(ctx context.Context) (string, error) {
	root := f.root()
	if _, err := f.cmd(ctx, false, "ln", "-sfn", "releases/"+f.release, root+"/current"); err != nil {
		return "", fmt.Errorf("move current to %s: %w", f.release, err)
	}
	entries, err := ReadReleases(ctx, f.vm, f.l.app)
	if err != nil {
		return "", err
	}
	snap := f.releaseSnapshot()
	kept, pruned := prependRelease(entries, ReleaseEntry{Release: snap.Release, GitSHA: snap.GitSHA, Color: snap.Color, Images: snap.Images, MigrationVersion: snap.MigrationVersion, User: snap.User, CreatedAt: snap.CreatedAt})
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return "", err
	}
	if err := f.vm.Upload(ctx, append(data, '\n'), root+"/releases.json", 0644); err != nil {
		return "", fmt.Errorf("write releases.json: %w", err)
	}
	keep := map[string]bool{}
	for _, e := range kept {
		keep[e.Release] = true
	}
	result, err := f.cmd(ctx, false, "sh", "-c", listReleasesScript, "spinup-releases", root+"/releases")
	if err != nil {
		return "", fmt.Errorf("list the release directories: %w", err)
	}
	var remove []string
	for _, name := range strings.Fields(string(result.Stdout)) {
		// Only remove names that are release IDs. Never remove a release that releases.json keeps.
		if ValidReleaseID(name) && !keep[name] {
			remove = append(remove, root+"/releases/"+name)
		}
	}
	if len(remove) > 0 {
		if _, err := f.cmd(ctx, false, append([]string{"rm", "-rf", "--"}, remove...)...); err != nil {
			return "", fmt.Errorf("remove old release directories: %w", err)
		}
	}
	// Remove the images of pruned releases. An image that a container still uses stays.
	var images []string
	for _, e := range pruned {
		for _, img := range e.Images {
			if img.Ref != "" && !slices.ContainsFunc(snap.Images, func(i Image) bool { return i.Ref == img.Ref }) {
				images = append(images, img.Ref)
			}
		}
	}
	if len(images) > 0 {
		if _, err := f.cmd(ctx, false, append([]string{"docker", "image", "rm"}, images...)...); err != nil {
			fmt.Fprintf(f.errOut, "Warning: some old images were not removed: %s\n", stripTry(err.Error()))
		}
	}
	return fmt.Sprintf("kept %d releases, removed %d", len(kept), len(remove)), nil
}

// post runs the post-release hooks (step 11). The release is live, so a hook failure is a warning.
func (f *flow) post(ctx context.Context) (string, error) {
	hooks := f.d.PostRelease()
	if len(hooks) == 0 {
		return "no hooks", nil
	}
	if err := RunHooks(ctx, hooks, &HookContext{Env: f.l.env, App: f.l.app, Release: f.release, Remote: f.vm, Out: f.out}); err != nil {
		fmt.Fprintf(f.errOut, "Warning: a post-release hook failed: %s\n", stripTry(err.Error()))
		return "warning: " + stripTry(err.Error()), nil
	}
	return "ok", nil
}
