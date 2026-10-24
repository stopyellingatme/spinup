package dofake

import (
	"crypto/md5" //nolint:gosec // DigitalOcean shows MD5 fingerprints.
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// SSHKey is an account SSH key in the fake.
type SSHKey struct {
	ID          int
	Name        string
	PublicKey   string
	Fingerprint string
}

// Droplet is a droplet in the fake. Raw is the decoded create request.
type Droplet struct {
	ID       int
	Name     string
	Status   string
	Region   string
	Size     string
	Image    string
	Tags     []string
	SSHKeys  []string
	UserData string
	IPv6     bool
	Raw      map[string]any
}

// Record is a DNS record in the fake. Name is relative to the zone.
type Record struct {
	ID   int
	Type string
	Name string
	Data string
	TTL  int
}

// Name rules from the DigitalOcean OpenAPI specification.
var (
	tagNamePattern      = regexp.MustCompile(`^[a-zA-Z0-9_\-:]+$`)
	firewallNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]+$`)
	dropletNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9]?[a-z0-9A-Z.\-]*[a-z0-9A-Z]$`)
	portPattern         = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?$`)
)

const maxTagName = 255

func validTagName(n string) bool { return len(n) <= maxTagName && tagNamePattern.MatchString(n) }

type dropletState struct {
	Droplet
	polls int
}

type sizeInfo struct {
	slug         string
	vcpus, mem   int
	disk         int
	monthly, hrs float64
}

var sizes = []sizeInfo{
	{"s-1vcpu-1gb", 1, 1024, 25, 6, 0.00893},
	{"s-1vcpu-2gb", 1, 2048, 50, 12, 0.01786},
	{"s-2vcpu-2gb", 2, 2048, 60, 18, 0.02679},
	{"s-2vcpu-4gb", 2, 4096, 80, 24, 0.03571},
}

var regions = map[string]string{"nyc3": "New York 3", "fra1": "Frankfurt 1", "ams3": "Amsterdam 3"}

func regionSlugs() []string {
	return []string{"ams3", "fra1", "nyc3"}
}

// Droplets returns a snapshot of the droplets.
func (s *Server) Droplets() []Droplet {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Droplet, 0, len(s.droplets))
	for _, d := range s.droplets {
		c := d.Droplet
		c.Tags = slices.Clone(c.Tags)
		c.SSHKeys = slices.Clone(c.SSHKeys)
		c.Raw = clone(c.Raw)
		out = append(out, c)
	}
	return out
}

// Firewalls returns a snapshot of the firewalls as decoded JSON objects.
func (s *Server) Firewalls() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.firewalls)
}

// Records returns a snapshot of the records of a zone.
func (s *Server) Records(zone string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records[strings.ToLower(zone)])
}

// Tags returns a snapshot of the account tags.
func (s *Server) Tags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tags)
}

// SSHKeys returns a snapshot of the account SSH keys.
func (s *Server) SSHKeys() []SSHKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sshKeys)
}

func (s *Server) id() int {
	s.nextID++
	return s.nextID
}

func (s *Server) api(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.bearerOK(r) {
		apiError(w, 401, "unauthorized", "Unable to authenticate you")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v2/"), "/"), "/")
	m := r.Method
	switch {
	case parts[0] == "account" && len(parts) == 1 && m == "GET":
		s.account(w)
	case parts[0] == "account" && len(parts) >= 2 && parts[1] == "keys":
		s.keys(w, m, parts[2:], body)
	case parts[0] == "droplets":
		s.dropletRoutes(w, r, parts[1:], body)
	case parts[0] == "firewalls":
		s.firewallRoutes(w, r, parts[1:], body)
	case parts[0] == "tags":
		s.tagRoutes(w, m, parts[1:], body)
	case parts[0] == "domains":
		s.domainRoutes(w, r, parts[1:], body)
	case parts[0] == "sizes" && len(parts) == 1 && m == "GET":
		s.paginate(w, r, "sizes", sizeList())
	case parts[0] == "regions" && len(parts) == 1 && m == "GET":
		s.paginate(w, r, "regions", regionList())
	default:
		notFound(w)
	}
}

func (s *Server) account(w http.ResponseWriter) {
	acc := map[string]any{
		"droplet_limit": s.o.DropletLimit, "email": s.o.Email, "uuid": "fake-user-uuid",
		"email_verified": true, "status": "active",
	}
	if s.o.TeamUUID != "" {
		acc["team"] = map[string]string{"uuid": s.o.TeamUUID, "name": s.o.TeamName}
	}
	writeJSON(w, 200, map[string]any{"account": acc})
}

// paginate answers a list with per_page, page, links.pages.next, and meta.total.
func (s *Server) paginate(w http.ResponseWriter, r *http.Request, key string, items []any) {
	q := r.URL.Query()
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 {
		per = 20
	}
	per = min(per, 200)
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	lo := min((page-1)*per, len(items))
	hi := min(lo+per, len(items))
	out := map[string]any{key: items[lo:hi], "meta": map[string]int{"total": len(items)}}
	pages := map[string]string{}
	link := func(p int) string {
		n := url.Values(clone(map[string][]string(q)))
		n.Set("page", strconv.Itoa(p))
		n.Set("per_page", strconv.Itoa(per))
		return "http://" + r.Host + r.URL.Path + "?" + n.Encode()
	}
	if hi < len(items) {
		pages["next"] = link(page + 1)
		pages["last"] = link((len(items) + per - 1) / per)
	}
	if page > 1 {
		pages["prev"] = link(page - 1)
	}
	out["links"] = map[string]any{"pages": pages}
	writeJSON(w, 200, out)
}

func sizeList() []any {
	var out []any
	for _, z := range sizes {
		out = append(out, map[string]any{
			"slug": z.slug, "vcpus": z.vcpus, "memory": z.mem, "disk": z.disk,
			"price_monthly": z.monthly, "price_hourly": z.hrs, "regions": regionSlugs(),
			"available": true, "transfer": 1, "description": "Basic",
		})
	}
	return out
}

func regionList() []any {
	var out []any
	for _, slug := range regionSlugs() {
		var zs []string
		for _, z := range sizes {
			zs = append(zs, z.slug)
		}
		out = append(out, map[string]any{"slug": slug, "name": regions[slug], "available": true, "sizes": zs})
	}
	return out
}

func decode(w http.ResponseWriter, body []byte) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		apiError(w, 400, "bad_request", "invalid JSON body")
		return nil, false
	}
	return m, true
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func strs(m map[string]any, k string) []string {
	var out []string
	l, _ := m[k].([]any)
	for _, v := range l {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// ---- SSH keys ----

func fingerprint(pub string) (string, bool) {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return "", false
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) == 0 {
		return "", false
	}
	sum := md5.Sum(blob) //nolint:gosec // see the import.
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":"), true
}

func keyJSON(k SSHKey) map[string]any {
	return map[string]any{"id": k.ID, "fingerprint": k.Fingerprint, "public_key": k.PublicKey, "name": k.Name}
}

func (s *Server) findKey(ref string) *SSHKey {
	for i := range s.sshKeys {
		k := &s.sshKeys[i]
		if k.Fingerprint == ref || strconv.Itoa(k.ID) == ref {
			return k
		}
	}
	return nil
}

func (s *Server) keys(w http.ResponseWriter, m string, rest []string, body []byte) {
	switch {
	case len(rest) == 0 && m == "GET":
		var items []any
		for _, k := range s.sshKeys {
			items = append(items, keyJSON(k))
		}
		writeJSON(w, 200, map[string]any{"ssh_keys": items, "meta": map[string]int{"total": len(items)}})
	case len(rest) == 0 && m == "POST":
		req, ok := decode(w, body)
		if !ok {
			return
		}
		fp, valid := fingerprint(str(req, "public_key"))
		switch {
		case !valid:
			apiError(w, 422, "unprocessable_entity", "SSH Key is invalid or incorrect format")
		case s.findKey(fp) != nil:
			apiError(w, 422, "unprocessable_entity", "SSH Key is already in use on your account")
		default:
			k := SSHKey{ID: s.id(), Name: str(req, "name"), PublicKey: str(req, "public_key"), Fingerprint: fp}
			s.sshKeys = append(s.sshKeys, k)
			writeJSON(w, 201, map[string]any{"ssh_key": keyJSON(k)})
		}
	case len(rest) == 1 && m == "GET":
		if k := s.findKey(rest[0]); k != nil {
			writeJSON(w, 200, map[string]any{"ssh_key": keyJSON(*k)})
			return
		}
		notFound(w)
	default:
		notFound(w)
	}
}

// ---- droplets ----

func (s *Server) dropletJSON(d *dropletState) map[string]any {
	nets := map[string]any{"v4": []any{}, "v6": []any{}}
	if d.Status == "active" {
		nets["v4"] = []any{
			map[string]string{"ip_address": s.o.IPv4, "type": "public", "netmask": "255.255.240.0", "gateway": "203.0.113.1"},
			map[string]string{"ip_address": "10.116.0.2", "type": "private"},
		}
		if d.IPv6 {
			nets["v6"] = []any{map[string]any{"ip_address": s.o.IPv6, "type": "public", "netmask": 64}}
		}
	}
	return map[string]any{
		"id": d.ID, "name": d.Name, "status": d.Status, "tags": nonNil(d.Tags),
		"region":    map[string]any{"slug": d.Region, "name": regions[d.Region]},
		"size_slug": d.Size, "size": map[string]any{"slug": d.Size},
		"image":    map[string]any{"slug": d.Image, "distribution": "Ubuntu"},
		"networks": nets, "vcpus": 1, "memory": 1024, "disk": 25, "locked": false,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) findDroplet(ref string) *dropletState {
	id, err := strconv.Atoi(ref)
	if err != nil {
		return nil
	}
	for _, d := range s.droplets {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (s *Server) dropletRoutes(w http.ResponseWriter, r *http.Request, rest []string, body []byte) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		tag := r.URL.Query().Get("tag_name")
		var items []any
		for _, d := range s.droplets {
			if tag == "" || slices.Contains(d.Tags, tag) {
				items = append(items, s.dropletJSON(d))
			}
		}
		s.paginate(w, r, "droplets", items)
	case len(rest) == 0 && r.Method == http.MethodPost:
		s.createDroplet(w, body)
	case len(rest) == 1 && r.Method == http.MethodGet:
		d := s.findDroplet(rest[0])
		if d == nil {
			notFound(w)
			return
		}
		d.polls++
		if d.Status == "new" && d.polls >= s.o.ActivateAfter {
			d.Status = "active"
		}
		writeJSON(w, 200, map[string]any{"droplet": s.dropletJSON(d)})
	case len(rest) == 1 && r.Method == http.MethodDelete:
		d := s.findDroplet(rest[0])
		if d == nil {
			notFound(w)
			return
		}
		s.droplets = slices.DeleteFunc(s.droplets, func(x *dropletState) bool { return x == d })
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}

func (s *Server) createDroplet(w http.ResponseWriter, body []byte) {
	req, ok := decode(w, body)
	if !ok {
		return
	}
	if hook := s.o.OnCreateDroplet; hook != nil {
		// Unlock so the hook can call methods of the server.
		s.mu.Unlock()
		err := hook(clone(req))
		s.mu.Lock()
		if err != nil {
			apiError(w, 422, "unprocessable_entity", err.Error())
			return
		}
	}
	d := Droplet{
		Name: str(req, "name"), Region: str(req, "region"), Size: str(req, "size"), Image: str(req, "image"),
		Tags: strs(req, "tags"), SSHKeys: strs(req, "ssh_keys"), UserData: str(req, "user_data"),
		Status: "new", Raw: req,
	}
	d.IPv6, _ = req["ipv6"].(bool)
	switch {
	case d.Name == "" || d.Image == "":
		apiError(w, 422, "unprocessable_entity", "Name and image are required")
		return
	case !dropletNamePattern.MatchString(d.Name):
		apiError(w, 422, "unprocessable_entity", "Only valid hostname characters are allowed. (a-z, A-Z, 0-9, . and -)")
		return
	case regions[d.Region] == "":
		apiError(w, 422, "unprocessable_entity", "Region is invalid")
		return
	case !slices.ContainsFunc(sizes, func(z sizeInfo) bool { return z.slug == d.Size }):
		apiError(w, 422, "unprocessable_entity", "Size is invalid")
		return
	case len(s.droplets) >= s.o.DropletLimit:
		apiError(w, 422, "unprocessable_entity", "creating this/these droplet(s) will exceed your droplet limit")
		return
	}
	for _, ref := range d.SSHKeys {
		if s.findKey(ref) == nil {
			apiError(w, 422, "unprocessable_entity", "You specified an invalid SSH Key")
			return
		}
	}
	for _, t := range d.Tags {
		if !validTagName(t) {
			apiError(w, 422, "unprocessable_entity", "tag name "+t+" is invalid: use letters, numbers, colons, dashes, and underscores")
			return
		}
	}
	// A droplet create makes its tags, as the real API does.
	for _, t := range d.Tags {
		s.addTag(t)
	}
	d.ID = s.id()
	st := &dropletState{Droplet: d}
	s.droplets = append(s.droplets, st)
	writeJSON(w, 202, map[string]any{"droplet": s.dropletJSON(st), "links": map[string]any{}})
}

// ---- tags ----

func (s *Server) addTag(name string) {
	if !slices.Contains(s.tags, name) {
		s.tags = append(s.tags, name)
	}
}

func (s *Server) tagRoutes(w http.ResponseWriter, m string, rest []string, body []byte) {
	switch {
	case len(rest) == 0 && m == "POST":
		req, ok := decode(w, body)
		if !ok {
			return
		}
		name := str(req, "name")
		if name == "" {
			apiError(w, 422, "unprocessable_entity", "name is required")
			return
		}
		switch {
		case !validTagName(name):
			apiError(w, 422, "unprocessable_entity", "tag name is invalid: use letters, numbers, colons, dashes, and underscores, up to 255 characters")
			return
		case slices.Contains(s.tags, name):
			apiError(w, 422, "unprocessable_entity", "tag already exists")
			return
		}
		s.addTag(name)
		writeJSON(w, 201, map[string]any{"tag": map[string]any{"name": name}})
	case len(rest) == 1 && m == "GET":
		if !slices.Contains(s.tags, rest[0]) {
			notFound(w)
			return
		}
		n := 0
		for _, d := range s.droplets {
			if slices.Contains(d.Tags, rest[0]) {
				n++
			}
		}
		writeJSON(w, 200, map[string]any{"tag": map[string]any{"name": rest[0], "resources": map[string]any{"count": n}}})
	case len(rest) == 1 && m == "DELETE":
		if !slices.Contains(s.tags, rest[0]) {
			notFound(w)
			return
		}
		s.tags = slices.DeleteFunc(s.tags, func(t string) bool { return t == rest[0] })
		for _, d := range s.droplets {
			d.Tags = slices.DeleteFunc(d.Tags, func(t string) bool { return t == rest[0] })
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}

// ---- firewalls ----

func (s *Server) findFirewall(id string) int {
	return slices.IndexFunc(s.firewalls, func(f map[string]any) bool { return f["id"] == id })
}

func (s *Server) firewallRoutes(w http.ResponseWriter, r *http.Request, rest []string, body []byte) {
	m := r.Method
	switch {
	case len(rest) == 0 && m == "GET":
		items := make([]any, 0, len(s.firewalls))
		for _, f := range s.firewalls {
			items = append(items, clone(f))
			f["status"] = "succeeded"
		}
		s.paginate(w, r, "firewalls", items)
	case len(rest) == 0 && m == "POST", len(rest) == 1 && m == "PUT":
		req, ok := decode(w, body)
		if !ok {
			return
		}
		if !firewallNamePattern.MatchString(str(req, "name")) {
			apiError(w, 422, "unprocessable_entity", "name must begin with a letter or digit and hold only letters, digits, periods, and dashes")
			return
		}
		if msg := checkRules(req); msg != "" {
			apiError(w, 422, "unprocessable_entity", msg)
			return
		}
		// The real API refuses a firewall with a tag that does not exist.
		for _, t := range strs(req, "tags") {
			if !slices.Contains(s.tags, t) {
				apiError(w, 422, "unprocessable_entity", "tag "+t+" does not exist")
				return
			}
		}
		// A change is pending until the next read, as in the real API.
		fw := map[string]any{"name": req["name"], "status": "waiting"}
		for _, k := range []string{"tags", "droplet_ids", "inbound_rules", "outbound_rules"} {
			switch {
			case req[k] == nil:
				fw[k] = []any{}
			case k == "inbound_rules" || k == "outbound_rules":
				fw[k] = canonRules(req[k])
			default:
				fw[k] = req[k]
			}
		}
		if m == "POST" {
			fw["id"] = fmt.Sprintf("fw-%d", s.id())
			s.firewalls = append(s.firewalls, fw)
			writeJSON(w, 202, map[string]any{"firewall": fw})
			return
		}
		i := s.findFirewall(rest[0])
		if i < 0 {
			notFound(w)
			return
		}
		fw["id"] = rest[0]
		s.firewalls[i] = fw
		writeJSON(w, 200, map[string]any{"firewall": fw})
	case len(rest) == 1 && m == "GET":
		if i := s.findFirewall(rest[0]); i >= 0 {
			writeJSON(w, 200, map[string]any{"firewall": clone(s.firewalls[i])})
			s.firewalls[i]["status"] = "succeeded"
			return
		}
		notFound(w)
	case len(rest) == 1 && m == "DELETE":
		i := s.findFirewall(rest[0])
		if i < 0 {
			notFound(w)
			return
		}
		s.firewalls = slices.Delete(s.firewalls, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}

// ---- domains ----

func (s *Server) domainRoutes(w http.ResponseWriter, r *http.Request, rest []string, body []byte) {
	if len(rest) == 0 && r.Method == http.MethodGet {
		items := make([]any, 0, len(s.domains))
		for _, d := range s.domains {
			items = append(items, map[string]any{"name": d, "ttl": 1800})
		}
		s.paginate(w, r, "domains", items)
		return
	}
	if len(rest) < 2 || rest[1] != "records" || !slices.Contains(s.domains, strings.ToLower(rest[0])) {
		notFound(w)
		return
	}
	zone := strings.ToLower(rest[0])
	rest = rest[2:]
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		q := r.URL.Query()
		var items []any
		for _, rec := range s.records[zone] {
			fqdn := zone
			if rec.Name != "@" {
				fqdn = rec.Name + "." + zone
			}
			if (q.Get("type") == "" || q.Get("type") == rec.Type) && (q.Get("name") == "" || strings.EqualFold(q.Get("name"), fqdn)) {
				items = append(items, recJSON(rec))
			}
		}
		s.paginate(w, r, "domain_records", items)
	case len(rest) == 0 && r.Method == http.MethodPost:
		req, ok := decode(w, body)
		if !ok {
			return
		}
		ttl, _ := req["ttl"].(float64)
		rec := Record{ID: s.id(), Type: str(req, "type"), Name: str(req, "name"), Data: str(req, "data"), TTL: int(ttl)}
		if rec.Type == "" || rec.Data == "" {
			apiError(w, 422, "unprocessable_entity", "type and data are required")
			return
		}
		s.records[zone] = append(s.records[zone], rec)
		writeJSON(w, 201, map[string]any{"domain_record": recJSON(rec)})
	case len(rest) == 1 && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		i := slices.IndexFunc(s.records[zone], func(x Record) bool { return strconv.Itoa(x.ID) == rest[0] })
		if i < 0 {
			notFound(w)
			return
		}
		if r.Method == http.MethodDelete {
			s.records[zone] = slices.Delete(s.records[zone], i, i+1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		req, ok := decode(w, body)
		if !ok {
			return
		}
		rec := &s.records[zone][i]
		if v := str(req, "name"); v != "" {
			rec.Name = v
		}
		if v := str(req, "data"); v != "" {
			rec.Data = v
		}
		if v, ok := req["ttl"].(float64); ok {
			rec.TTL = int(v)
		}
		writeJSON(w, 200, map[string]any{"domain_record": recJSON(*rec)})
	default:
		notFound(w)
	}
}

func recJSON(r Record) map[string]any {
	return map[string]any{"id": r.ID, "type": r.Type, "name": r.Name, "data": r.Data, "ttl": r.TTL}
}

// checkRules validates the rules of a firewall body. It returns an error
// message, or an empty string.
func checkRules(req map[string]any) string {
	for _, k := range []string{"inbound_rules", "outbound_rules"} {
		rules, _ := req[k].([]any)
		for _, x := range rules {
			rule, _ := x.(map[string]any)
			proto := str(rule, "protocol")
			if !slices.Contains([]string{"tcp", "udp", "icmp"}, proto) {
				return "protocol must be tcp, udp, or icmp"
			}
			ports := str(rule, "ports")
			if proto == "icmp" {
				continue
			}
			if ports != "" && ports != "all" && ports != "0" && !portPattern.MatchString(ports) {
				return "ports must be a port, a range, or all"
			}
		}
	}
	return ""
}

// canonRules writes the ports of each rule as the real API answers them:
// "0" for all ports and for ICMP.
func canonRules(v any) any {
	rules, _ := v.([]any)
	out := make([]any, 0, len(rules))
	for _, x := range rules {
		rule, _ := x.(map[string]any)
		c := map[string]any{}
		for k, val := range rule {
			c[k] = val
		}
		if p := str(rule, "ports"); p == "" || p == "all" || str(rule, "protocol") == "icmp" {
			c["ports"] = "0"
		}
		out = append(out, c)
	}
	return out
}
