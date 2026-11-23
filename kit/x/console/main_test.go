package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestMainModes(t *testing.T) {
	env := map[string]string{"CONSOLE_APP": "todo", "CONSOLE_ENV": "production", "CONSOLE_PROFILE": "none", "CONSOLE_BACKEND": "none", "CONSOLE_JOBS": "false", "CONSOLE_OBSERVED_AT": "2026-11-22T18:00:00Z"}
	for _, mode := range []string{"serve", "query", "project", "healthcheck", "healthcheck query"} {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			deps := mainRuntime{
				getenv: func(key string) string { return env[key] }, input: baseProbes(), facts: fstest.MapFS{}, now: func() time.Time { return projectTime },
				client: &http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
					want := "http://127.0.0.1:7410/healthz"
					if mode == "healthcheck query" {
						want = "http://127.0.0.1:7411/healthz"
					}
					if r.URL.String() != want || r.Method != http.MethodGet {
						t.Errorf("probe: %s %s", r.Method, r.URL)
					}
					called = true
					return queryResponse("ok\n"), nil
				})},
				serve: func(s *http.Server) error {
					called = true
					port := ":7410"
					if mode == "query" {
						port = ":7411"
					}
					if s.Addr != port || s.ReadHeaderTimeout != 3*time.Second || s.ReadTimeout != 5*time.Second || s.WriteTimeout != 10*time.Second || s.IdleTimeout != 30*time.Second {
						t.Errorf("server settings: %+v", s)
					}
					w := queryRequest(t, s.Handler, "http://127.0.0.1/healthz")
					if w.Code != 200 || w.Body.String() != "ok\n" {
						t.Errorf("process did not serve health: %d %s", w.Code, w.Body)
					}
					if mode == "serve" && queryRequest(t, s.Handler, "http://127.0.0.1/api/v1/overview").Code != 200 {
						t.Error("page mode did not serve overview")
					}
					if mode == "query" && queryRequest(t, s.Handler, "http://127.0.0.1/v1/logs?query=unsafe").Code != 400 {
						t.Error("query mode did not validate filters")
					}
					return http.ErrServerClosed
				},
			}
			if code := runMain(strings.Fields(mode), &stdout, &stderr, deps); code != 0 {
				t.Fatalf("mode %s: %d %s", mode, code, &stderr)
			}
			if mode == "project" {
				var got overview
				if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.App != "todo" || got.Release.State != "ok" || got.Release.Data != nil || got.Alerts.State != "disabled" || !got.ObservedAt.Equal(projectTime) {
					t.Fatalf("projection: %s", &stdout)
				}
			} else if !called || stdout.Len() != 0 {
				t.Fatal("mode did not execute or wrote stdout")
			}
		})
	}
	for _, test := range []struct {
		name   string
		args   []string
		change func(*mainRuntime)
		code   int
	}{
		{"arguments", []string{"serve", "extra"}, func(*mainRuntime) {}, 2},
		{"configuration", []string{"serve"}, func(d *mainRuntime) { d.getenv = func(string) string { return "" } }, 1},
		{"sample-time", []string{"project"}, func(d *mainRuntime) {
			d.getenv = func(key string) string {
				if key == "CONSOLE_OBSERVED_AT" {
					return "invalid"
				}
				return env[key]
			}
		}, 1},
		{"missing-sample-time", []string{"project"}, func(d *mainRuntime) {
			d.getenv = func(key string) string {
				if key == "CONSOLE_OBSERVED_AT" {
					return ""
				}
				return env[key]
			}
		}, 1},
		{"listen", []string{"serve"}, func(d *mainRuntime) {
			d.serve = func(*http.Server) error { return errors.New("private listener diagnostic") }
		}, 1},
		{"health-body", []string{"healthcheck"}, func(d *mainRuntime) {
			d.client = &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) { return queryResponse("wrong"), nil })}
		}, 1},
		{"health-down", []string{"healthcheck", "query"}, func(d *mainRuntime) {
			d.client = &http.Client{Transport: queryTransport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
		}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			d := mainRuntime{getenv: func(key string) string { return env[key] }, input: baseProbes(), now: func() time.Time { return projectTime }}
			test.change(&d)
			if code := runMain(test.args, &out, &errout, d); code != test.code || out.Len() != 0 || !strings.Contains(errout.String(), "\nTry:") || strings.Contains(errout.String(), "private listener") {
				t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
			}
		})
	}
}

func TestMainModesEntrypoint(t *testing.T) {
	for key, value := range map[string]string{"CONSOLE_APP": "todo", "CONSOLE_ENV": "production", "CONSOLE_PROFILE": "absent", "CONSOLE_BACKEND": "none", "CONSOLE_JOBS": "false", "CONSOLE_ORIGIN": "", "CONSOLE_OBSERVED_AT": time.Now().UTC().Format(time.RFC3339Nano)} {
		t.Setenv(key, value)
	}
	var out, errout bytes.Buffer
	if code := Main([]string{"project"}, &out, &errout); code != 0 {
		t.Fatalf("project: %d %s", code, &errout)
	}
	var got overview
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.App != "todo" || got.Profile != "absent" || got.ObservedAt == nil || got.Alerts.State != "disabled" {
		t.Fatalf("entrypoint: %s", &out)
	}
}
