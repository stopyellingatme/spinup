package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// commitFiles commits spinup.toml, .sops.yaml, the secrets file, and a scaffolded environment
// settings file (ADR 0017 §7 step 13).
// It returns true when the run may go on to the deploy.
func (r *run) commitFiles(ctx context.Context) (bool, error) {
	if r.o.NoCommit {
		if r.o.Deploy.AllowDirty {
			return true, nil
		}
		r.say("Stopped before the deploy, because of --no-commit.")
		if r.scaffoldedEnv {
			r.say("Commit spinup.toml, .sops.yaml, secrets/%s.enc.yaml, and deploy/environments/%s.toml, then run: spinup deploy --env %s", r.env, r.env, r.env)
		} else {
			r.say("Commit spinup.toml, .sops.yaml, and secrets/%s.enc.yaml, then run: spinup deploy --env %s", r.env, r.env)
		}
		return false, nil
	}
	candidates := []string{"spinup.toml", ".sops.yaml", "secrets/" + r.env + ".enc.yaml"}
	if r.scaffoldedEnv {
		// A new environment got its settings file from staging.toml in the preflight.
		candidates = append(candidates, "deploy/environments/"+r.env+".toml")
	}
	var paths []string
	for _, p := range candidates {
		if _, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(p))); err == nil {
			paths = append(paths, p)
		}
	}
	git := func(args ...string) (runexec.Result, error) {
		return r.d.Exec.Run(ctx, runexec.Cmd{Name: "git", Args: args, Dir: r.root})
	}
	if _, err := git(append([]string{"add", "--"}, paths...)...); err != nil {
		return false, failWrap("stage the provisioning files", err, "commit the files by hand, then run spinup deploy --env "+r.env)
	}
	res, err := git(append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return false, failWrap("check the staged files", err, "commit the files by hand, then run spinup deploy --env "+r.env)
	}
	if strings.TrimSpace(string(res.Stdout)) == "" {
		r.say("The provisioning files are committed already.")
		return true, nil
	}
	msg := "spinup: provision " + r.env + " on DigitalOcean"
	if _, err := git(append([]string{"commit", "-m", msg, "--"}, paths...)...); err != nil {
		return false, failWrap("commit the provisioning files", err, "commit the files by hand, then run spinup deploy --env "+r.env)
	}
	r.say("Committed: %s", msg)
	// A release ID of --tag ends with the SHA of the commit that built its images. The new commit
	// moved HEAD, so the deploy would refuse that tag.
	if r.o.Deploy.Tag != "" && !r.o.SkipDeploy {
		return false, fail("deploy the images of --tag "+r.o.Deploy.Tag, "the provisioning commit moved HEAD, and the release ID must end with the HEAD SHA",
			"build the images of the new commit, then run spinup deploy --env "+r.env+"; or provision first with --skip-deploy")
	}
	return true, nil
}
