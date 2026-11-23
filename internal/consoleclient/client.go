// Package consoleclient reads the console through authenticated SSH forwards.
package consoleclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stopyellingatme/spinup/internal/browser"
	"github.com/stopyellingatme/spinup/internal/consoleinstall"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options configures console access and terminal output.
type Options struct {
	Dir, Env                          string
	JSON, NoBrowser, AcceptNewHostKey bool
	Out, ErrOut                       io.Writer
}

// The VM loopback ports of the console and of Grafana (ADR 0037, sections 2 and 7).
const (
	consolePort = 7410
	grafanaPort = 3000
)

// The overview read limits (ADR 0037, section 7 and appendix A).
const (
	overviewTimeout = 6 * time.Second
	overviewLimit   = 512 << 10
)

// Test seams. Production code uses the real values.
var (
	// refresh requests a host sample when the previous sample is old or missing.
	refresh = consoleinstall.Refresh
	// openBrowser opens the verified console URL.
	openBrowser = browser.Open
	// startupBound bounds the forward and its readiness check.
	startupBound = 10 * time.Second
	// refreshInterval repeats the sample check while the console is open.
	refreshInterval = 30 * time.Second
	// aliveInterval checks that the console forward still runs.
	aliveInterval = 2 * time.Second
)

// target is the resolved deployment of one console.
type target struct {
	app, env   string
	monitoring bool
	host       remote.Host
}

// The fixed CLI diagnostics of ADR 0037, section 7.
func errDisabled() error {
	return &ui.Error{What: "Cannot open console: module console is disabled.", Try: "spinup module add console"}
}

func errNoAnswer(env string) error {
	return &ui.Error{What: "Cannot read console: the service for " + env + " did not answer.", Try: "spinup doctor --remote --env " + env}
}

func errIncompatible(env string) error {
	return &ui.Error{What: "Cannot read console: the overview has an incompatible version, identity, or shape.", Try: "upgrade the CLI and deploy the console for " + env}
}

func defaults(o *Options) {
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.ErrOut == nil {
		o.ErrOut = io.Discard
	}
}

func load(o Options) (target, error) {
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return target{}, err
	}
	cfg, err := project.Load(root)
	if err != nil {
		return target{}, err
	}
	if err = cfg.Validate(); err != nil {
		return target{}, err
	}
	if !modules.Enabled(cfg, "console") {
		return target{}, errDisabled()
	}
	env, err := cfg.ResolveEnv(o.Env)
	if err != nil {
		return target{}, err
	}
	host, err := remote.HostFromEnv(env)
	if err != nil {
		return target{}, err
	}
	profile := "absent"
	if modules.Enabled(cfg, "monitoring") {
		profile = cfg.Monitoring.Profile
	}
	return target{app: cfg.App.Name, env: env.Name, monitoring: profile == "lite" || profile == "full", host: host}, nil
}

// sample requests a host sample over its own SSH connection. A failure keeps the prior
// sample, which the overview shows with its original observation times.
func sample(ctx context.Context, t target, o Options, r runexec.Runner) {
	// No shared control master: closing it could end a connection of another command.
	vm := remote.New(t.host, r, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey, DisableMultiplexing: true})
	if err := refresh(ctx, vm, t.app); err != nil && ctx.Err() == nil {
		fmt.Fprintf(o.ErrOut, "Console sample delayed: %s. The overview shows the previous sample.\n", withoutTry(err.Error()))
	}
}

