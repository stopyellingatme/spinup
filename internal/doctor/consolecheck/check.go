// Package consolecheck registers checks for console wiring and readiness.
package consolecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const (
	mainPath   = "api/cmd/console/main.go"
	dockerPath = "deploy/Dockerfile.api"
	basePath   = "console/compose.console.yml"
	queryPath  = "console/compose.query.yml"
)

func init() {
	doctor.Register(doctor.Check{ID: "console-wired", Title: "Console wiring", Scope: doctor.Local, Quick: true, Run: localCheck})
	doctor.Register(doctor.Check{ID: "console-ready", Title: "Console readiness", Scope: doctor.Remote, Run: remoteCheck})
}

func config(c *doctor.Context) (*project.Config, error) {
	if c == nil || c.Root == "" {
		return &project.Config{}, nil
	}
	return project.Load(c.Root)
}

func profile(cfg *project.Config) string {
	if !modules.Enabled(cfg, "monitoring") {
		return "absent"
	}
	return cfg.Monitoring.Profile
}

func active(cfg *project.Config) bool { return profile(cfg) == "lite" || profile(cfg) == "full" }

func localFailure(path string) doctor.Result {
	return doctor.Result{Status: doctor.Fail, Message: "Console wiring is unsafe or incomplete: " + path + ".", Try: "review the console pack and restore or merge " + path}
}

func localCheck(_ context.Context, c *doctor.Context) doctor.Result {
	cfg, err := config(c)
	if err != nil {
		return localFailure("spinup.toml")
	}
	if !modules.Enabled(cfg, "console") {
		return doctor.Result{Status: doctor.Skip, Message: "Console module is disabled."}
	}
	for _, step := range []struct {
		path  string
		valid func([]byte) bool
	}{
		{mainPath, validMain}, {dockerPath, validDockerfile},
		{basePath, func(b []byte) bool { return validCompose(b, cfg.App.Name, false, false) }},
		{queryPath, func(b []byte) bool { return validCompose(b, cfg.App.Name, true, !active(cfg)) }},
	} {
		b, err := os.ReadFile(filepath.Join(c.Root, step.path))
		if err != nil || !step.valid(b) {
			return localFailure(step.path)
		}
	}
	if path := isolationPath(c.Root, cfg.App.Name); path != "" {
		return localFailure(path)
	}
	return doctor.Result{Status: doctor.OK, Message: "Console wiring is present."}
}

// validMain checks executable syntax, not matching text in comments or dead code.
func validMain(b []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), mainPath, b, parser.SkipObjectResolution)
	if err != nil || f.Name.Name != "main" || len(f.Imports) != 2 || len(f.Decls) != 2 {
		return false
	}
	names := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return false
		}
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			return false
		}
		names[p] = name
	}
	osName, consoleName := names["os"], names["github.com/stopyellingatme/spinup/kit/x/console"]
	if osName == "" || consoleName == "" || osName == consoleName {
		return false
	}
	fn, ok := f.Decls[1].(*ast.FuncDecl)
	if !ok || fn.Name.Name != "main" || fn.Recv != nil || fn.Type.Params.NumFields() != 0 || fn.Type.Results.NumFields() != 0 || fn.Body == nil {
		return false
	}
	var buf bytes.Buffer
	if format.Node(&buf, token.NewFileSet(), fn.Body) != nil {
		return false
	}
	want := fmt.Sprintf("{\n\t%s.Exit(%s.Main(%s.Args[1:], %s.Stdout, %s.Stderr))\n}", osName, consoleName, osName, osName, osName)
	return buf.String() == want
}

// Dockerfile instructions are joined before matching. Comments cannot supply wiring.
func validDockerfile(b []byte) bool {
	var lines []string
	pending := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pending += line
		if strings.HasSuffix(pending, "\\") {
			pending = strings.TrimSuffix(pending, "\\") + " "
			continue
		}
		lines = append(lines, strings.Join(strings.Fields(pending), " "))
		pending = ""
	}
	stage := ""
	built, copied := false, false
	for _, line := range lines {
		fields := strings.Fields(line)
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			stage = ""
			if len(fields) >= 4 && strings.EqualFold(fields[len(fields)-2], "AS") {
				stage = fields[len(fields)-1]
			}
			copied = false
		case "RUN":
			if strings.Contains(line, "/out/console") {
				if stage != "build" || line != `RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/console ./cmd/console` {
					return false
				}
				built = true
			}
		case "COPY", "ADD":
			if strings.Contains(line, "/console") {
				if stage == "build" || !built || line != "COPY --from=build /out/console /console" {
					return false
				}
				copied = true
			}
		}
	}
	return pending == "" && built && copied
}

