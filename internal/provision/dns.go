package provision

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
)

const (
	defaultDNSTimeout = 10 * time.Minute
	dnsPollInterval   = 10 * time.Second
)

// ensureDNS points the domain at the droplet (ADR 0017 §8).
// It uses DigitalOcean DNS when the account holds the zone. Otherwise it waits for the user.
func (r *run) ensureDNS(ctx context.Context) error {
	if r.domain == "" {
		return nil
	}
	zone, name, found, err := r.d.Cloud.FindZone(ctx, r.domain)
	if err != nil {
		return failWrap("look up the DNS zone", err, "run the command again")
	}
	if !found {
		return r.waitForDNS(ctx)
	}
	rec, created, err := r.d.Cloud.EnsureRecord(ctx, zone, digitalocean.Record{Type: "A", Name: name, Data: r.ip, TTL: digitalocean.DefaultRecordTTL})
	if err != nil {
		return failWrap("make the DNS record", err, "run the command again")
	}
	if created {
		r.say("Set the DNS record %s A %s in the zone %s.", r.domain, rec.Data, zone)
	} else {
		r.say("The DNS record %s A %s exists.", r.domain, rec.Data)
	}
	aaaa, err := r.d.Cloud.Records(ctx, zone, "AAAA", name)
	if err != nil {
		return failWrap("look up the AAAA records", err, "run the command again")
	}
	v6 := r.droplet.PublicIPv6()
	for _, a := range aaaa {
		if a.Data != v6 {
			r.warn("Warning: %s has an AAAA record for %s that does not point at this droplet. Clients that use IPv6 reach another host.", zone, r.domain)
			break
		}
	}
	return nil
}

// waitForDNS prints the record and polls the resolver until it returns the droplet address.
func (r *run) waitForDNS(ctx context.Context) error {
	timeout := r.o.DNSTimeout
	if timeout <= 0 {
		timeout = defaultDNSTimeout
	}
	r.say("Add this DNS record: %s A %s", r.domain, r.ip)
	want, err := netip.ParseAddr(r.ip)
	if err != nil {
		return failWrap("read the droplet address", err, "run the command again")
	}
	start := r.d.Now()
	for {
		addrs, rerr := r.d.Resolve(ctx, r.domain)
		for _, a := range addrs {
			if a.Unmap() == want {
				r.say("The DNS record resolves to %s.", r.ip)
				return nil
			}
		}
		_ = rerr // A failed lookup means the record does not exist yet.
		if r.d.Now().Sub(start) >= timeout {
			return fail("wait for the DNS record", "after "+timeout.String()+", "+r.domain+" does not resolve to "+r.ip,
				"add the record "+r.domain+" A "+r.ip+" at your DNS provider, then run the command again")
		}
		if err := r.d.Sleep(ctx, dnsPollInterval); err != nil {
			return err
		}
	}
}

// sslipHost reports whether the URL uses the IP DNS service.
func sslipURL(url string) bool {
	return strings.HasSuffix(strings.TrimSuffix(url, "/"), "."+defaults().IPDNS)
}
