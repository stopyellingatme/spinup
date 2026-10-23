package provision

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
)

const (
	activeTimeout = 5 * time.Minute
	goneTimeout   = 2 * time.Minute
)

// loadDroplets finds the droplets with the environment tag. More than one is an error.
func (r *run) loadDroplets(ctx context.Context) error {
	list, err := r.d.Cloud.DropletsByTag(ctx, r.tag())
	if err != nil {
		return failWrap("list the droplets of the environment", err, "run the command again")
	}
	if len(list) > 1 {
		ids := make([]string, len(list))
		for i, d := range list {
			ids[i] = strconv.Itoa(d.ID)
		}
		return fail("find the droplet of the environment", "the tag "+r.tag()+" is on several droplets: "+strings.Join(ids, ", "), "delete the extra droplets in the DigitalOcean control panel, then run the command again")
	}
	r.droplets = list
	return nil
}

// mustCreate reports whether the run creates a droplet.
func (r *run) mustCreate() bool { return len(r.droplets) == 0 || r.o.Recreate }

// confirmCreate asks for the price once, before the first cost (ADR 0017 §1).
func (r *run) confirmCreate() error {
	if !r.mustCreate() {
		return nil
	}
	question := fmt.Sprintf("Create droplet %s in %s for $%.2f/month ($%.5f/hour)?", r.size, r.region, r.sizeInfo.PriceMonthly, r.sizeInfo.PriceHourly)
	c := r.o.Confirm
	switch {
	case c.Yes:
		r.say("%s yes (--yes)", question)
		return nil
	case !c.Interactive:
		return fail("create the droplet", "the price needs an answer, and there is no terminal", "pass --yes to accept the price")
	case c.In == nil:
		return fail("create the droplet", "the price needs an answer, and the input is not available", "pass --yes to accept the price")
	}
	if c.Out != nil {
		_, _ = fmt.Fprintf(c.Out, "%s [y/N] ", question)
	}
	line, _ := bufio.NewReader(c.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return fail("create the droplet", "you did not accept the price", "run the command again and answer y, or pass --yes")
}

// checkRecreate refuses --recreate when a release may exist, unless --i-know is set.
func (r *run) checkRecreate(ctx context.Context) error {
	if !r.o.Recreate || len(r.droplets) == 0 || r.o.IKnow {
		return nil
	}
	exists, known := r.releaseExists(ctx)
	switch {
	case exists:
		return fail("recreate the droplet", "a release runs on it, and the database is lost with the droplet", "run spinup backup first, then pass --i-know")
	case !known:
		return fail("recreate the droplet", "spinup cannot check whether a release runs on it, and a release may exist", "run spinup backup first, then pass --i-know")
	}
	return nil
}

// releaseExists connects to the old droplet and reads releases.json.
// known is false when the connection or the read fails.
func (r *run) releaseExists(ctx context.Context) (exists, known bool) {
	env := r.envCfg
	if env.Host == "" {
		env.Host = r.droplets[0].PublicIPv4()
	}
	if env.User == "" {
		env.User = deployUser
	}
	if env.Host == "" || len(env.HostKeys) == 0 {
		return false, false
	}
	conn, err := r.d.Dial(ctx, env)
	if err != nil {
		return false, false
	}
	defer func() { _ = conn.Close(ctx) }()
	entries, err := deploy.ReadReleases(ctx, conn, r.app)
	if err != nil {
		return false, false
	}
	return len(entries) > 0, true
}

// operatorName names the SSH key in the account.
func operatorName() string {
	user := first(os.Getenv("USER"), os.Getenv("USERNAME"))
	host, _ := os.Hostname()
	if user == "" || host == "" {
		return "operator"
	}
	return user + "@" + host
}

func (r *run) ensureSSHKey(ctx context.Context) error {
	key, created, err := r.d.Cloud.EnsureSSHKey(ctx, "spinup-"+operatorName(), r.pub)
	if err != nil {
		return failWrap("register the operator SSH key", err, "run the command again")
	}
	r.sshKey = key
	if created {
		r.say("Added the SSH key %s to the account.", key.Name)
	} else {
		r.say("Using the SSH key %s.", key.Name)
	}
	return nil
}

func (r *run) wantFirewall() digitalocean.Firewall {
	from := []string{"0.0.0.0/0", "::/0"}
	if s := strings.TrimSpace(r.o.SSHFrom); s != "" {
		from = nil
		for _, a := range strings.Split(s, ",") {
			if a = strings.TrimSpace(a); a != "" {
				from = append(from, a)
			}
		}
	}
	all := []string{"0.0.0.0/0", "::/0"}
	in := func(proto, ports string, src []string) digitalocean.InboundRule {
		return digitalocean.InboundRule{Protocol: proto, Ports: ports, Sources: digitalocean.Sources{Addresses: src}}
	}
	out := func(proto, ports string) digitalocean.OutboundRule {
		return digitalocean.OutboundRule{Protocol: proto, Ports: ports, Destinations: digitalocean.Destinations{Addresses: all}}
	}
	return digitalocean.Firewall{
		Name: r.firewallName(),
		Tags: []string{r.tag()},
		InboundRules: []digitalocean.InboundRule{
			in("tcp", "22", from), in("tcp", "80", all), in("tcp", "443", all), in("udp", "443", all),
		},
		OutboundRules: []digitalocean.OutboundRule{out("tcp", "all"), out("udp", "all"), {Protocol: "icmp", Destinations: digitalocean.Destinations{Addresses: all}}},
	}
}

func (r *run) ensureFirewall(ctx context.Context) error {
	fw, created, err := r.d.Cloud.EnsureFirewall(ctx, r.wantFirewall())
	if err != nil {
		return failWrap("make the cloud firewall", err, "run the command again")
	}
	r.fw = fw
	if created {
		r.say("Created the firewall %s.", fw.Name)
	} else {
		r.say("Using the firewall %s.", fw.Name)
	}
	return nil
}

// ensureDroplet adopts the droplet with the tag, or makes a new one with a pinned host key.
func (r *run) ensureDroplet(ctx context.Context) error {
	if len(r.droplets) == 1 && !r.o.Recreate {
		dr := r.droplets[0]
		if r.st.DropletID != 0 && r.st.DropletID != int64(dr.ID) {
			return fail("use the droplet "+strconv.Itoa(dr.ID), fmt.Sprintf("spinup.toml names the droplet %d, and the tag %s is on another droplet", r.st.DropletID, r.tag()), "check droplet_id in spinup.toml, or pass --recreate")
		}
		if len(r.envCfg.HostKeys) == 0 {
			return fail("use the droplet "+strconv.Itoa(dr.ID), "spinup.toml has no host_keys pin for it", "pass --recreate to make a new droplet with a pinned host key")
		}
		r.droplet = &dr
		r.say("Using the droplet %s (ID %d).", dr.Name, dr.ID)
		return nil
	}
	if len(r.droplets) == 1 {
		if err := r.deleteOld(ctx, r.droplets[0]); err != nil {
			return err
		}
	}
	if err := r.newHostKey(ctx); err != nil {
		return err
	}
	data, err := buildUserData(r.root, r.app, r.pub, r.hostPriv, r.hostPub)
	if err != nil {
		return err
	}
	created, err := r.d.Cloud.CreateDroplet(ctx, digitalocean.DropletRequest{
		Name: r.app + "-" + r.env, Region: r.region, Size: r.size, Image: r.image,
		SSHKeys: []string{r.sshKey.Fingerprint}, Tags: []string{"spinup", r.tag()}, UserData: data,
		IPv6: true,
	})
	r.hostPriv = ""
	if err != nil {
		return failWrap("create the droplet", err, "run the command again, because the next run finds the droplet by its tag")
	}
	r.droplet = &created
	r.say("Created the droplet %s (ID %d).", created.Name, created.ID)
	return nil
}

// deleteOld deletes the droplet for --recreate and waits until it is gone.
func (r *run) deleteOld(ctx context.Context, dr digitalocean.Droplet) error {
	r.say("Deleting the droplet %s (ID %d).", dr.Name, dr.ID)
	if err := r.d.Cloud.DeleteDroplet(ctx, dr.ID); err != nil {
		return failWrap("delete the old droplet", err, "run the command again")
	}
	var waited time.Duration
	for {
		_, err := r.d.Cloud.Droplet(ctx, dr.ID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return failWrap("check the old droplet", err, "run the command again")
		}
		if waited >= goneTimeout {
			return fail("delete the old droplet", "it still exists after "+goneTimeout.String(), "wait, then run the command again")
		}
		if err := r.d.Sleep(ctx, 5*time.Second); err != nil {
			return err
		}
		waited += 5 * time.Second
	}
}

// waitActive waits until the droplet is active and has an IPv4 address.
func (r *run) waitActive(ctx context.Context) error {
	dr, err := r.d.Cloud.WaitActive(ctx, r.droplet.ID, activeTimeout)
	if err != nil {
		return failWrap("wait for the droplet", err, "run the command again")
	}
	r.droplet = &dr
	r.ip = dr.PublicIPv4()
	r.say("The droplet is active at %s.", r.ip)
	return nil
}

// billingNote tells the user how to stop the cost after a failure (ADR 0017 §7).
func (r *run) billingNote() {
	if r.droplet == nil {
		return
	}
	hourly := r.sizeInfo.PriceHourly
	r.warn("The droplet %s (ID %d) still runs and costs $%.5f/hour.", r.droplet.Name, r.droplet.ID, hourly)
	r.warn("Try: spinup deploy digital-ocean destroy --env %s --i-know", r.env)
}