// KnownFields rejects unsafe additions, including host namespaces and credentials.
type compose struct {
	Name     string             `yaml:"name"`
	Services map[string]service `yaml:"services"`
	Networks map[string]network `yaml:"networks"`
}
type network struct {
	Name     string `yaml:"name"`
	Driver   string `yaml:"driver"`
	External bool   `yaml:"external"`
}
type service struct {
	Image       string            `yaml:"image"`
	Entrypoint  []string          `yaml:"entrypoint"`
	User        string            `yaml:"user"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	SecurityOpt []string          `yaml:"security_opt"`
	PIDs        int               `yaml:"pids_limit"`
	CPUs        string            `yaml:"cpus"`
	Memory      string            `yaml:"mem_limit"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Networks    []string          `yaml:"networks"`
	Profiles    []string          `yaml:"profiles"`
	Healthcheck struct {
		Test     []string `yaml:"test"`
		Interval string   `yaml:"interval"`
		Timeout  string   `yaml:"timeout"`
		Retries  int      `yaml:"retries"`
	} `yaml:"healthcheck"`
	Restart string `yaml:"restart"`
	Logging struct {
		Driver  string            `yaml:"driver"`
		Options map[string]string `yaml:"options"`
	} `yaml:"logging"`
}

// normalizeCompose accepts the short and long forms that Compose treats alike.
// It retains unknown keys for KnownFields to reject after YAML merges resolve.
func normalizeCompose(b []byte) ([]byte, error) {
	var doc map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return nil, errors.New("multiple Compose documents")
	}
	services, _ := doc["services"].(map[string]any)
	for _, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if nets, ok := svc["networks"].(map[string]any); ok {
			names := make([]string, 0, len(nets))
			for name, value := range nets {
				if value != nil {
					m, ok := value.(map[string]any)
					if !ok || len(m) != 0 {
						return nil, errors.New("custom network settings")
					}
				}
				names = append(names, name)
			}
			slices.Sort(names)
			svc["networks"] = names
		}
		if env, ok := svc["environment"].([]any); ok {
			values := map[string]string{}
			for _, raw := range env {
				v, ok := raw.(string)
				if !ok {
					return nil, errors.New("invalid environment")
				}
				key, value, ok := strings.Cut(v, "=")
				if !ok {
					return nil, errors.New("inherited environment")
				}
				if _, exists := values[key]; exists {
					return nil, errors.New("duplicate environment")
				}
				values[key] = value
			}
			svc["environment"] = values
		}
		for _, key := range []string{"ports", "volumes"} {
			entries, _ := svc[key].([]any)
			for i, raw := range entries {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				allowed := []string{"target", "published", "host_ip", "protocol", "mode"}
				if key == "volumes" {
					allowed = []string{"type", "source", "target", "read_only"}
				}
				for k := range m {
					if !slices.Contains(allowed, k) {
						return nil, errors.New("custom publication or mount settings")
					}
				}
				if key == "ports" {
					if mode, ok := m["mode"]; ok && mode != "ingress" {
						return nil, errors.New("nondefault port mode")
					}
					protocol := "tcp"
					if p, ok := m["protocol"].(string); ok {
						protocol = p
					}
					entries[i] = fmt.Sprint(m["host_ip"]) + ":" + fmt.Sprint(m["published"]) + ":" + fmt.Sprint(m["target"]) + "/" + protocol
				} else {
					if m["type"] != "bind" || m["read_only"] != true {
						return nil, errors.New("writable or non-bind mount")
					}
					entries[i] = fmt.Sprint(m["source"]) + ":" + fmt.Sprint(m["target"]) + ":ro"
				}
			}
		}
	}
	return yaml.Marshal(doc)
}

