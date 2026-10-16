// Package devtools serves the development page at /_dev.
package devtools

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

const maxRequests = 50

// Route describes one registered HTTP route.
type Route struct{ Pattern string }

// Migration describes the state of one database migration.
type Migration struct {
	Name    string
	Applied bool
}

// Options configures a Devtools instance.
type Options struct {
	// APIDocs shows a link to the mounted API reference.
	APIDocs    bool
	Name       string
	Version    string
	Config     any
	Routes     func() []Route
	Migrations func(context.Context) ([]Migration, error)
}

// Devtools provides a development-only diagnostics page and request recorder.
type Devtools struct {
	o           Options
	mu          sync.Mutex
	requests    [maxRequests]request
	next, count int
}

type request struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	RequestID  string `json:"request_id"`
}
type pageData struct {
	APIDocs       bool
	Name, Version string
	Config        []configValue
	Routes        []Route
	Requests      []request
	Migrations    []Migration
	Migration     string
}
type configValue struct{ Name, Value string }

// New creates a development diagnostics service.
func New(o Options) *Devtools { return &Devtools{o: o} }

// Handler serves the development page and its request JSON endpoint.
func (d *Devtools) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		switch r.URL.Path {
		case "/_dev/requests.json":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(d.snapshot())
		case "/_dev", "/_dev/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := pageData{APIDocs: d.o.APIDocs, Name: d.o.Name, Version: d.o.Version, Config: configValues(d.o.Config), Requests: d.snapshot()}
			if d.o.Routes != nil {
				data.Routes = d.o.Routes()
			}
			if d.o.Migrations == nil {
				data.Migration = "not configured"
			} else if migrations, err := d.o.Migrations(r.Context()); err != nil {
				data.Migration = "unavailable: " + err.Error()
			} else {
				data.Migrations = migrations
			}
			_ = pageTemplate.Execute(w, data)
		default:
			http.NotFound(w, r)
		}
	})
}

// Middleware records the last 50 HTTP requests. It is safe for concurrent use.
func (d *Devtools) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			requestID := httpx.RequestIDFrom(r.Context())
			if requestID == "" {
				requestID = r.Header.Get("X-Request-ID")
			}
			d.record(request{Method: r.Method, Path: r.URL.Path, Status: status, DurationMS: time.Since(start).Milliseconds(), RequestID: requestID})
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (d *Devtools) record(value request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests[d.next] = value
	d.next = (d.next + 1) % len(d.requests)
	if d.count < len(d.requests) {
		d.count++
	}
}
func (d *Devtools) snapshot() []request {
	d.mu.Lock()
	defer d.mu.Unlock()
	values := make([]request, d.count)
	start := (d.next - d.count + len(d.requests)) % len(d.requests)
	for i := range values {
		values[i] = d.requests[(start+i)%len(d.requests)]
	}
	return values
}

func configValues(value any) []configValue {
	var values []configValue
	configWalk(reflect.ValueOf(value), "", &values)
	return values
}
func configWalk(value reflect.Value, prefix string, values *[]configValue) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		*values = append(*values, configValue{Name: prefix, Value: maskValue(prefix, "", value)})
		return
	}
	typeOfValue := value.Type()
	for i := range value.NumField() {
		field := typeOfValue.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := field.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		fieldValue := value.Field(i)
		if field.Anonymous || indirectStruct(fieldValue) {
			configWalk(fieldValue, name, values)
			continue
		}
		*values = append(*values, configValue{Name: name, Value: maskValue(field.Name, field.Tag.Get("env"), fieldValue)})
	}
}
func indirectStruct(value reflect.Value) bool {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	return value.IsValid() && value.Kind() == reflect.Struct
}
func maskValue(name, env string, value reflect.Value) string {
	text := ""
	if value.IsValid() && value.CanInterface() {
		text = fmt.Sprint(value.Interface())
	}
	upperName, upperEnv := strings.ToUpper(name), strings.ToUpper(env)
	if containsSecret(upperName) || containsSecret(upperEnv) {
		return "****"
	}
	if (isCredentialURLName(upperName) || isCredentialURLName(upperEnv)) && hasCredentials(text) {
		return maskURL(text)
	}
	return text
}

func isCredentialURLName(value string) bool {
	value = strings.ReplaceAll(value, "_", "")
	return strings.Contains(value, "DATABASEURL") || strings.HasSuffix(value, "URL")
}
func containsSecret(value string) bool {
	for _, marker := range []string{"PASSWORD", "SECRET", "TOKEN", "KEY"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
func hasCredentials(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.User != nil
}
func maskURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return "****"
	}
	username := parsed.User.Username()
	if username == "" {
		return "****"
	}
	return parsed.Scheme + "://" + username + ":****@" + parsed.Host + parsed.RequestURI()
}

var pageTemplate = template.Must(template.New("devtools").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Development</title><style>body{font:14px system-ui;margin:2rem;color:#222}table{border-collapse:collapse;width:100%;margin:1rem 0}th,td{border:1px solid #ccc;padding:.4rem;text-align:left}th{background:#f3f3f3}code{white-space:pre-wrap}</style></head><body><h1>Development</h1><p><code>{{.Name}} {{.Version}}</code></p>{{if .APIDocs}}<p><a href="/api/docs">API reference</a></p>{{end}}<h2>Routes</h2><table><tr><th>Pattern</th></tr>{{range .Routes}}<tr><td><code>{{.Pattern}}</code></td></tr>{{end}}</table><h2>Configuration</h2><table><tr><th>Name</th><th>Value</th></tr>{{range .Config}}<tr><td>{{.Name}}</td><td><code>{{.Value}}</code></td></tr>{{end}}</table><h2>Requests</h2><p><a href="/_dev/requests.json">JSON</a></p><table><tr><th>Method</th><th>Path</th><th>Status</th><th>Duration ms</th><th>Request ID</th></tr>{{range .Requests}}<tr><td>{{.Method}}</td><td>{{.Path}}</td><td>{{.Status}}</td><td>{{.DurationMS}}</td><td>{{.RequestID}}</td></tr>{{end}}</table><h2>Pending migrations</h2>{{if .Migration}}<p>{{.Migration}}</p>{{else}}<table><tr><th>Name</th><th>Applied</th></tr>{{range .Migrations}}{{if not .Applied}}<tr><td>{{.Name}}</td><td>{{.Applied}}</td></tr>{{end}}{{end}}</table>{{end}}</body></html>`))
