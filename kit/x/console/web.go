package console

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/overview.html assets/logs.html assets/console.css
var webAssets embed.FS

const webCSP = "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

var errWebRelayBusy = errors.New("console relay query limit is busy")

type webRelayTransport struct{ http.RoundTripper }

func (t webRelayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(r)
	if err == nil && response != nil && response.StatusCode == http.StatusTooManyRequests {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errWebRelayBusy
	}
	return response, err
}

var webTemplates = template.Must(template.ParseFS(webAssets, "assets/*.html"))

func webHandler(cfg config, facts fs.FS, client *http.Client, now clock) http.Handler {
	client = queryClient(client)
	client.Transport = webRelayTransport{client.Transport}
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", webCSP)
		fail := func(code string) { webError(w, r, code) }
		if !webOrigin(r) {
			fail("forbidden")
			return
		}
		target := r.RequestURI
		if target == "" {
			target = r.URL.RequestURI()
		}
		if len(target) > 8<<10 {
			fail("query_too_long")
			return
		}
		if !slices.Contains([]string{"/", "/logs", "/assets/console.css", "/healthz", "/api/v1/overview", "/api/v1/logs"}, r.URL.Path) {
			fail("not_found")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			fail("method_not_allowed")
			return
		}
		for _, header := range []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
			if _, present := r.Header[http.CanonicalHeaderKey(header)]; present {
				fail("invalid_filter")
				return
			}
		}
		filter, port, err := webFilters(r)
		if err != nil {
			fail("invalid_filter")
			return
		}
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "ok\n")
			return
		case "/assets/console.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			b, _ := webAssets.ReadFile("assets/console.css")
			_, _ = w.Write(b)
			return
		}
		active := cfg.Profile == "lite" || cfg.Profile == "full"
		isLogs := r.URL.Path == "/logs" || r.URL.Path == "/api/v1/logs"
		if isLogs && !active {
			fail("monitoring_disabled")
			return
		}
		// Process health never waits for a source or a query slot.
		if active {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				fail("busy")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		at := now().UTC()
		view := webPage{App: cfg.App, Env: cfg.Env, Port: port, Filter: filter, OverviewURL: webPageURL("/", port), LogsURL: webPageURL("/logs", port), GrafanaCommand: "spinup monitor open --env " + cfg.Env}
		view.LogCommand = "spinup monitor logs --env " + cfg.Env + " --since " + filter.Since + " --query " + webQuote(filter.logQL())
		if isLogs {
			result, code := webLogs(ctx, client, filter, at)
			if code != "" {
				fail(code)
				return
			}
			if r.URL.Path == "/api/v1/logs" {
				queryWriteJSON(w, http.StatusOK, result)
				return
			}
			view.Logs = &result
			view.LogCommand = "spinup monitor logs --env " + cfg.Env + " --since " + filter.Since + " --query " + webQuote(result.Query)
			if port != "" {
				view.ExploreURL = "http://127.0.0.1:" + port + result.ExplorePath
			}
			webRender(w, "logs.html", view)
			return
		}
		result := webSnapshot(facts, cfg, at)
		if active {
			result.Alerts = webAlerts(ctx, client, at)
		}
		if r.URL.Path == "/api/v1/overview" {
			queryWriteJSON(w, http.StatusOK, result)
			return
		}
		view.Observed = webTime(result.ObservedAt)
		view.Tiles = webTiles(result)
		view.AppURL = cfg.Origin
		view.BackupCommand = cfg.Backend != "none"
		view.RollbackCommand = result.Release.State == "ok" && result.Release.Data != nil && result.Release.Data.PreviousID != nil
		if active && port != "" {
			view.Dashboards = webDashboards(cfg.Profile, port)
		}
		webRender(w, "overview.html", view)
	})
}