func validCompose(b []byte, app string, query, off bool) bool {
	var err error
	b, err = normalizeCompose(b)
	if err != nil {
		return false
	}
	var doc compose
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if dec.Decode(&doc) != nil {
		return false
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return false
	}
	if off {
		return doc.Name == "" && len(doc.Services) == 0 && len(doc.Networks) == 0
	}
	name, command, memory, heap := "console", "serve", "64m", "40MiB"
	nets := []string{"console"}
	health := []string{"CMD", "/console", "healthcheck"}
	expectedNets := map[string]network{"console": {Name: app + "-console", Driver: "bridge"}}
	if query {
		name, command, memory, heap = "console-query", "query", "48m", "28MiB"
		nets = append(nets, "monitoring")
		health = append(health, "query")
		expectedNets = map[string]network{"monitoring": {Name: app + "-monitoring", External: true}}
		if doc.Name != "" {
			return false
		}
	} else if doc.Name != app+"-console" {
		return false
	}
	if !reflect.DeepEqual(doc.Networks, expectedNets) || len(doc.Services) != 1 {
		return false
	}
	s, ok := doc.Services[name]
	slices.Sort(s.Networks)
	if !ok {
		return false
	}
	if s.Image != "${CONSOLE_IMAGE:?spinup deploy sets CONSOLE_IMAGE}" || !slices.Equal(s.Entrypoint, []string{"/console", command}) || !slices.Equal(s.Networks, nets) {
		return false
	}
	if query {
		if len(s.Ports) != 0 || len(s.Volumes) != 0 || !slices.Equal(s.Profiles, []string{"query"}) {
			return false
		}
	} else if !slices.Equal(s.Ports, []string{"127.0.0.1:7410:7410/tcp"}) || !slices.Equal(s.Volumes, []string{"/opt/" + app + "/console/data:/data:ro"}) || len(s.Profiles) != 0 {
		return false
	}
	env := map[string]string{"GOMEMLIMIT": heap}
	for _, key := range []string{"CONSOLE_APP", "CONSOLE_ENV", "CONSOLE_PROFILE"} {
		env[key] = "${" + key + ":?spinup deploy sets " + key + "}"
	}
	if !query {
		env["CONSOLE_ORIGIN"] = "${CONSOLE_ORIGIN:-}"
		for _, key := range []string{"CONSOLE_BACKEND", "CONSOLE_JOBS"} {
			env[key] = "${" + key + ":?spinup deploy sets " + key + "}"
		}
	}
	return reflect.DeepEqual(s.Environment, env) && s.User == "65532:65532" && s.ReadOnly && slices.Equal(s.CapDrop, []string{"ALL"}) && slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) && s.PIDs == 32 && s.CPUs == "0.25" && s.Memory == memory && s.Restart == "unless-stopped" && slices.Equal(s.Healthcheck.Test, health) && s.Healthcheck.Interval == "15s" && s.Healthcheck.Timeout == "3s" && s.Healthcheck.Retries == 3 && s.Logging.Driver == "json-file" && reflect.DeepEqual(s.Logging.Options, map[string]string{"max-size": "10m", "max-file": "3"})
}

var consoleRoute = regexp.MustCompile(`(?i)(\bconsole(?:-query)?\s*:\s*741[01]\b|:\s*741[01]\b)`)

// Inspect other rendered files too. A private service is unsafe behind a public proxy.
func isolationPath(root, app string) string {
	for _, dir := range []string{"deploy", "monitoring"} {
		bad := ""
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				bad = rel
				return fs.SkipAll
			}
			ext := filepath.Ext(path)
			if ext != ".yml" && ext != ".yaml" && ext != ".caddy" && d.Name() != "Caddyfile" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if ext == ".caddy" || d.Name() == "Caddyfile" {
				var lines []string
				for _, line := range strings.Split(string(b), "\n") {
					line, _, _ = strings.Cut(line, "#")
					lines = append(lines, line)
				}
				if consoleRoute.MatchString(strings.Join(lines, "\n")) {
					bad = rel
					return fs.SkipAll
				}
			} else {
				var doc struct {
					Networks map[string]struct {
						Name string `yaml:"name"`
					} `yaml:"networks"`
					Services map[string]struct {
						Networks    any    `yaml:"networks"`
						NetworkMode string `yaml:"network_mode"`
					} `yaml:"services"`
				}
				if yaml.Unmarshal(b, &doc) != nil {
					bad = rel
					return fs.SkipAll
				}
				for name, n := range doc.Networks {
					if name == "console" || n.Name == app+"-console" {
						bad = rel
						return fs.SkipAll
					}
				}
				for _, s := range doc.Services {
					if strings.Contains(s.NetworkMode, "console") || strings.Contains(fmt.Sprint(s.Networks), "console") {
						bad = rel
						return fs.SkipAll
					}
				}
			}
			return nil
		})
		if bad != "" {
			return bad
		}
		if err != nil && !os.IsNotExist(err) {
			return dir
		}
	}
	return ""
}

const inspectFormat = `{"running":{{.State.Running}},"ports":{{json .NetworkSettings.Ports}},"networks":{{json .NetworkSettings.Networks}}}`
const networkFormat = `{"driver":{{json .Driver}},"containers":{{json .Containers}}}`