// Open opens the console and maintains its SSH forwards until cancellation.
func Open(ctx context.Context, o Options, r runexec.Runner) error {
	defaults(&o)
	t, err := load(o)
	if err != nil {
		return err
	}
	sample(ctx, t, o, r)
	port, closeConsole, err := forward(ctx, t, o, r, consolePort, consoleReady)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return forwardError(t, o, err)
	}
	// Close only the forwards that this invocation owns.
	defer func() { _ = closeConsole() }()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	if t.monitoring {
		gport, closeGrafana, gerr := forward(ctx, t, o, r, grafanaPort, grafanaReady)
		switch {
		case gerr == nil:
			defer func() { _ = closeGrafana() }()
			url += "?grafana_port=" + strconv.Itoa(gport)
		case ctx.Err() != nil:
			return nil
		default:
			// Grafana never prevents the console from opening.
			fmt.Fprintf(o.ErrOut, "Grafana did not answer, so the console shows no Grafana links: %s\nTry: spinup monitor status --env %s\n", withoutTry(gerr.Error()), t.env)
		}
	}
	fmt.Fprintf(o.Out, "Console: %s\nPress Ctrl-C to close the tunnels.\n", url)
	if !o.NoBrowser {
		if err = openBrowser(ctx, r, url); err != nil && ctx.Err() == nil {
			fmt.Fprintln(o.ErrOut, "Cannot open the browser. Open the printed URL manually.")
		}
	}
	refreshTicker := time.NewTicker(refreshInterval)
	defer refreshTicker.Stop()
	aliveTicker := time.NewTicker(aliveInterval)
	defer aliveTicker.Stop()
	address := "127.0.0.1:" + strconv.Itoa(port)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-refreshTicker.C:
			sample(ctx, t, o, r)
		case <-aliveTicker.C:
			if listening(ctx, address) {
				continue
			}
			cerr := closeConsole()
			if ctx.Err() != nil {
				return nil
			}
			if cerr == nil {
				cerr = errors.New("SSH exited while the forward was open")
			}
			return forwardError(t, o, cerr)
		}
	}
}

// Status writes a validated overview from the deployed console.
func Status(ctx context.Context, o Options, r runexec.Runner) error {
	defaults(&o)
	t, err := load(o)
	if err != nil {
		return err
	}
	sample(ctx, t, o, r)
	port, closeForward, err := forward(ctx, t, o, r, consolePort, consoleReady)
	if err != nil {
		return forwardError(t, o, err)
	}
	defer func() { _ = closeForward() }()
	body, err := readOverview(ctx, port)
	if err != nil && !errors.Is(err, errBodyLimit) {
		fmt.Fprintf(o.ErrOut, "Console overview: %s\n", err)
		return errNoAnswer(t.env)
	}
	var doc *overview
	if err == nil {
		doc, err = validate(body, t.app, t.env)
	}
	if err != nil {
		fmt.Fprintf(o.ErrOut, "Console overview: %s\n", err)
		return errIncompatible(t.env)
	}
	if o.JSON {
		// Emit the received document unchanged, with exactly one final newline.
		_, err = o.Out.Write(append(bytes.TrimRight(body, "\n"), '\n'))
		return err
	}
	_, err = io.WriteString(o.Out, render(doc))
	return err
}

// forwardError keeps SSH host key diagnostics and maps other forward failures to the
// fixed console diagnostic. The SSH cause goes to the error stream.
func forwardError(t target, o Options, err error) error {
	if remote.IsHostKeyError(err.Error()) {
		return err
	}
	fmt.Fprintf(o.ErrOut, "Console forward: %s\n", withoutTry(err.Error()))
	return errNoAnswer(t.env)
}

type readiness func(ctx context.Context, client *http.Client, address string) bool

