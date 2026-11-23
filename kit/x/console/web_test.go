package console

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	texttemplate "text/template"
	"time"
)

func webFixture(t *testing.T, cfg config) (overview, fs.FS) {
	t.Helper()
	probes := baseProbes()
	probes["release.json"].Data = []byte(`{"release":"` + currentID + `","app":"todo","env":"production","git_sha":"` + currentSHA + `","color":"blue","created_at":"2026-11-22T17:00:00Z"}`)
	probes["upstream.caddy"].Data = []byte("name {args[0]}-blue\n")
	probes["releases.json"].Data = []byte(`[{"release":"` + currentID + `","created_at":"2026-11-22T17:00:00Z"},{"release":"` + olderID + `","created_at":"2026-11-21T17:00:00Z"}]`)
	probes["jobs.json"].Data = []byte(`[{"queue":"default","state":"running","count":3}]`)
	v := runProject(t, probes, cfg)
	return v, webFacts(t, v)
}

func webFacts(t *testing.T, v overview) fs.FS {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "facts.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(dir)
}

func webConfig() config {
	return config{App: "todo", Env: "production", Profile: "lite", Backend: "pgdump", Jobs: true, Origin: "https://todo.example"}
}

func fakeRelay(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Host != "console-query:7411" || len(r.Header) != 0 {
			t.Errorf("unsafe relay request: %s %s %v", r.Method, r.URL, r.Header)
		}
		if r.URL.Path == "/v1/alerts" {
			return queryResponse(`{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[{"name":"Heartbeat","severity":null,"since":null}]}`), nil
		}
		if r.URL.Path != "/v1/logs" || strings.Contains(r.URL.RawQuery, "grafana_port") {
			t.Errorf("unexpected relay URL: %s", r.URL)
		}
		f, err := parseLogFilter(r.URL.Query())
		if err != nil {
			t.Fatal(err)
		}
		result := logs{Version: 1, Start: projectTime.Add(-time.Hour), End: projectTime, Query: f.logQL(), Entries: []line{{At: projectTime, Service: "api", Line: `<script>alert('log')</script>`}}}
		result.ExplorePath = queryExplore(result.Query, result.Start, result.End)
		b, _ := json.Marshal(result)
		return queryResponse(string(b)), nil
	})}
}

func webGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return queryRequest(t, h, "http://127.0.0.1:17741"+target)
}

type countedFacts struct {
	fs.FS
	opens atomic.Int32
}

func (f *countedFacts) Open(name string) (fs.File, error) {
	if name == "facts.json" {
		f.opens.Add(1)
	}
	return f.FS.Open(name)
}

func TestWebParity(t *testing.T) {
	cfg := webConfig()
	want, facts := webFixture(t, cfg)
	counted := &countedFacts{FS: facts}
	h := webHandler(cfg, counted, fakeRelay(t), func() time.Time { return projectTime })
	w := webGet(t, h, "/api/v1/overview")
	var got overview
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("overview: %d %s: %v", w.Code, w.Body, err)
	}
	if w.Code != 200 || counted.opens.Load() != 1 {
		t.Fatalf("snapshot must be read once: %d reads=%d", w.Code, counted.opens.Load())
	}
	want.Alerts = got.Alerts
	if !reflect.DeepEqual(got, want) || got.Alerts.Data == nil || (*got.Alerts.Data)[0].Name != "Heartbeat" {
		t.Fatalf("projection changed: %s", w.Body)
	}
	w = webGet(t, h, "/")
	text := html.UnescapeString(w.Body.String())
	for _, expected := range []string{"Live release", "Containers", "Firing alerts", "Last backup", "WAL archive", "Job queues", "Disk", "Memory", "Deploy history", currentID, currentSHA, "Heartbeat", "default", "30720", "bytes", "seconds", "2026-11-22T18:00:00Z", "spinup status --env production", "spinup deploy status --env production"} {
		if w.Code != 200 || !strings.Contains(text, expected) {
			t.Errorf("page missing %q: %d %s", expected, w.Code, text)
		}
	}
	if counted.opens.Load() != 2 {
		t.Fatal("HTML reread the snapshot")
	}
}

