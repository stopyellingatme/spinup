package digitalocean

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultRecordTTL is the TTL of records that spinup creates.
const DefaultRecordTTL = 300

// Domain is a DNS zone in DigitalOcean.
type Domain struct {
	Name string `json:"name"`
	TTL  int    `json:"ttl"`
}

// Record is a DNS record. Name is relative to the zone ("@" is the apex).
type Record struct {
	ID   int    `json:"id,omitempty"`
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

// Domains lists the zones of the account.
func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	return listAll[Domain](ctx, c, "/v2/domains", nil, "domains")
}

// FindZone finds the zone with the longest suffix match on dot boundaries.
// name is "@" for the apex, else the part before the zone.
func (c *Client) FindZone(ctx context.Context, fqdn string) (zone, name string, found bool, err error) {
	domains, err := c.Domains(ctx)
	if err != nil {
		return "", "", false, err
	}
	fqdn = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	for _, d := range domains {
		z := strings.ToLower(strings.TrimSuffix(d.Name, "."))
		if fqdn != z && !strings.HasSuffix(fqdn, "."+z) {
			continue
		}
		if len(z) > len(zone) {
			zone = z
		}
	}
	if zone == "" {
		return "", "", false, nil
	}
	if fqdn == zone {
		return zone, "@", true, nil
	}
	return zone, strings.TrimSuffix(fqdn, "."+zone), true, nil
}

func recordFQDN(zone, name string) string {
	if name == "" || name == "@" {
		return zone
	}
	return name + "." + zone
}

// Records lists the records of a zone. The name filter of the API takes the
// fully qualified name (for example app.example.com), so Records takes the
// name relative to the zone ("app" or "@") and builds the FQDN. An empty typ
// or name means no filter.
func (c *Client) Records(ctx context.Context, zone, typ, name string) ([]Record, error) {
	q := url.Values{}
	if typ != "" {
		q.Set("type", typ)
	}
	if name != "" {
		q.Set("name", recordFQDN(zone, name))
	}
	return listAll[Record](ctx, c, "/v2/domains/"+url.PathEscape(zone)+"/records", q, "domain_records")
}

// EnsureRecord makes one record of the type (default A) for the name. It
// updates data and TTL when they differ. It fails when the name has more than
// one such record, because it cannot know which to keep.
func (c *Client) EnsureRecord(ctx context.Context, zone string, want Record) (Record, bool, error) {
	if want.Type == "" {
		want.Type = "A"
	}
	if want.Name == "" {
		want.Name = "@"
	}
	if want.TTL == 0 {
		want.TTL = DefaultRecordTTL
	}
	have, err := c.Records(ctx, zone, want.Type, want.Name)
	if err != nil {
		return Record{}, false, err
	}
	var out struct {
		Record Record `json:"domain_record"`
	}
	base := "/v2/domains/" + url.PathEscape(zone) + "/records"
	switch len(have) {
	case 0:
		want.ID = 0
		err = c.do(ctx, http.MethodPost, base, want, &out)
		return out.Record, err == nil, err
	case 1:
		r := have[0]
		if r.Data == want.Data && r.TTL == want.TTL {
			return r, false, nil
		}
		want.ID = 0
		err = c.do(ctx, http.MethodPut, base+"/"+strconv.Itoa(r.ID), want, &out)
		return out.Record, err == nil, err
	default:
		return Record{}, false, fmt.Errorf("zone %s has %d %s records for %s\nTry: delete the extra records in the DigitalOcean control panel, then run the command again", zone, len(have), want.Type, recordFQDN(zone, want.Name))
	}
}

// DeleteRecord deletes a record. A record that does not exist is not an error.
func (c *Client) DeleteRecord(ctx context.Context, zone string, id int) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/v2/domains/"+url.PathEscape(zone)+"/records/"+strconv.Itoa(id), nil, nil))
}