// forward opens a verified forward to a VM loopback port. The forward and its readiness
// check share one startup bound. A forward that never becomes ready is closed.
func forward(ctx context.Context, t target, o Options, r runexec.Runner, port int, ready readiness) (int, func() error, error) {
	deadline := time.Now().Add(startupBound)
	local, closeForward, err := remote.ForwardLoopback(ctx, t.host, r, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey}, port)
	if err != nil {
		return 0, nil, err
	}
	address := "127.0.0.1:" + strconv.Itoa(local)
	client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	for {
		if ready(ctx, client, address) {
			return local, closeForward, nil
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	cerr := closeForward()
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	cause := fmt.Errorf("the health request to VM port %d through SSH did not succeed", port)
	if cerr != nil {
		cause = fmt.Errorf("%w: %s", cause, withoutTry(cerr.Error()))
	}
	return 0, nil, cause
}

// consoleReady checks the console process health route.
func consoleReady(ctx context.Context, client *http.Client, address string) bool {
	body, status, err := get(ctx, client, "http://"+address+"/healthz", 16)
	return err == nil && status == http.StatusOK && string(body) == "ok\n"
}

// grafanaReady checks the Grafana health route.
func grafanaReady(ctx context.Context, client *http.Client, address string) bool {
	body, status, err := get(ctx, client, "http://"+address+"/api/health", 64<<10)
	var health struct {
		Database string `json:"database"`
	}
	return err == nil && status == http.StatusOK && json.Unmarshal(body, &health) == nil && health.Database == "ok"
}

// get reads at most limit bytes. A longer body is an error.
func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if int64(len(body)) > limit {
		return nil, res.StatusCode, fmt.Errorf("%w of %d bytes", errBodyLimit, limit)
	}
	return body, res.StatusCode, nil
}

// errBodyLimit marks a response over its size limit. An overview over the limit is incompatible.
var errBodyLimit = errors.New("the response exceeds the size limit")

func readOverview(ctx context.Context, port int) ([]byte, error) {
	client := &http.Client{Timeout: overviewTimeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	body, status, err := get(ctx, client, "http://127.0.0.1:"+strconv.Itoa(port)+"/api/v1/overview", overviewLimit)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", status)
	}
	return body, nil
}