func remoteCheck(ctx context.Context, c *doctor.Context) doctor.Result {
	cfg, err := config(c)
	fail := doctor.Result{Status: doctor.Fail, Message: "Console is unreachable, exposed, or incompatible."}
	if c != nil {
		fail.Try = "spinup deploy --resume --env " + c.Env
	}
	if err != nil {
		return fail
	}
	if !modules.Enabled(cfg, "console") {
		return doctor.Result{Status: doctor.Skip, Message: "Console module is disabled."}
	}
	r, ok := c.Deps["remote"].(remote.Runner)
	if !ok || r == nil {
		return fail
	}
	read := func(limit int, argv ...string) ([]byte, bool) {
		res, err := r.Run(ctx, remote.Cmd{Argv: argv, Timeout: 6 * time.Second})
		return res.Stdout, err == nil && res.ExitCode == 0 && len(res.Stdout) <= limit
	}
	web := cfg.App.Name + "-console-console-1"
	relay := cfg.App.Name + "-console-console-query-1"
	for _, query := range []bool{false, true} {
		if query && !active(cfg) {
			continue
		}
		name := web
		if query {
			name = relay
		}
		b, ok := read(64<<10, "docker", "inspect", "--format", inspectFormat, name)
		if !ok || !validRuntime(b, cfg.App.Name, query) {
			return fail
		}
	}
	b, ok := read(64<<10, "docker", "network", "inspect", "--format", networkFormat, cfg.App.Name+"-console")
	var net struct {
		Driver     string
		Containers map[string]struct{ Name string }
	}
	if !ok || json.Unmarshal(b, &net) != nil || net.Driver != "bridge" {
		return fail
	}
	members := map[string]bool{}
	for _, peer := range net.Containers {
		if peer.Name != web && (!active(cfg) || peer.Name != relay) {
			return fail
		}
		members[peer.Name] = true
	}
	if !members[web] || (active(cfg) && !members[relay]) {
		return fail
	}
	b, ok = read(16, "curl", "--fail", "--silent", "--show-error", "--noproxy", "*", "--max-time", "3", "--max-filesize", "16", "http://127.0.0.1:7410/healthz")
	if !ok || string(b) != "ok\n" {
		return fail
	}
	b, ok = read(512<<10, "curl", "--fail", "--silent", "--show-error", "--noproxy", "*", "--max-time", "6", "--max-filesize", "524288", "http://127.0.0.1:7410/api/v1/overview")
	if !ok {
		return fail
	}
	section, valid := validOverview(b, cfg.App.Name, c.Env, profile(cfg))
	if !valid {
		return fail
	}
	if section != "" {
		return doctor.Result{Status: doctor.Warn, Message: "Console source unavailable: " + section + ".", Try: "spinup status --env " + c.Env}
	}
	return doctor.Result{Status: doctor.OK, Message: "Console is ready."}
}

func validRuntime(b []byte, app string, query bool) bool {
	var state struct {
		Running bool
		Ports   map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
		Networks map[string]json.RawMessage
	}
	if json.Unmarshal(b, &state) != nil || !state.Running {
		return false
	}
	nets := []string{app + "-console"}
	if query {
		nets = append(nets, app+"-monitoring")
	}
	if len(state.Networks) != len(nets) {
		return false
	}
	for _, n := range nets {
		if _, ok := state.Networks[n]; !ok {
			return false
		}
	}
	bindings := 0
	for port, list := range state.Ports {
		for _, binding := range list {
			if query || port != "7410/tcp" || binding.HostIP != "127.0.0.1" || binding.HostPort != "7410" {
				return false
			}
			bindings++
		}
	}
	return (query && bindings == 0) || (!query && bindings == 1)
}

var sectionOrder = []string{"release", "containers", "alerts", "backup", "wal", "jobs", "disk", "memory", "history"}

// Each field is required. A suffix permits null, and the prefix selects its scalar type.
var recordFields = map[string]string{
	"release":    "id git_sha color created_at:t age_seconds:i previous_id?",
	"containers": "project service name state health?",
	"alerts":     "name severity? since:t?",
	"backup":     "backend label kind finished_at:t size_bytes:i?",
	"wal":        "state archived_at:t? failed_at:t? min_segment? max_segment?",
	"jobs":       "queue state count:i",
	"disk":       "total_bytes:i used_bytes:i available_bytes:i",
	"memory":     "total_bytes:i used_bytes:i available_bytes:i",
	"history":    "id at:t user? git_sha? color? result",
}

func object(b []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	err := json.Unmarshal(b, &m)
	return m, err == nil && m != nil
}
func null(b json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(b), []byte("null")) }
func text(m map[string]json.RawMessage, key string) (string, bool) {
	var s string
	b, ok := m[key]
	valid := ok && json.Unmarshal(b, &s) == nil && !null(b)
	return s, valid
}

