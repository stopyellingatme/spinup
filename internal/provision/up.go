package provision

import (
	"context"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/project"
)

// Up provisions the environment on DigitalOcean and runs the deploy (ADR 0017 §7).
// Each step reads the current state first, so a second run continues after a failure.
func Up(ctx context.Context, o Options, d Deps) (err error) {
	r := newRun(o, d)
	defer func() {
		if err != nil {
			r.billingNote()
		}
		if r.runner != nil {
			_ = r.runner.Close(context.WithoutCancel(ctx))
		}
	}()
	if err = r.preflight(ctx); err != nil {
		return err
	}
	if err = r.account(ctx); err != nil {
		return err
	}
	if err = r.checkSize(ctx); err != nil {
		return err
	}
	if err = r.loadDroplets(ctx); err != nil {
		return err
	}
	if r.o.Plan {
		return r.plan(ctx)
	}
	if err = r.checkRecreate(ctx); err != nil {
		return err
	}
	if err = r.confirmCreate(); err != nil {
		return err
	}
	if err = r.ensureSSHKey(ctx); err != nil {
		return err
	}
	if err = r.ensureFirewall(ctx); err != nil {
		return err
	}
	if err = r.ensureDroplet(ctx); err != nil {
		return err
	}
	// Record the IDs at once, so destroy finds the droplet and the firewall also when the run
	// stops before the droplet is active.
	if err = r.recordIDs(); err != nil {
		return err
	}
	if err = r.waitActive(ctx); err != nil {
		return err
	}
	if err = r.writeEnvironment(); err != nil {
		return err
	}
	if err = r.ensureDNS(ctx); err != nil {
		return err
	}
	if err = r.waitSSH(ctx); err != nil {
		return err
	}
	if err = r.waitCloudInit(ctx); err != nil {
		return err
	}
	if err = r.reconnect(ctx); err != nil {
		return err
	}
	if err = r.rotate(ctx); err != nil {
		return err
	}
	if err = r.ensureSecrets(ctx); err != nil {
		return err
	}
	proceed, err := r.commitFiles(ctx)
	if err != nil || !proceed {
		return err
	}
	if r.o.SkipDeploy {
		r.say("Skipped the deploy. Run: spinup deploy --env %s", r.env)
		return nil
	}
	return r.deploy(ctx)
}

// rotate replaces the host key that cloud-init got through the user data (ADR 0017 §6).
func (r *run) rotate(ctx context.Context) error {
	keys, err := rotateHostKey(ctx, rotation{
		Run: func(ctx context.Context, argv ...string) (string, error) {
			return r.run(ctx, argv...)
		},
		Pin:       r.pinHostKeys,
		Reconnect: r.reconnect,
		Keys:      slices.Clone(r.envCfg.HostKeys),
		Out:       r.o.Out,
	})
	if err != nil {
		return failWrap("replace the host key of the droplet", err, "run the command again, because a second run continues safely")
	}
	if !slices.Equal(keys, r.envCfg.HostKeys) {
		return r.pinHostKeys(keys)
	}
	return nil
}

func (r *run) deploy(ctx context.Context) error {
	if r.d.Deploy == nil {
		return fail("run the deploy", "no deploy function is configured", "run spinup deploy --env "+r.env)
	}
	od := r.o.Deploy
	od.Dir, od.Env, od.Out, od.ErrOut = r.root, r.env, r.o.Out, r.o.ErrOut
	if od.Confirm == (project.Confirm{}) {
		od.Confirm = r.o.Confirm
	}
	if err := r.d.Deploy(ctx, od); err != nil {
		return failWrap("deploy the app", err, "spinup deploy --env "+r.env+" --resume")
	}
	return nil
}

// publicURLFor returns the public URL of the run (ADR 0017 §5).
func (r *run) publicURLFor() string {
	switch {
	case r.o.PublicURL != "":
		return r.o.PublicURL
	case r.o.Domain != "":
		return "https://" + r.o.Domain
	case r.st.Domain != "":
		return "https://" + r.st.Domain
	case r.envCfg.PublicURL != "" && !sslipURL(r.envCfg.PublicURL):
		return r.envCfg.PublicURL
	}
	return "https://" + strings.ReplaceAll(r.ip, ".", "-") + "." + defaults().IPDNS
}

// writeEnvironment writes the environment to spinup.toml (ADR 0017 §7 step 6).
// recordIDs writes the droplet ID, the firewall ID, the tag, and the team to spinup.toml.
func (r *run) recordIDs() error {
	doT := []string{"env", r.env, "digitalocean"}
	return editConfig(r.root, []edit{
		set(doT, "droplet_id", int64(r.droplet.ID)),
		set(doT, "firewall_id", r.fw.ID),
		set(doT, "tag", r.tag()),
		set(doT, "team", r.team),
	})
}

func (r *run) writeEnvironment() error {
	r.publicURL = r.publicURLFor()
	envT := []string{"env", r.env}
	doT := []string{"env", r.env, "digitalocean"}
	edits := []edit{
		set(envT, "host", r.ip),
		set(envT, "user", deployUser),
		set(envT, "public_url", r.publicURL),
		set(envT, "host_keys", r.envCfg.HostKeys),
		set(doT, "droplet_id", int64(r.droplet.ID)),
		set(doT, "region", r.region),
		set(doT, "size", r.size),
		set(doT, "image", r.image),
		set(doT, "tag", r.tag()),
		set(doT, "firewall_id", r.fw.ID),
		set(doT, "team", r.team),
	}
	switch {
	case r.domain != "":
		edits = append(edits, set(doT, "domain", r.domain))
	case sslipURL(r.publicURL):
		edits = append(edits, del(doT, "domain"))
	}
	if r.cfg.DefaultEnv == "" {
		edits = append(edits, set(nil, "default_env", r.env))
	}
	if err := editConfig(r.root, edits); err != nil {
		return err
	}
	r.envCfg.Host, r.envCfg.User, r.envCfg.PublicURL = r.ip, deployUser, r.publicURL
	return nil
}