// listening reports whether the local SSH listener still accepts connections.
// SSH accepts a connection even when the VM service refuses it, so a refusal means SSH exited.
func listening(ctx context.Context, address string) bool {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// withoutTry removes an embedded Try line, so that each diagnostic has one.
func withoutTry(s string) string {
	before, _, _ := strings.Cut(s, "\nTry:")
	return before
}

// The overview v1 grammar (ADR 0037, section 4). The client decodes it with the standard
// library and keeps the received values, so the root module has no kit dependency.
// Unknown keys are additive in v1 and are ignored. Every listed key is required.

type kind int

const (
	kindString kind = iota
	kindTime
	kindInt
)

type field struct {
	name string
	kind kind
	null bool
	// empty permits a known empty string, independently of null.
	empty bool
	enum  []string
}

var (
	colors        = []string{"blue", "green"}
	sectionStates = []string{"ok", "disabled", "unavailable", "stale"}
	reasons       = map[string][]string{
		"disabled":    {"no_monitoring", "profile_none", "no_jobs", "no_backups", "no_wal"},
		"unavailable": {"missing", "timeout", "invalid", "source_down"},
		"stale":       {"sample_expired"},
	}
)

// records lists the fields of each wire record in schema order.
var records = map[string][]field{
	"release": {
		{name: "id"}, {name: "git_sha"}, {name: "color", enum: colors}, {name: "created_at", kind: kindTime},
		{name: "age_seconds", kind: kindInt}, {name: "previous_id", null: true},
	},
	"container": {
		{name: "project"}, {name: "service"}, {name: "name"},
		{name: "state", enum: []string{"created", "running", "paused", "restarting", "removing", "exited", "dead"}},
		{name: "health", null: true, enum: []string{"none", "starting", "healthy", "unhealthy"}},
	},
	"alert": {{name: "name"}, {name: "severity", null: true}, {name: "since", kind: kindTime, null: true}},
	"backup": {
		{name: "backend", enum: []string{"pgbackrest", "pgdump", "walg"}}, {name: "label"}, {name: "kind"},
		{name: "finished_at", kind: kindTime}, {name: "size_bytes", kind: kindInt, null: true},
	},
	"wal": {
		{name: "state", enum: []string{"off", "failing", "waiting", "archiving"}},
		{name: "archived_at", kind: kindTime, null: true}, {name: "failed_at", kind: kindTime, null: true},
		{name: "min_segment", null: true}, {name: "max_segment", null: true},
	},
	"queue": {
		{name: "queue"},
		{name: "state", enum: []string{"available", "cancelled", "completed", "discarded", "pending", "retryable", "running", "scheduled"}},
		{name: "count", kind: kindInt},
	},
	"space": {{name: "total_bytes", kind: kindInt}, {name: "used_bytes", kind: kindInt}, {name: "available_bytes", kind: kindInt}},
	"attempt": {
		{name: "id"}, {name: "at", kind: kindTime}, {name: "user", null: true, empty: true}, {name: "git_sha", null: true},
		{name: "color", null: true, enum: colors},
		{name: "result", enum: []string{"failed", "succeeded", "incomplete", "recorded"}},
	},
}

type sectionSpec struct {
	key, caption, record string
	array                bool
	// nullData permits a successful section without data: a known absence.
	nullData bool
}

// sectionSpecs lists the nine sections in schema order with their terminal captions.
var sectionSpecs = []sectionSpec{
	{key: "release", caption: "Live release", record: "release", nullData: true},
	{key: "containers", caption: "Containers", record: "container", array: true},
	{key: "alerts", caption: "Firing alerts", record: "alert", array: true},
	{key: "backup", caption: "Last backup", record: "backup", nullData: true},
	{key: "wal", caption: "WAL archive", record: "wal"},
	{key: "jobs", caption: "Job queues", record: "queue", array: true},
	{key: "disk", caption: "Disk", record: "space"},
	{key: "memory", caption: "Memory", record: "space"},
	{key: "history", caption: "Deploy history", record: "attempt", array: true},
}

// overview is a validated document with its received values.
type overview struct {
	app, env, observedAt string
	sections             []section
}

type section struct {
	spec          sectionSpec
	state, reason string
	// items holds the data records. It is empty for null data and for an empty array.
	items []map[string]json.RawMessage
}

// validate checks the version, the identity, and the shape of an overview document.
func validate(body []byte, app, env string) (*overview, error) {
	if !utf8.Valid(body) {
		return nil, errors.New("the document is not valid UTF-8")
	}
	top, err := object(body)
	if err != nil {
		return nil, fmt.Errorf("the document: %w", err)
	}
	if raw, ok := top["version"]; !ok || string(raw) != "1" {
		return nil, errors.New("the document version is not 1")
	}
	d := &overview{}
	for _, item := range []struct {
		key, want string
		dst       *string
	}{{"app", app, &d.app}, {"env", env, &d.env}} {
		got, err := str(top, field{name: item.key})
		if err != nil {
			return nil, err
		}
		if got != item.want {
			return nil, fmt.Errorf("the overview is for %s %q, not %q", item.key, got, item.want)
		}
		*item.dst = got
	}
	if _, err = str(top, field{name: "profile", enum: []string{"absent", "none", "lite", "full"}}); err != nil {
		return nil, err
	}
	if d.observedAt, err = str(top, field{name: "observed_at", kind: kindTime, null: true}); err != nil {
		return nil, err
	}
	for _, spec := range sectionSpecs {
		raw, ok := top[spec.key]
		if !ok {
			return nil, fmt.Errorf("the section %s is missing", spec.key)
		}
		s, err := checkSection(raw, spec)
		if err != nil {
			return nil, fmt.Errorf("the section %s: %w", spec.key, err)
		}
		d.sections = append(d.sections, s)
	}
	return d, nil
}

func checkSection(raw json.RawMessage, spec sectionSpec) (section, error) {
	m, err := object(raw)
	if err != nil {
		return section{}, err
	}
	s := section{spec: spec}
	if s.state, err = str(m, field{name: "state", enum: sectionStates}); err != nil {
		return s, err
	}
	reason := field{name: "reason", null: s.state == "ok", enum: reasons[s.state]}
	if s.reason, err = str(m, reason); err != nil {
		return s, err
	}
	if s.state == "ok" && s.reason != "" {
		return s, errors.New("an ok section has a reason")
	}
	at := field{name: "at", kind: kindTime, null: s.state == "disabled" || s.state == "unavailable"}
	when, err := str(m, at)
	if err != nil {
		return s, err
	}
	if s.state == "disabled" && when != "" {
		return s, errors.New("a disabled section has an observation time")
	}
	data, ok := m["data"]
	if !ok {
		return s, errors.New("the key data is missing")
	}
	null := isNull(data)
	switch {
	case s.state == "disabled" || s.state == "unavailable":
		if !null {
			return s, fmt.Errorf("a %s section has data", s.state)
		}
		return s, nil
	case null && spec.nullData:
		return s, nil
	case null:
		return s, errors.New("the data is null")
	}
	var list []json.RawMessage
	if spec.array {
		if data[0] != '[' || json.Unmarshal(data, &list) != nil {
			return s, errors.New("the data is not an array")
		}
	} else {
		list = []json.RawMessage{data}
	}
	for _, item := range list {
		record, err := object(item)
		if err != nil {
			return s, fmt.Errorf("a %s record: %w", spec.record, err)
		}
		for _, f := range records[spec.record] {
			if _, err = str(record, f); err != nil {
				return s, fmt.Errorf("a %s record: %w", spec.record, err)
			}
		}
		s.items = append(s.items, record)
	}
	return s, nil
}

// object decodes one JSON object. Trailing data after the object is an error.
func object(raw []byte) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	var m map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &m) != nil {
		return nil, errors.New("the value is not a JSON object")
	}
	return m, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// str checks one required key and returns its text. A null value gives "".