func validOverview(b []byte, app, env, prof string) (string, bool) {
	if !utf8.Valid(b) {
		return "", false
	}
	doc, ok := object(b)
	if !ok {
		return "", false
	}
	if string(doc["version"]) != "1" {
		return "", false
	}
	for key, want := range map[string]string{"app": app, "env": env, "profile": prof} {
		s, ok := text(doc, key)
		if !ok || s != want {
			return "", false
		}
	}
	if !validField(doc, "observed_at:t?") {
		return "", false
	}
	warning := ""
	for _, key := range sectionOrder {
		s, ok := object(doc[key])
		if !ok {
			return "", false
		}
		state, ok := text(s, "state")
		if !ok {
			return "", false
		}
		reason, hasReason := text(s, "reason")
		rawReason, reasonPresent := s["reason"]
		data, dataPresent := s["data"]
		at, atPresent := s["at"]
		if !reasonPresent || !dataPresent || !atPresent || !validField(s, "at:t?") {
			return "", false
		}
		switch state {
		case "ok":
			if !null(rawReason) || null(at) {
				return "", false
			}
		case "disabled":
			if !hasReason || !slices.Contains(disabledReasons[key], reason) || !null(data) || !null(at) {
				return "", false
			}
			continue
		case "unavailable":
			if !hasReason || !slices.Contains([]string{"missing", "timeout", "invalid", "source_down"}, reason) || !null(data) {
				return "", false
			}
			if warning == "" {
				warning = key
			}
			continue
		case "stale":
			if reason != "sample_expired" || null(at) {
				return "", false
			}
			if warning == "" {
				warning = key
			}
		default:
			return "", false
		}
		if null(data) {
			if key != "release" && key != "backup" {
				return "", false
			}
			continue
		}
		rows := []json.RawMessage{data}
		if slices.Contains([]string{"containers", "alerts", "jobs", "history"}, key) {
			if len(data) == 0 || data[0] != '[' || json.Unmarshal(data, &rows) != nil {
				return "", false
			}
		}
		limit := 512
		if key == "alerts" {
			limit = 256
		}
		if key == "history" {
			limit = 20
		}
		if len(rows) > limit {
			return "", false
		}
		for _, row := range rows {
			record, ok := object(row)
			if !ok || !validRecord(key, record) {
				return "", false
			}
			for _, f := range strings.Fields(recordFields[key]) {
				if !validField(record, f) {
					return "", false
				}
			}
		}
	}
	return warning, true
}

var disabledReasons = map[string][]string{
	"alerts": {"no_monitoring", "profile_none"}, "backup": {"no_backups"},
	"wal": {"no_backups", "no_wal"}, "jobs": {"no_jobs"},
}

func validRecord(section string, record map[string]json.RawMessage) bool {
	enums := map[string][]string{}
	switch section {
	case "release":
		enums["color"] = []string{"blue", "green"}
	case "containers":
		enums["state"] = []string{"created", "running", "paused", "restarting", "removing", "exited", "dead"}
		enums["health"] = []string{"none", "starting", "healthy", "unhealthy"}
	case "backup":
		enums["backend"] = []string{"pgbackrest", "pgdump", "walg"}
	case "wal":
		enums["state"] = []string{"off", "failing", "waiting", "archiving"}
	case "jobs":
		enums["state"] = []string{"available", "cancelled", "completed", "discarded", "pending", "retryable", "running", "scheduled"}
	case "history":
		enums["result"] = []string{"failed", "succeeded", "incomplete", "recorded"}
		enums["color"] = []string{"blue", "green"}
	}
	for field, allowed := range enums {
		if null(record[field]) && (field == "health" || (section == "history" && field == "color")) {
			continue
		}
		v, ok := text(record, field)
		if !ok || !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

func validField(m map[string]json.RawMessage, spec string) bool {
	nullable := strings.HasSuffix(spec, "?")
	spec = strings.TrimSuffix(spec, "?")
	key, kind, _ := strings.Cut(spec, ":")
	raw, ok := m[key]
	if !ok {
		return false
	}
	if null(raw) {
		return nullable
	}
	if kind == "i" {
		var n int64
		return json.Unmarshal(raw, &n) == nil && n >= 0
	}
	s, ok := text(m, key)
	if !ok || s == "" {
		return false
	}
	if kind == "t" {
		_, err := time.Parse(time.RFC3339Nano, s)
		return err == nil && strings.HasSuffix(s, "Z")
	}
	return true
}