func TestWebStates(t *testing.T) {
	cfg := webConfig()
	v, _ := webFixture(t, cfg)
	for _, test := range []struct {
		name          string
		mutate        func(*overview)
		now           time.Time
		state, reason string
	}{
		{"stale", func(*overview) {}, projectTime.Add(91 * time.Second), "stale", "sample_expired"},
		{"boundary", func(*overview) {}, projectTime.Add(90 * time.Second), "ok", ""},
		{"app", func(v *overview) { v.App = "another" }, projectTime, "unavailable", "invalid"},
		{"env", func(v *overview) { v.Env = "staging" }, projectTime, "unavailable", "invalid"},
		{"profile", func(v *overview) { v.Profile = "full" }, projectTime, "unavailable", "invalid"},
		{"future", func(*overview) {}, projectTime.Add(-6 * time.Second), "unavailable", "invalid"},
		{"section-time", func(v *overview) { at := projectTime.Add(time.Second); v.Disk.At = &at }, projectTime, "unavailable", "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := v
			test.mutate(&value)
			h := webHandler(cfg, webFacts(t, value), fakeRelay(t), func() time.Time { return test.now })
			w := webGet(t, h, "/api/v1/overview")
			var got overview
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Disk.State != test.state || queryString(got.Disk.Reason) != test.reason {
				t.Fatalf("state: %s", w.Body)
			}
			if test.state == "stale" && (got.Disk.Data == nil || !got.Disk.At.Equal(projectTime)) {
				t.Fatal("stale facts were erased")
			}
			if test.state == "unavailable" && got.Disk.Data != nil {
				t.Fatal("invalid facts survived")
			}
		})
	}
	for _, profile := range []string{"absent", "none", "lite", "full"} {
		cfg := config{App: "todo", Env: "production", Profile: profile, Backend: "none"}
		h := webHandler(cfg, fstest.MapFS{}, fakeRelay(t), func() time.Time { return projectTime })
		w := webGet(t, h, "/api/v1/overview")
		var got overview
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Release.State != "unavailable" || queryString(got.Release.Reason) != "missing" || got.Release.At != nil || got.ObservedAt != nil || queryString(got.Jobs.Reason) != "no_jobs" || queryString(got.Backup.Reason) != "no_backups" || queryString(got.WAL.Reason) != "no_backups" {
			t.Fatalf("missing selection: %s", w.Body)
		}
		if (profile == "absent" || profile == "none") && got.Alerts.State != "disabled" {
			t.Fatal("monitoring selection lost")
		}
	}
	broken := &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	h := webHandler(cfg, webFacts(t, v), broken, func() time.Time { return projectTime })
	w := webGet(t, h, "/api/v1/overview")
	var got overview
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Alerts.State != "unavailable" || queryString(got.Alerts.Reason) != "timeout" || got.Disk.State != "ok" {
		t.Fatalf("relay failure: %s", w.Body)
	}
	w = webGet(t, h, "/healthz")
	if w.Code != 200 || w.Body.String() != "ok\n" {
		t.Fatal("health depends on sources")
	}
}

