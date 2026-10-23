package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// pollInterval is the wait between two polls in WaitActive.
const pollInterval = 5 * time.Second

// Network is one address of a droplet.
type Network struct {
	IPAddress string `json:"ip_address"`
	Type      string `json:"type"`
}

// Networks holds the addresses of a droplet.
type Networks struct {
	V4 []Network `json:"v4"`
	V6 []Network `json:"v6"`
}

// Droplet is a virtual machine.
type Droplet struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Tags   []string `json:"tags"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	SizeSlug string `json:"size_slug"`
	Image    struct {
		Slug         string `json:"slug"`
		Distribution string `json:"distribution"`
	} `json:"image"`
	Networks Networks `json:"networks"`
}

func firstPublic(ns []Network) string {
	for _, n := range ns {
		if n.Type == "public" {
			return n.IPAddress
		}
	}
	return ""
}

// PublicIPv4 returns the public IPv4 address, or an empty string.
func (d Droplet) PublicIPv4() string { return firstPublic(d.Networks.V4) }

// PublicIPv6 returns the public IPv6 address, or an empty string.
func (d Droplet) PublicIPv6() string { return firstPublic(d.Networks.V6) }

// DropletRequest is the body of a droplet create. SSHKeys holds fingerprints.
type DropletRequest struct {
	Name       string   `json:"name"`
	Region     string   `json:"region"`
	Size       string   `json:"size"`
	Image      string   `json:"image"`
	SSHKeys    []string `json:"ssh_keys,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	UserData   string   `json:"user_data,omitempty"`
	IPv6       bool     `json:"ipv6"`
	Monitoring bool     `json:"monitoring"`
	Backups    bool     `json:"backups"`
}

// DropletsByTag lists the droplets that have the tag.
func (c *Client) DropletsByTag(ctx context.Context, tag string) ([]Droplet, error) {
	return listAll[Droplet](ctx, c, "/v2/droplets", url.Values{"tag_name": {tag}}, "droplets")
}

// Droplet reads one droplet.
func (c *Client) Droplet(ctx context.Context, id int) (Droplet, error) {
	var out struct {
		Droplet Droplet `json:"droplet"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/droplets/"+strconv.Itoa(id), nil, &out)
	return out.Droplet, err
}

// CreateDroplet creates a droplet. It never retries. If the call fails, the
// caller finds the droplet again by its tag on the next run.
func (c *Client) CreateDroplet(ctx context.Context, req DropletRequest) (Droplet, error) {
	var out struct {
		Droplet Droplet `json:"droplet"`
	}
	err := c.do(ctx, http.MethodPost, "/v2/droplets", req, &out)
	return out.Droplet, err
}

// WaitActive polls every 5 seconds until the droplet is active and has a
// public IPv4 address.
func (c *Client) WaitActive(ctx context.Context, id int, timeout time.Duration) (Droplet, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		d, err := c.Droplet(ctx, id)
		if err != nil {
			return Droplet{}, waitError(ctx, id, timeout, err)
		}
		if d.Status == "active" && d.PublicIPv4() != "" {
			return d, nil
		}
		if err := c.sleep(ctx, pollInterval); err != nil {
			return Droplet{}, waitError(ctx, id, timeout, err)
		}
	}
}

func waitError(ctx context.Context, id int, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("droplet %d was not active after %s\nTry: wait and run the command again, because the next run continues with the same droplet", id, timeout)
	}
	return err
}

// DeleteDroplet deletes a droplet. A droplet that does not exist is not an error.
func (c *Client) DeleteDroplet(ctx context.Context, id int) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/v2/droplets/"+strconv.Itoa(id), nil, nil))
}

func ignoreNotFound(err error) error {
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}