func webOrigin(r *http.Request) bool {
	parts := strings.Split(r.Host, ":")
	if len(parts) > 2 || (parts[0] != "127.0.0.1" && parts[0] != "localhost") {
		return false
	}
	if len(parts) == 2 {
		if !webDigits(parts[1]) {
			return false
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	origins, present := r.Header["Origin"]
	if !present {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	u, err := url.Parse(origins[0])
	return err == nil && u.Scheme == "http" && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(origins[0], "#")
}

func webDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func webFilters(r *http.Request) (logFilter, string, error) {
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return logFilter{}, "", err
	}
	port := ""
	if r.URL.Path == "/" || r.URL.Path == "/logs" {
		if values, ok := v["grafana_port"]; ok {
			if len(values) != 1 || !webDigits(values[0]) {
				return logFilter{}, "", errQueryData
			}
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1024 || n > 65535 {
				return logFilter{}, "", errQueryData
			}
			port = strconv.Itoa(n)
			delete(v, "grafana_port")
		}
	}
	if r.URL.Path != "/logs" && r.URL.Path != "/api/v1/logs" && len(v) != 0 {
		return logFilter{}, "", errQueryData
	}
	f, err := parseLogFilter(v)
	return f, port, err
}

func webError(w http.ResponseWriter, r *http.Request, code string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		queryWriteError(w, code)
		return
	}
	item := errorCatalog[code]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(item.Status)
	_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Console</title><h1>"+template.HTMLEscapeString(item.Message)+"</h1><p>Try: "+template.HTMLEscapeString(item.Try)+"</p></html>")
}

// webDecode requires the v1 keys while permitting additive fields. Ordinary
// unmarshalling alone accepts missing keys and null for non-pointer scalars.
func webDecode(data []byte, dst any) bool {
	return json.Unmarshal(data, dst) == nil && webShape(data, reflect.TypeOf(dst).Elem())
}

func webShape(raw json.RawMessage, typ reflect.Type) bool {
	if len(raw) == 0 {
		return false
	}
	if typ.Kind() == reflect.Pointer {
		return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || webShape(raw, typ.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if typ == reflect.TypeFor[time.Time]() {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !webShape(fields[f.Tag.Get("json")], f.Type) {
				return false
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, v := range values {
			if !webShape(v, typ.Elem()) {
				return false
			}
		}
	}
	return true
}

func webSnapshot(facts fs.FS, cfg config, now time.Time) overview {
	data, reason := readInput(facts, "facts.json")
	var v overview
	if reason == "" && (len(data) > hostLimit || !webDecode(data, &v) || v.Version != 1 || v.App != cfg.App || v.Env != cfg.Env || v.Profile != cfg.Profile || v.ObservedAt == nil || !validTime(*v.ObservedAt, now)) {
		reason = "invalid"
	}
	if reason != "" {
		v = overview{Version: 1, App: cfg.App, Env: cfg.Env, Profile: cfg.Profile}
		v.Release = section[release]{State: "unavailable", Reason: &reason}
		v.Containers = section[[]container]{State: "unavailable", Reason: &reason}
		v.Backup = section[backup]{State: "unavailable", Reason: &reason}
		v.WAL = section[wal]{State: "unavailable", Reason: &reason}
		v.Jobs = section[[]queue]{State: "unavailable", Reason: &reason}
		v.Disk = section[space]{State: "unavailable", Reason: &reason}
		v.Memory = section[space]{State: "unavailable", Reason: &reason}
		v.History = section[[]attempt]{State: "unavailable", Reason: &reason}
	} else {
		webClean(reflect.ValueOf(&v).Elem())
		at := v.ObservedAt.UTC()
		v.ObservedAt = &at
		v.Release = webHostSection(v.Release, at, now, true, func(r release) bool {
			return factReleaseID.MatchString(r.ID) && factSHA.MatchString(r.GitSHA) && strings.HasPrefix(r.GitSHA, r.ID[17:29]) && slices.Contains([]string{"blue", "green"}, r.Color) && validTime(r.CreatedAt, at) && r.AgeSeconds >= 0 && (r.PreviousID == nil || factReleaseID.MatchString(*r.PreviousID) && *r.PreviousID != r.ID)
		})
		v.Containers = webHostSection(v.Containers, at, now, false, func(rows []container) bool {
			if rows == nil || len(rows) > 512 {
				return false
			}
			for _, row := range rows {
				if !slices.Contains([]string{cfg.App, cfg.App + "-blue", cfg.App + "-green", cfg.App + "-monitoring", cfg.App + "-console"}, row.Project) || row.Service == "" || row.Name == "" || row.State == "" {
					return false
				}
			}
			return true
		})
		v.Backup = webHostSection(v.Backup, at, now, true, func(b backup) bool {
			return b.Backend == webBackend(cfg) && b.Label != "" && slices.Contains([]string{"full", "diff", "incr"}, b.Kind) && validTime(b.FinishedAt, at) && (b.SizeBytes == nil || *b.SizeBytes >= 0)
		})
		v.WAL = webHostSection(v.WAL, at, now, false, func(w wal) bool {
			return slices.Contains([]string{"off", "failing", "waiting", "archiving"}, w.State) && (w.ArchivedAt == nil || validTime(*w.ArchivedAt, at)) && (w.FailedAt == nil || validTime(*w.FailedAt, at)) && (w.MinSegment == nil || factSegment.MatchString(*w.MinSegment)) && (w.MaxSegment == nil || factSegment.MatchString(*w.MaxSegment))
		})
		v.Jobs = webHostSection(v.Jobs, at, now, false, func(rows []queue) bool {
			if rows == nil || len(rows) > 512 {
				return false
			}
			for _, q := range rows {
				if q.Queue == "" || len(q.Queue) > 128 || q.Count <= 0 || !slices.Contains([]string{"available", "cancelled", "completed", "discarded", "pending", "retryable", "running", "scheduled"}, q.State) {
					return false
				}
			}
			return true
		})
		v.Disk = webHostSection(v.Disk, at, now, false, webSpace)
		v.Memory = webHostSection(v.Memory, at, now, false, func(s space) bool { return webSpace(s) && s.AvailableBytes == s.TotalBytes-s.UsedBytes })
		v.History = webHostSection(v.History, at, now, false, func(rows []attempt) bool {
			if rows == nil || len(rows) > 20 {
				return false
			}
			for _, a := range rows {
				if !factReleaseID.MatchString(a.ID) || !validTime(a.At, at) || (a.User != nil && len(*a.User) > 128) || (a.GitSHA != nil && !factSHA.MatchString(*a.GitSHA)) || (a.Color != nil && !slices.Contains([]string{"blue", "green"}, *a.Color)) || !slices.Contains([]string{"recorded", "incomplete", "failed", "succeeded"}, a.Result) {
					return false
				}
			}
			return true
		})
	}
	if !cfg.Jobs {
		v.Jobs = disabledSection[[]queue]("no_jobs")
	}
	switch cfg.Backend {
	case "none":
		v.Backup = disabledSection[backup]("no_backups")
		v.WAL = disabledSection[wal]("no_backups")
	case "pgdump":
		v.WAL = disabledSection[wal]("no_wal")
	}
	v.Alerts = disabledSection[[]alert]("no_monitoring")
	if cfg.Profile == "none" {
		v.Alerts = disabledSection[[]alert]("profile_none")
	}
	if v.ObservedAt != nil {
		boundHost(&v, *v.ObservedAt)
	}
	return v
}

func webBackend(cfg config) string {
	if cfg.Backend == "" {
		return "pgbackrest"
	}
	return cfg.Backend
}

func webSpace(s space) bool {
	return s.TotalBytes >= 0 && s.UsedBytes >= 0 && s.AvailableBytes >= 0 && s.UsedBytes <= s.TotalBytes && s.AvailableBytes <= s.TotalBytes-s.UsedBytes
}

func webHostSection[T any](s section[T], at, now time.Time, nullable bool, valid func(T) bool) section[T] {
	invalid := failedSection[T]("invalid", at)
	if s.At == nil || !s.At.Equal(at) {
		return invalid
	}
	if s.State == "unavailable" {
		if s.Data == nil && slices.Contains([]string{"missing", "timeout", "invalid", "source_down"}, queryString(s.Reason)) {
			return s
		}
		return invalid
	}
	expired := now.Sub(at) > 90*time.Second
	if s.State != "ok" && s.State != "stale" || s.State == "ok" && s.Reason != nil || s.State == "stale" && (!expired || queryString(s.Reason) != "sample_expired") || s.Data == nil && !nullable || s.Data != nil && !valid(*s.Data) {
		return invalid
	}
	if expired {
		reason := "sample_expired"
		s.State = "stale"
		s.Reason = &reason
	}
	utc := s.At.UTC()
	s.At = &utc
	return s
}

func webClean(v reflect.Value) {
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(v.Interface().(time.Time).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			webClean(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			webClean(v.Field(i))
		}
	case reflect.Slice:
		for i := range v.Len() {
			webClean(v.Index(i))
		}
	case reflect.String:
		v.SetString(cleanFact(v.String()))
	}
}

func webAlerts(ctx context.Context, client *http.Client, at time.Time) section[[]alert] {
	var raw json.RawMessage
	err := queryRead(ctx, client, "http://console-query:7411/v1/alerts", &raw)
	if err != nil {
		reason := "source_down"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		if errors.Is(err, errQueryData) {
			reason = "invalid"
		}
		return failedSection[[]alert](reason, at)
	}
	var s section[[]alert]
	if len(raw) > 128<<10 || !webDecode(raw, &s) || s.At == nil || !validTime(*s.At, at) || at.Sub(*s.At) > 90*time.Second {
		return failedSection[[]alert]("invalid", at)
	}
	webClean(reflect.ValueOf(&s).Elem())
	encoded, _ := json.Marshal(s)
	if len(encoded) > 128<<10 {
		return failedSection[[]alert]("invalid", at)
	}
	return webHostSection(s, *s.At, at, false, func(rows []alert) bool {
		if rows == nil || len(rows) > 256 {
			return false
		}
		for _, a := range rows {
			if a.Name == "" || a.Since != nil && !validTime(*a.Since, at) {
				return false
			}
		}
		return true
	})
}

func webLogs(ctx context.Context, client *http.Client, f logFilter, at time.Time) (logs, string) {
	values := url.Values{"service": {f.Service}, "level": {f.Level}, "since": {f.Since}, "text": {f.Text}}
	var raw json.RawMessage
	if err := queryRead(ctx, client, "http://console-query:7411/v1/logs?"+values.Encode(), &raw); err != nil {
		if errors.Is(err, errWebRelayBusy) {
			return logs{}, "busy"
		}
		return logs{}, "source_down"
	}
	var result logs
	if !webDecode(raw, &result) || result.Version != 1 || result.Query != f.logQL() || !validTime(result.End, at) || !validTime(result.Start, at) || result.Start.After(result.End) || result.Entries == nil || len(result.Entries) > 100 {
		return logs{}, "source_down"
	}
	duration, _ := time.ParseDuration(f.Since)
	if !result.Start.Equal(result.End.Add(-duration)) || at.Sub(result.End) > 5*time.Second {
		return logs{}, "source_down"
	}
	for i := range result.Entries {
		row := &result.Entries[i]
		if row.At.Before(result.Start) || row.At.After(result.End) {
			return logs{}, "source_down"
		}
		row.Service = queryClean(row.Service)
		if row.Level != nil {
			level := queryClean(*row.Level)
			row.Level = &level
		}
		row.Line = queryRedact(row.Line)
	}
	// Rebuild navigation from typed data, never a relay-supplied URL.
	result.ExplorePath = queryExplore(result.Query, result.Start, result.End)
	return result, ""
}

type webField struct{ Name, Value string }
type webTile struct {
	Caption, State, Reason, At, Command, OverviewCommand, Docs string
	Rows                                                       [][]webField
}
type webLink struct{ Caption, URL string }
type webPage struct {
	App, Env, Port, Observed, AppURL, OverviewURL, LogsURL, GrafanaCommand string
	Filter                                                                 logFilter
	Tiles                                                                  []webTile
	Dashboards                                                             []webLink
	BackupCommand, RollbackCommand                                         bool
	Logs                                                                   *logs
	LogCommand, ExploreURL                                                 string
}

func webTiles(v overview) []webTile {
	result := []webTile{}
	add := func(caption, command, docs string, value any) {
		raw, _ := json.Marshal(value)
		var s struct {
			State  string
			Reason *string
			At     *time.Time
			Data   json.RawMessage
		}
		_ = json.Unmarshal(raw, &s)
		tile := webTile{Caption: caption, State: s.State, Reason: queryString(s.Reason), At: webTime(s.At), Command: command + " --env " + v.Env, OverviewCommand: "spinup status --env " + v.Env, Docs: "https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/" + docs}
		if s.State == "ok" || s.State == "stale" {
			tile.Rows = webRows(s.Data)
		}
		result = append(result, tile)
	}
	add("Live release", "spinup deploy status", "commands/deploy.md", v.Release)
	add("Containers", "spinup status", "commands/status.md", v.Containers)
	add("Firing alerts", "spinup monitor alerts", "monitoring.md", v.Alerts)
	add("Last backup", "spinup backup status", "commands/backup.md", v.Backup)
	add("WAL archive", "spinup status", "commands/backup.md", v.WAL)
	add("Job queues", "spinup status", "build/jobs.md", v.Jobs)
	add("Disk", "spinup status", "commands/status.md", v.Disk)
	add("Memory", "spinup status", "commands/status.md", v.Memory)
	add("Deploy history", "spinup releases list", "commands/releases.md", v.History)
	return result
}

func webRows(raw json.RawMessage) [][]webField {
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	objects := []json.RawMessage{raw}
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &objects)
	}
	rows := [][]webField{}
	for _, obj := range objects {
		// Tokens preserve schema order in each field list.
		d := json.NewDecoder(bytes.NewReader(obj))
		_, _ = d.Token()
		fields := []webField{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				break
			}
			var value json.RawMessage
			if d.Decode(&value) != nil {
				break
			}
			name := key.(string)
			if strings.HasSuffix(name, "_bytes") {
				name += " (bytes)"
			}
			if strings.HasSuffix(name, "_seconds") {
				name += " (seconds)"
			}
			fields = append(fields, webField{name, string(value)})
		}
		rows = append(rows, fields)
	}
	return rows
}

func webDashboards(profile, port string) []webLink {
	links := []webLink{{"HTTP RED", "spinup-http-red"}, {"Go runtime", "spinup-go-runtime"}, {"Caddy", "spinup-caddy"}, {"Backups", "spinup-backups"}, {"Deploys", "spinup-deploys"}}
	if profile == "full" {
		links = append(links, webLink{"Host", "rYdddlPWk"}, webLink{"Containers", "spinup-containers"}, webLink{"PostgreSQL", "000000039"})
	}
	for i := range links {
		links[i].URL = "http://127.0.0.1:" + port + "/d/" + links[i].URL
	}
	return links
}
func webTime(at *time.Time) string {
	if at == nil {
		return "unknown"
	}
	return at.UTC().Format(time.RFC3339Nano)
}
func webPageURL(path, port string) string {
	if port == "" {
		return path
	}
	return path + "?grafana_port=" + port
}
func webQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func webRender(w http.ResponseWriter, name string, view webPage) {
	var b bytes.Buffer
	if err := webTemplates.ExecuteTemplate(&b, name, view); err != nil {
		queryWriteError(w, "source_down")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}