func TestWebIsolation(t *testing.T) {
	h := webHandler(webConfig(), fstest.MapFS{}, fakeRelay(t), func() time.Time { return projectTime })
	for _, path := range []string{"/", "/logs", "/assets/console.css", "/healthz", "/api/v1/overview", "/api/v1/logs", "/unknown"} {
		for _, test := range []struct {
			name, host, origin, method string
			status                     int
		}{
			{"host", "evil.test", "", "GET", 403},
			{"host-suffix", "localhost.evil:1234", "", "GET", 403},
			{"host-port", "localhost:x", "", "GET", 403},
			{"origin", "localhost:1234", "https://evil.test", "GET", 403},
			{"null", "localhost:1234", "null", "GET", 403},
			{"other-port", "localhost:1234", "http://localhost:4321", "GET", 403},
			{"write", "localhost:1234", "", "POST", 405},
		} {
			r := httptest.NewRequestWithContext(t.Context(), test.method, "http://localhost:1234"+path, nil)
			r.Host = test.host
			if test.origin != "" {
				r.Header.Set("Origin", test.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := test.status
			if path == "/unknown" && test.method == "POST" {
				want = 404
			}
			if w.Code != want {
				t.Errorf("%s %s: %d %s", path, test.name, w.Code, w.Body)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("headers: %v", w.Header())
			}
			if want == 405 && w.Header().Get("Allow") != "GET" {
				t.Fatal("missing Allow")
			}
		}
	}
	for _, target := range []string{"/api/v1/logs?query=evil", "/logs?text=a&text=b", "/?grafana_port=80", "/?grafana_port=2000&grafana_port=3000", "/api/v1/overview?grafana_port=3000", "/healthz?x=1", "/logs?text=%zz", "/logs?text=%00"} {
		w := webGet(t, h, target)
		if w.Code != 400 {
			t.Errorf("filter %s: %d", target, w.Code)
		}
	}
	if w := webGet(t, h, "/logs?text="+strings.Repeat("a", 8192)); w.Code != 414 {
		t.Fatal("target limit missing")
	}
	r := httptest.NewRequestWithContext(t.Context(), "GET", "http://localhost:1234/healthz", nil)
	r.Header.Set("Origin", "http://localhost:1234")
	r.Header.Set("X-Forwarded-Host", "evil.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("valid origin failed")
	}
}

func TestWebLinks(t *testing.T) {
	for _, profile := range []string{"absent", "none", "lite", "full"} {
		cfg := webConfig()
		cfg.Profile = profile
		_, facts := webFixture(t, cfg)
		h := webHandler(cfg, facts, fakeRelay(t), func() time.Time { return projectTime })
		w := webGet(t, h, "/?grafana_port=18765")
		body := html.UnescapeString(w.Body.String())
		if !strings.Contains(body, `href="https://todo.example"`) || !strings.Contains(body, `/logs?grafana_port=18765`) || !strings.Contains(body, "https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/") {
			t.Fatalf("links: %s", body)
		}
		if !strings.Contains(body, "spinup monitor open --env production") || !strings.Contains(body, "spinup monitor logs --env production --since 1h --query") {
			t.Error("overview tool commands are missing")
		}
		paths := []string{"spinup-http-red", "spinup-go-runtime", "spinup-caddy", "spinup-backups", "spinup-deploys", "rYdddlPWk", "pMEd7m0Mz", "000000039"}
		for i, uid := range paths {
			want := (profile == "lite" && i < 5) || profile == "full"
			if strings.Contains(body, "http://127.0.0.1:18765/d/"+uid) != want {
				t.Errorf("%s dashboard %s", profile, uid)
			}
		}
		if profile != "lite" && profile != "full" {
			continue
		}
		w = webGet(t, h, "/")
		if !strings.Contains(w.Body.String(), "spinup monitor open --env production") || strings.Contains(w.Body.String(), "http://127.0.0.1:") {
			t.Fatal("dead Grafana link")
		}
		w = webGet(t, h, "/logs?grafana_port=18765&text="+url.QueryEscape("it's <script>"))
		body = html.UnescapeString(w.Body.String())
		for _, expected := range []string{`name="grafana_port" value="18765"`, `href="/?grafana_port=18765"`, "http://127.0.0.1:18765/explore?schemaVersion=1", "spinup monitor logs --env production --since 1h --query", "'\"'\"'"} {
			if w.Code != 200 || !strings.Contains(body, expected) {
				t.Errorf("logs missing %q: %d %s", expected, w.Code, body)
			}
		}
		if strings.Contains(w.Body.String(), "<script>") {
			t.Fatal("HTML injection in logs or filter")
		}
	}
	// Check the top-level UIDs, including all vendor dashboards, against shipped files.
	files, err := filepath.Glob("../../../internal/templates/packs/monitoring/files/monitoring/grafana/dashboards/stack/*.json*")
	if err != nil {
		t.Fatal(err)
	}
	vendor, err := filepath.Glob("../../../internal/templates/packs/monitoring/files/monitoring/grafana/dashboards/stack/vendor/*.json")
	if err != nil {
		t.Fatal(err)
	}
	uids := map[string]bool{}
	for _, path := range append(files, vendor...) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, ".tmpl") {
			tmpl, err := texttemplate.New(path).Delims("[[", "]]").Parse(string(b))
			if err != nil {
				t.Fatal(err)
			}
			var rendered bytes.Buffer
			if err := tmpl.Execute(&rendered, map[string]any{"monitoring_profile": "full"}); err != nil {
				t.Fatal(err)
			}
			b = rendered.Bytes()
		}
		var doc struct {
			UID string `json:"uid"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		uids[doc.UID] = true
	}
	for _, uid := range []string{"spinup-http-red", "spinup-go-runtime", "spinup-caddy", "spinup-backups", "spinup-deploys", "rYdddlPWk", "pMEd7m0Mz", "000000039"} {
		if !uids[uid] {
			t.Errorf("dashboard %s is not shipped", uid)
		}
	}
}

func TestWebActions(t *testing.T) {
	cfg := webConfig()
	v, _ := webFixture(t, cfg)
	for _, test := range []struct {
		name             string
		cfg              config
		v                overview
		now              time.Time
		backup, rollback bool
	}{
		{"enabled", cfg, v, projectTime, true, true},
		{"stale", cfg, v, projectTime.Add(91 * time.Second), true, false},
		{"disabled", config{App: "todo", Env: "production", Profile: "lite", Backend: "none"}, v, projectTime, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := webHandler(test.cfg, webFacts(t, test.v), fakeRelay(t), func() time.Time { return test.now })
			w := webGet(t, h, "/")
			body := html.UnescapeString(w.Body.String())
			if strings.Contains(body, "spinup backup now --type full --env production") != test.backup || strings.Contains(body, "spinup rollback --env production") != test.rollback {
				t.Fatalf("actions: %s", body)
			}
			if strings.Contains(body, `method="post"`) || strings.Contains(body, "onclick") {
				t.Fatal("stateful action exposed")
			}
			for _, path := range []string{"/actions/backup", "/actions/rollback"} {
				if webGet(t, h, path).Code != 404 {
					t.Fatal("action endpoint exists")
				}
			}
		})
	}
}

func TestWebStatesInvalidDocuments(t *testing.T) {
	cfg := webConfig()
	v, _ := webFixture(t, cfg)
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{}`, `null`, strings.Replace(string(encoded), `"version":1`, `"version":null`, 1),
		strings.Replace(string(encoded), `"observed_at":"2026-11-22T18:00:00Z"`, `"observed_at":null`, 1),
		strings.Repeat(" ", hostLimit+1) + string(encoded),
	} {
		h := webHandler(cfg, probeFS(map[string]string{"facts.json": raw}), fakeRelay(t), func() time.Time { return projectTime })
		w := webGet(t, h, "/api/v1/overview")
		var got overview
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.Release.State != "unavailable" || queryString(got.Release.Reason) != "invalid" || got.Release.Data != nil || got.Alerts.State != "ok" {
			t.Fatalf("invalid snapshot: %s", w.Body)
		}
	}
	// The limit applies to encoded bytes, not the length of names before escaping.
	for _, data := range []string{
		`[{"name":"` + strings.Repeat("x", 128<<10) + `","severity":null,"since":null}]`,
		`[` + strings.TrimSuffix(strings.Repeat(`{"name":"A","severity":null,"since":null},`, 257), ",") + `]`,
		`[{"name":"","severity":null,"since":null}]`,
		`null`,
	} {
		body := `{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":` + data + `}`
		client := &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) { return queryResponse(body), nil })}
		h := webHandler(cfg, webFacts(t, v), client, func() time.Time { return projectTime })
		w := webGet(t, h, "/api/v1/overview")
		var got overview
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.Alerts.State != "unavailable" || queryString(got.Alerts.Reason) != "invalid" || got.Release.State != "ok" || w.Body.Len() > 512<<10 {
			t.Fatalf("alert overflow: %s", w.Body)
		}
	}
	// A fresh failed observation must erase an older value, without erasing other sections.
	v.Disk = failedSection[space]("source_down", projectTime)
	h := webHandler(cfg, webFacts(t, v), fakeRelay(t), func() time.Time { return projectTime })
	w := webGet(t, h, "/")
	if !strings.Contains(w.Body.String(), "source_down") || !strings.Contains(w.Body.String(), currentID) {
		t.Fatal("partial failure damaged the page")
	}
	h = webHandler(cfg, webFacts(t, v), fakeRelay(t), func() time.Time { return projectTime.Add(91 * time.Second) })
	w = webGet(t, h, "/")
	if !strings.Contains(w.Body.String(), "sample_expired") || !strings.Contains(w.Body.String(), currentID) {
		t.Fatal("stale data was hidden or presented as current")
	}
}