func str(m map[string]json.RawMessage, f field) (string, error) {
	raw, ok := m[f.name]
	if !ok {
		return "", fmt.Errorf("the key %s is missing", f.name)
	}
	if isNull(raw) {
		if f.null {
			return "", nil
		}
		return "", fmt.Errorf("the key %s is null", f.name)
	}
	if f.kind == kindInt {
		text := string(bytes.TrimSpace(raw))
		if n, err := strconv.ParseInt(text, 10, 64); err != nil || n < 0 || strconv.FormatInt(n, 10) != text {
			return "", fmt.Errorf("the key %s is not a nonnegative integer", f.name)
		}
		return text, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || (s == "" && !f.empty) {
		return "", fmt.Errorf("the key %s is not a nonempty string", f.name)
	}
	if f.kind == kindTime {
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil || !strings.HasSuffix(s, "Z") {
			return "", fmt.Errorf("the key %s is not a UTC RFC3339 time", f.name)
		}
	}
	if f.enum != nil && !slices.Contains(f.enum, s) {
		return "", fmt.Errorf("the key %s has the unknown value %q", f.name, s)
	}
	return s, nil
}

// render writes the terminal form: identity, observation time, then the nine sections.
func render(d *overview) string {
	var b strings.Builder
	observed := d.observedAt
	if observed == "" {
		observed = "unknown"
	}
	fmt.Fprintf(&b, "%s (%s)\nObserved: %s\n", d.app, d.env, observed)
	for _, s := range d.sections {
		b.WriteString(s.spec.caption)
		if s.state != "ok" {
			fmt.Fprintf(&b, " [%s: %s]", s.state, s.reason)
		}
		b.WriteByte('\n')
		if s.state == "disabled" || s.state == "unavailable" {
			continue
		}
		if len(s.items) == 0 {
			b.WriteString("  none\n")
			continue
		}
		fields := records[s.spec.record]
		if !s.spec.array {
			for _, f := range fields {
				fmt.Fprintf(&b, "  %s=%s\n", f.name, compact(s.items[0][f.name]))
			}
			continue
		}
		for _, item := range s.items {
			parts := make([]string, len(fields))
			for i, f := range fields {
				parts[i] = strconv.Quote(f.name) + ":" + compact(item[f.name])
			}
			b.WriteString("  {" + strings.Join(parts, ",") + "}\n")
		}
	}
	return b.String()
}

// compact writes a received JSON value on one line. It escapes DEL and the C1 controls,
// which JSON permits in strings but which a terminal can interpret.
func compact(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "null"
	}
	var out strings.Builder
	for _, r := range buf.String() {
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			fmt.Fprintf(&out, `\u%04x`, r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
