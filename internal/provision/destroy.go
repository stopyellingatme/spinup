package provision

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/project"
)

// Destroy deletes the droplet, the firewall, and the A record of the environment (ADR 0017 §1).
// It deletes only resources that match the IDs in spinup.toml. It keeps the account SSH key.
func Destroy(ctx context.Context, o Options, d Deps) error {
	d.fill()
	if !o.IKnow {
		return fail("destroy the environment", "the command deletes the droplet and its database", "pass --i-know")
	}
	if err := needCloud(d); err != nil {
		return err
	}
	b := &base{o: o, d: d}
	if err := b.load(); err != nil {
		return err
	}
	env := b.envCfg
	env.Name = b.env
	env.Protected = true
	if err := project.ConfirmProtected(env, "destroy the DigitalOcean resources", o.Confirm); err != nil {
		return failWrap("confirm the destroy", err, "type the environment name in a terminal, or pass --yes in CI")
	}
	acct, err := d.Cloud.Account(ctx)
	if err != nil {
		return failWrap("read the DigitalOcean account", err, "run spinup deploy digital-ocean login")
	}
	if team := accountTeam(acct); b.st.Team != "" && b.st.Team != team {
		return fail("use this DigitalOcean team", "the environment belongs to team "+b.st.Team+", and the token belongs to team "+team, "sign in to the team that owns the environment: spinup deploy digital-ocean login")
	}
	b.warn("Warning: the database on the droplet is lost. spinup backup keeps a copy.")
	list, err := d.Cloud.DropletsByTag(ctx, b.tag())
	if err != nil {
		return failWrap("list the droplets of the environment", err, "run the command again")
	}
	ip := b.envCfg.Host
	var mine, others []string
	var mineID int
	// A run that stopped between the create call and the write of droplet_id leaves one tagged
	// droplet and no ID. That droplet belongs to this environment, so destroy deletes it too.
	adopt := b.st.DropletID == 0 && len(list) == 1
	for _, x := range list {
		if adopt || b.st.DropletID != 0 && int64(x.ID) == b.st.DropletID {
			mine = append(mine, strconv.Itoa(x.ID))
			mineID = x.ID
			if v := x.PublicIPv4(); v != "" {
				ip = v
			}
			continue
		}
		others = append(others, strconv.Itoa(x.ID))
	}
	if len(others) > 0 {
		b.warn("Not deleted: the droplets %s have the tag %s, and their IDs differ from droplet_id in spinup.toml.", strings.Join(others, ", "), b.tag())
	}
	if err := b.destroyRecord(ctx, ip); err != nil {
		return err
	}
	if len(mine) == 1 {
		if err := d.Cloud.DeleteDroplet(ctx, mineID); err != nil {
			return failWrap("delete the droplet", err, "run the command again")
		}
		b.say("Deleted the droplet %d.", mineID)
	} else {
		b.say("No droplet matches droplet_id in spinup.toml.")
	}
	if err := b.destroyFirewall(ctx); err != nil {
		return err
	}
	if err := b.destroyTags(ctx); err != nil {
		return err
	}
	b.say("Kept the account SSH key.")
	if err := b.cleanConfig(); err != nil {
		return err
	}
	b.say("Commit the change of spinup.toml.")
	return nil
}

// destroyRecord deletes the A record of the domain when it points at the droplet.
func (b *base) destroyRecord(ctx context.Context, ip string) error {
	if b.st.Domain == "" || ip == "" {
		return nil
	}
	zone, name, found, err := b.d.Cloud.FindZone(ctx, b.st.Domain)
	if err != nil {
		return failWrap("look up the DNS zone", err, "run the command again")
	}
	if !found {
		return nil
	}
	recs, err := b.d.Cloud.Records(ctx, zone, "A", name)
	if err != nil {
		return failWrap("look up the DNS record", err, "run the command again")
	}
	for _, rec := range recs {
		if rec.Data != ip {
			b.warn("Not deleted: the A record of %s points at %s, not at the droplet.", b.st.Domain, rec.Data)
			continue
		}
		if err := b.d.Cloud.DeleteRecord(ctx, zone, rec.ID); err != nil {
			return failWrap("delete the DNS record", err, "run the command again")
		}
		b.say("Deleted the A record of %s.", b.st.Domain)
	}
	return nil
}

// destroyFirewall deletes the firewall by ID after it checks the name. When spinup.toml has no
// firewall_id (a run stopped before it wrote the ID), it deletes the firewall that has the
// environment name and the environment tag.
func (b *base) destroyFirewall(ctx context.Context) error {
	all, err := b.d.Cloud.Firewalls(ctx)
	if err != nil {
		return failWrap("list the cloud firewalls", err, "run the command again")
	}
	for _, f := range all {
		if b.st.FirewallID == "" {
			if f.Name != b.firewallName() || !slices.Contains(f.Tags, b.tag()) {
				continue
			}
		} else if f.ID != b.st.FirewallID {
			continue
		}
		if f.Name != b.firewallName() {
			b.warn("Not deleted: the firewall %s is named %s, not %s.", f.ID, f.Name, b.firewallName())
			return nil
		}
		if err := b.d.Cloud.DeleteFirewall(ctx, f.ID); err != nil {
			return failWrap("delete the firewall", err, "run the command again")
		}
		b.say("Deleted the firewall %s.", f.Name)
	}
	return nil
}

// cleanConfig removes what points at the deleted resources. It keeps the settings for a new run.
func (b *base) cleanConfig() error {
	envT := []string{"env", b.env}
	doT := []string{"env", b.env, "digitalocean"}
	edits := []edit{del(envT, "host"), del(envT, "host_keys")}
	if sslipURL(b.envCfg.PublicURL) {
		edits = append(edits, del(envT, "public_url"))
	}
	edits = append(edits, del(doT, "droplet_id"), del(doT, "firewall_id"))
	return editConfig(b.root, edits)
}

// destroyTags deletes the environment tag and the shared spinup tag when no resource has them.
// A token from before the tag:delete scope cannot delete a tag. Then the tags stay, with a warning.
func (b *base) destroyTags(ctx context.Context) error {
	for _, name := range []string{b.tag(), "spinup"} {
		t, err := b.d.Cloud.TagByName(ctx, name)
		if err != nil {
			return failWrap("look up the tag "+name, err, "run the command again")
		}
		if t == nil {
			continue
		}
		if t.Resources.Count > 0 {
			if name == b.tag() {
				b.warn("Not deleted: %d resources still have the tag %s.", t.Resources.Count, name)
			}
			continue
		}
		err = b.d.Cloud.DeleteTag(ctx, name)
		var ae *digitalocean.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
			b.warn("Not deleted: the tag %s. The token has no tag:delete scope. Run spinup deploy digital-ocean login, then run destroy again.", name)
			continue
		}
		if err != nil {
			return failWrap("delete the tag "+name, err, "run the command again")
		}
		b.say("Deleted the tag %s.", name)
	}
	return nil
}