func TestWebIsolationUntrustedFields(t *testing.T) {
	cfg := webConfig()
	probes := baseProbes()
	probes["jobs.json"].Data = []byte(`[{"queue":"<img src=x onerror=alert(1)>\u0001","state":"running","count":3}]`)
	v := runProject(t, probes, cfg)
	client := &http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/alerts" {
			return queryResponse(`{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[{"name":"<script>alert(1)</script>\u0001","severity":"<img src=x>","since":null}]}`), nil
		}
		return fakeRelay(t).Transport.RoundTrip(r)
	})}
	h := webHandler(cfg, webFacts(t, v), client, func() time.Time { return projectTime })
	for _, path := range []string{"/", "/logs?text=%22%3E%3Cscript%3E", "/api/v1/overview"} {
		w := webGet(t, h, path)
		if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "<img ") || strings.Contains(w.Body.String(), `\u0001`) {
			t.Fatalf("untrusted text escaped its context: %s", w.Body)
		}
	}
	// The JSON and HTML log routes must use the same filters and relay document.
	plain := webHandler(cfg, webFacts(t, v), fakeRelay(t), func() time.Time { return projectTime })
	jsonResult := webGet(t, plain, "/api/v1/logs?service=api&level=error&since=1h&text=needle")
	var got logs
	if err := json.Unmarshal(jsonResult.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	page := webGet(t, plain, "/logs?service=api&level=error&since=1h&text=needle")
	body := html.UnescapeString(page.Body.String())
	if jsonResult.Code != 200 || page.Code != 200 || !strings.Contains(body, got.Query) || !strings.Contains(body, got.Entries[0].Line) {
		t.Fatalf("log parity failed: %s", page.Body)
	}
}

