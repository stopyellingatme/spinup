package digitalocean

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Sources is the source of an inbound rule.
type Sources struct {
	Addresses []string `json:"addresses,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// Destinations is the destination of an outbound rule.
type Destinations struct {
	Addresses []string `json:"addresses,omitempty"`
}

// InboundRule is an inbound firewall rule.
type InboundRule struct {
	Protocol string  `json:"protocol"`
	Ports    string  `json:"ports,omitempty"`
	Sources  Sources `json:"sources"`
}

// OutboundRule is an outbound firewall rule.
type OutboundRule struct {
	Protocol     string       `json:"protocol"`
	Ports        string       `json:"ports,omitempty"`
	Destinations Destinations `json:"destinations"`
}

// Firewall is a cloud firewall.
type Firewall struct {
	ID            string         `json:"id,omitempty"`
	Name          string         `json:"name"`
	Status        string         `json:"status,omitempty"`
	Tags          []string       `json:"tags"`
	DropletIDs    []int          `json:"droplet_ids"`
	InboundRules  []InboundRule  `json:"inbound_rules"`
	OutboundRules []OutboundRule `json:"outbound_rules"`
}

// Firewalls lists all firewalls of the account.
func (c *Client) Firewalls(ctx context.Context) ([]Firewall, error) {
	return listAll[Firewall](ctx, c, "/v2/firewalls", nil, "firewalls")
}

// EnsureFirewall finds the firewall by its exact name. It creates the firewall
// when it is missing. It replaces the firewall when the tags or rules differ,
// and it keeps the droplet IDs that the firewall has. It reports a change.
func (c *Client) EnsureFirewall(ctx context.Context, want Firewall) (Firewall, bool, error) {
	all, err := c.Firewalls(ctx)
	if err != nil {
		return Firewall{}, false, err
	}
	i := slices.IndexFunc(all, func(f Firewall) bool { return f.Name == want.Name })
	body := want
	body.ID, body.Status = "", ""
	body.Tags = nonNil(body.Tags)
	body.DropletIDs = nonNil(body.DropletIDs)
	body.InboundRules = nonNil(body.InboundRules)
	body.OutboundRules = nonNil(body.OutboundRules)
	var out struct {
		Firewall Firewall `json:"firewall"`
	}
	if i < 0 {
		err = c.do(ctx, http.MethodPost, "/v2/firewalls", body, &out)
		return out.Firewall, err == nil, err
	}
	have := all[i]
	if sameFirewall(have, want) {
		return have, false, nil
	}
	body.DropletIDs = nonNil(have.DropletIDs)
	err = c.do(ctx, http.MethodPut, "/v2/firewalls/"+url.PathEscape(have.ID), body, &out)
	return out.Firewall, err == nil, err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func sameFirewall(a, b Firewall) bool {
	return slices.Equal(sortedCopy(a.Tags), sortedCopy(b.Tags)) &&
		slices.Equal(inboundKeys(a.InboundRules), inboundKeys(b.InboundRules)) &&
		slices.Equal(outboundKeys(a.OutboundRules), outboundKeys(b.OutboundRules))
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

// normPorts treats an empty value, "0", and "all" as the same.
func normPorts(p string) string {
	if p == "" || p == "0" {
		return "all"
	}
	return p
}

func inboundKeys(rs []InboundRule) []string {
	keys := make([]string, 0, len(rs))
	for _, r := range rs {
		keys = append(keys, strings.Join([]string{
			r.Protocol, normPorts(r.Ports),
			strings.Join(sortedCopy(r.Sources.Addresses), ","),
			strings.Join(sortedCopy(r.Sources.Tags), ","),
		}, "|"))
	}
	sort.Strings(keys)
	return keys
}

func outboundKeys(rs []OutboundRule) []string {
	keys := make([]string, 0, len(rs))
	for _, r := range rs {
		keys = append(keys, strings.Join([]string{
			r.Protocol, normPorts(r.Ports),
			strings.Join(sortedCopy(r.Destinations.Addresses), ","),
		}, "|"))
	}
	sort.Strings(keys)
	return keys
}

// DeleteFirewall deletes a firewall. A firewall that does not exist is not an error.
func (c *Client) DeleteFirewall(ctx context.Context, id string) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/v2/firewalls/"+url.PathEscape(id), nil, nil))
}
