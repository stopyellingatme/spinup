package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
)

// Report is the JSON output of Status. The field names are stable.
type Report struct {
	Env      string          `json:"env"`
	Account  ReportAccount   `json:"account"`
	Config   ReportConfig    `json:"config"`
	Droplet  *ReportDroplet  `json:"droplet,omitempty"`
	Firewall *ReportFirewall `json:"firewall,omitempty"`
	DNS      *ReportDNS      `json:"dns,omitempty"`
	Drift    []string        `json:"drift"`
}

// ReportAccount identifies the signed-in account.
type ReportAccount struct {
	Email string `json:"email"`
	Team  string `json:"team"`
}

// ReportConfig is the environment in spinup.toml.
type ReportConfig struct {
	Host       string `json:"host,omitempty"`
	User       string `json:"user,omitempty"`
	PublicURL  string `json:"public_url,omitempty"`
	HostKeys   int    `json:"host_keys"`
	DropletID  int64  `json:"droplet_id,omitempty"`
	Region     string `json:"region,omitempty"`
	Size       string `json:"size,omitempty"`
	Image      string `json:"image,omitempty"`
	Tag        string `json:"tag,omitempty"`
	FirewallID string `json:"firewall_id,omitempty"`
	Team       string `json:"team,omitempty"`
	Domain     string `json:"domain,omitempty"`
}

// ReportDroplet is the droplet with the environment tag.
type ReportDroplet struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	IPv4   string `json:"ipv4,omitempty"`
	IPv6   string `json:"ipv6,omitempty"`
	Size   string `json:"size"`
	Region string `json:"region"`
}

// ReportFirewall is the cloud firewall of the environment.
type ReportFirewall struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Found bool   `json:"found"`
}

// ReportDNS is the A record of the domain.
type ReportDNS struct {
	Domain string `json:"domain"`
	Zone   string `json:"zone,omitempty"`
	Data   string `json:"data,omitempty"`
	Found  bool   `json:"found"`
}