func TestWebIsolationQueryLimit(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client := &http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 4*time.Second {
			t.Error("relay deadline missing")
		}
		started <- struct{}{}
		select {
		case <-release:
			return queryResponse(`{"state":"unavailable","reason":"source_down","at":"2026-11-22T18:00:00Z","data":null}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	h := webHandler(webConfig(), fstest.MapFS{}, client, func() time.Time { return projectTime })
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { defer func() { done <- struct{}{} }(); webGet(t, h, "/api/v1/overview") }()
	}
	defer func() { close(release); <-done; <-done }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("relay did not start")
		}
	}
	if w := webGet(t, h, "/api/v1/logs"); w.Code != 429 {
		t.Fatalf("busy: %d %s", w.Code, w.Body)
	}
	if w := webGet(t, h, "/healthz"); w.Code != 200 {
		t.Fatal("health blocked by query slots")
	}
	if w := webGet(t, h, "/assets/console.css"); w.Code != 200 || !strings.Contains(w.Body.String(), "@media") {
		t.Fatal("stylesheet blocked by query slots")
	}
}

func TestWebStatesRelayErrors(t *testing.T) {
	for _, test := range []struct {
		status, want int
		code         string
	}{{429, 429, "busy"}, {503, 503, "source_down"}, {302, 503, "source_down"}} {
		t.Run(test.code+http.StatusText(test.status), func(t *testing.T) {
			client := &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) {
				response := queryResponse("private source diagnostic")
				response.StatusCode = test.status
				response.Header.Set("Location", "http://evil.test")
				return response, nil
			})}
			h := webHandler(webConfig(), fstest.MapFS{}, client, func() time.Time { return projectTime })
			for _, path := range []string{"/logs", "/api/v1/logs"} {
				w := webGet(t, h, path)
				if w.Code != test.want || strings.Contains(w.Body.String(), "private source") {
					t.Errorf("relay status %d: %d %s", test.status, w.Code, w.Body)
				}
				if strings.HasPrefix(path, "/api/") {
					var got wireError
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Error.Code != test.code || got.Error.Try == "" {
						t.Fatalf("error catalog: %s", w.Body)
					}
				} else if !strings.Contains(w.Body.String(), "Try:") {
					t.Fatal("HTML error lacks repair instruction")
				}
			}
			if w := webGet(t, h, "/api/v1/overview"); w.Code != 200 {
				t.Fatal("relay HTTP error blocked overview")
			}
		})
	}
	for _, profile := range []string{"none", "absent"} {
		cfg := webConfig()
		cfg.Profile = profile
		h := webHandler(cfg, fstest.MapFS{}, fakeRelay(t), func() time.Time { return projectTime })
		for _, path := range []string{"/logs", "/api/v1/logs"} {
			if w := webGet(t, h, path); w.Code != 409 {
				t.Fatalf("disabled logs: %d %s", w.Code, w.Body)
			}
		}
	}
}

func TestWebStatesEncodedBudgets(t *testing.T) {
	cfg := webConfig()
	v, _ := webFixture(t, cfg)
	rows := []container{{Project: "todo", Service: "api", Name: strings.Repeat("<", 70000), State: "running"}}
	v.Containers = section[[]container]{State: "ok", At: &projectTime, Data: &rows}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte(`\u003c`), []byte("<"))
	alerts := `{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[{"name":"` + strings.Repeat("<", 23000) + `","severity":null,"since":null}]}`
	h := webHandler(cfg, probeFS(map[string]string{"facts.json": string(raw)}), &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) { return queryResponse(alerts), nil })}, func() time.Time { return projectTime })
	w := webGet(t, h, "/api/v1/overview")
	var got overview
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Body.Len() > 512<<10 || got.Containers.State != "unavailable" || got.Alerts.State != "unavailable" || queryString(got.Containers.Reason) != "invalid" || queryString(got.Alerts.Reason) != "invalid" || got.Disk.State != "ok" {
		t.Fatal("encoded limits damaged an unrelated section or permitted overflow")
	}
}
