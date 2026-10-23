package provision

import (
	"context"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
)

// plan prints what the run would create or change. It writes nothing (ADR 0017 §7).
func (r *run) plan(ctx context.Context) error {
	r.say("Plan for the environment %s of %s:", r.env, r.app)
	if err := r.planKey(ctx); err != nil {
		return err
	}
	if err := r.planFirewall(ctx); err != nil {
		return err
	}
	r.planDroplet()
	if err := r.planDNS(ctx); err != nil {
		return err
	}
	r.say("spinup.toml: set host, user, public_url, host_keys, and the [env.%s.digitalocean] table.", r.env)
	if err := r.planSecrets(ctx); err != nil {
		return err
	}
	r.say("No changes made (--plan).")
	return nil
}

func (r *run) planKey(ctx context.Context) error {
	fp, err := digitalocean.Fingerprint(r.pub)
	if err != nil {
		return failWrap("read the operator SSH key", err, "pass --ssh-key <public-key-file>")
	}
	k, err := r.d.Cloud.SSHKeyByFingerprint(ctx, fp)
	if err != nil {
		return failWrap("look up the operator SSH key", err, "run the command again")
	}
	if k != nil {
		r.say("SSH key: exists (%s).", k.Name)
	} else {
		r.say("SSH key: create spinup-%s.", operatorName())
	}
	return nil
}

func (r *run) planFirewall(ctx context.Context) error {
	all, err := r.d.Cloud.Firewalls(ctx)
	if err != nil {
		return failWrap("list the cloud firewalls", err, "run the command again")
	}
	if slices.ContainsFunc(all, func(f digitalocean.Firewall) bool { return f.Name == r.firewallName() }) {
		r.say("Firewall: %s exists. spinup updates it when the rules differ.", r.firewallName())
	} else {
		r.say("Firewall: create %s (22/tcp from %s, 80/tcp, 443/tcp, 443/udp).", r.firewallName(), first(r.o.SSHFrom, "0.0.0.0/0,::/0"))
	}
	return nil
}

func (r *run) planDroplet() {
	price := money(r.sizeInfo.PriceMonthly, r.sizeInfo.PriceHourly)
	switch {
	case len(r.droplets) == 0:
		r.say("Droplet: create %s-%s, size %s, region %s, image %s, %s.", r.app, r.env, r.size, r.region, r.image, price)
	case r.o.Recreate:
		r.say("Droplet: delete %s (ID %d), then create a new one: size %s, region %s, image %s, %s.", r.droplets[0].Name, r.droplets[0].ID, r.size, r.region, r.image, price)
	default:
		r.say("Droplet: %s (ID %d) exists. spinup keeps it.", r.droplets[0].Name, r.droplets[0].ID)
	}
	if len(r.droplets) == 0 || r.o.Recreate {
		r.say("Host key: generate an ed25519 key and pin it in host_keys.")
	}
}

func (r *run) planDNS(ctx context.Context) error {
	if r.domain == "" {
		r.say("DNS: none. The public URL uses %s.", defaults().IPDNS)
		return nil
	}
	zone, name, found, err := r.d.Cloud.FindZone(ctx, r.domain)
	if err != nil {
		return failWrap("look up the DNS zone", err, "run the command again")
	}
	if !found {
		r.say("DNS: DigitalOcean does not hold a zone for %s. You add the A record yourself, and spinup waits for it.", r.domain)
		return nil
	}
	recs, err := r.d.Cloud.Records(ctx, zone, "A", name)
	if err != nil {
		return failWrap("look up the DNS record", err, "run the command again")
	}
	if len(recs) == 0 {
		r.say("DNS: create the A record %s in the zone %s.", r.domain, zone)
	} else {
		r.say("DNS: the A record %s exists (%s). spinup updates it when it differs.", r.domain, recs[0].Data)
	}
	return nil
}

func (r *run) planSecrets(ctx context.Context) error {
	need, err := deploy.RequiredSecrets(r.root, r.cfg)
	if err != nil {
		return err
	}
	have := map[string]string{}
	if r.d.Secrets.Exists(r.root, r.env) {
		if have, err = r.d.Secrets.Decrypt(ctx, r.root, r.env); err != nil {
			return failWrap("read the secrets", err, "check the environment key")
		}
	} else {
		r.say("Secrets: create secrets/%s.enc.yaml.", r.env)
	}
	if m := missingSecrets(need, have); len(m) > 0 {
		r.say("Secrets: make %s.", strings.Join(m, ", "))
	} else {
		r.say("Secrets: complete.")
	}
	return nil
}