// Status reports the account, the stored environment, and the resources. It changes nothing.
func Status(ctx context.Context, o Options, d Deps) error {
	d.fill()
	if err := needCloud(d); err != nil {
		return err
	}
	b := &base{o: o, d: d}
	if err := b.load(); err != nil {
		return err
	}
	acct, err := d.Cloud.Account(ctx)
	if err != nil {
		return failWrap("read the DigitalOcean account", err, "run spinup deploy digital-ocean login")
	}
	rep := Report{Env: b.env, Drift: []string{}}
	rep.Account = ReportAccount{Email: acct.Email, Team: accountTeam(acct)}
	st := b.st
	rep.Config = ReportConfig{
		Host: b.envCfg.Host, User: b.envCfg.User, PublicURL: b.envCfg.PublicURL, HostKeys: len(b.envCfg.HostKeys),
		DropletID: st.DropletID, Region: st.Region, Size: st.Size, Image: st.Image, Tag: st.Tag,
		FirewallID: st.FirewallID, Team: st.Team, Domain: st.Domain,
	}
	drift := func(format string, args ...any) { rep.Drift = append(rep.Drift, fmt.Sprintf(format, args...)) }
	if st.Team != "" && st.Team != rep.Account.Team {
		drift("the token belongs to team %s, and the environment belongs to team %s", rep.Account.Team, st.Team)
	}
	list, err := d.Cloud.DropletsByTag(ctx, b.tag())
	if err != nil {
		return failWrap("list the droplets of the environment", err, "run the command again")
	}
	var ip string
	switch {
	case len(list) > 1:
		ids := make([]string, len(list))
		for i, x := range list {
			ids[i] = strconv.Itoa(x.ID)
		}
		drift("the tag %s is on several droplets: %s", b.tag(), strings.Join(ids, ", "))
	case len(list) == 1:
		x := list[0]
		ip = x.PublicIPv4()
		rep.Droplet = &ReportDroplet{ID: x.ID, Name: x.Name, Status: x.Status, IPv4: ip, IPv6: x.PublicIPv6(), Size: x.SizeSlug, Region: x.Region.Slug}
		if st.DropletID != 0 && st.DropletID != int64(x.ID) {
			drift("droplet_id is %d, and the droplet with the tag is %d", st.DropletID, x.ID)
		}
		if b.envCfg.Host != "" && ip != "" && b.envCfg.Host != ip {
			drift("host is %s, and the droplet address is %s", b.envCfg.Host, ip)
		}
	case st.DropletID != 0:
		drift("droplet_id is %d, and no droplet has the tag %s", st.DropletID, b.tag())
	}
	if err := b.statusFirewall(ctx, &rep); err != nil {
		return err
	}
	if st.Domain != "" {
		if err := b.statusDNS(ctx, &rep, ip, drift); err != nil {
			return err
		}
	}
	if o.JSON {
		enc := json.NewEncoder(o.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(o, rep)
	return nil
}

func (b *base) statusFirewall(ctx context.Context, rep *Report) error {
	all, err := b.d.Cloud.Firewalls(ctx)
	if err != nil {
		return failWrap("list the cloud firewalls", err, "run the command again")
	}
	rep.Firewall = &ReportFirewall{Name: b.firewallName()}
	i := slices.IndexFunc(all, func(f digitalocean.Firewall) bool { return f.Name == b.firewallName() })
	if i < 0 {
		if b.st.DropletID != 0 || b.st.FirewallID != "" {
			rep.Drift = append(rep.Drift, "the firewall "+b.firewallName()+" does not exist")
		}
		return nil
	}
	rep.Firewall.ID, rep.Firewall.Found = all[i].ID, true
	if b.st.FirewallID != "" && b.st.FirewallID != all[i].ID {
		rep.Drift = append(rep.Drift, fmt.Sprintf("firewall_id is %s, and the firewall %s has the ID %s", b.st.FirewallID, b.firewallName(), all[i].ID))
	}
	return nil
}

func (b *base) statusDNS(ctx context.Context, rep *Report, ip string, drift func(string, ...any)) error {
	rep.DNS = &ReportDNS{Domain: b.st.Domain}
	zone, name, found, err := b.d.Cloud.FindZone(ctx, b.st.Domain)
	if err != nil {
		return failWrap("look up the DNS zone", err, "run the command again")
	}
	if !found {
		return nil
	}
	rep.DNS.Zone = zone
	recs, err := b.d.Cloud.Records(ctx, zone, "A", name)
	if err != nil {
		return failWrap("look up the DNS record", err, "run the command again")
	}
	if len(recs) == 0 {
		drift("the zone %s has no A record for %s", zone, b.st.Domain)
		return nil
	}
	rep.DNS.Found, rep.DNS.Data = true, recs[0].Data
	if ip != "" && recs[0].Data != ip {
		drift("the A record of %s points at %s, and the droplet address is %s", b.st.Domain, recs[0].Data, ip)
	}
	return nil
}

func printReport(o Options, rep Report) {
	say := func(format string, args ...any) {
		if o.Out != nil {
			_, _ = fmt.Fprintf(o.Out, format+"\n", args...)
		}
	}
	say("Environment: %s", rep.Env)
	say("Account: %s (team %s)", rep.Account.Email, rep.Account.Team)
	c := rep.Config
	say("Config: host %s, user %s, public URL %s, %d pinned host key(s)", dash(c.Host), dash(c.User), dash(c.PublicURL), c.HostKeys)
	if rep.Droplet != nil {
		x := rep.Droplet
		say("Droplet: %s (ID %d) %s, %s, %s, IPv4 %s, IPv6 %s", x.Name, x.ID, x.Status, x.Size, x.Region, dash(x.IPv4), dash(x.IPv6))
	} else {
		say("Droplet: none")
	}
	if f := rep.Firewall; f != nil {
		if f.Found {
			say("Firewall: %s (%s)", f.Name, f.ID)
		} else {
			say("Firewall: %s is missing", f.Name)
		}
	}
	if x := rep.DNS; x != nil {
		switch {
		case x.Found:
			say("DNS: %s A %s", x.Domain, x.Data)
		case x.Zone == "":
			say("DNS: %s is not in DigitalOcean DNS", x.Domain)
		default:
			say("DNS: no A record for %s", x.Domain)
		}
	}
	for _, line := range rep.Drift {
		say("Drift: %s", line)
	}
	if len(rep.Drift) == 0 {
		say("No drift.")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
