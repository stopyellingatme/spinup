package monitor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

const dogfoodAlerts = `{"status":"success","data":{"alerts":[{"labels":{"alertname":"BackupStale","severity":"warning"},"annotations":{"summary":"No backup succeeded in 26 hours"},"state":"firing","activeAt":"2026-11-22T18:00:00Z","value":"1"},{"labels":{"alertname":"Heartbeat"},"annotations":{"summary":"The monitoring stack is alive"},"state":"firing","activeAt":"2026-11-22T18:00:00Z","value":"1"}]}}`

func TestDogfoodAlertsText(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	expectAPI(f, `[{"labels":{"alertname":"Prometheus alert","prometheus_alertname":"BackupStale","source":"prometheus"},"status":{"state":"active"}},{"labels":{"alertname":"Prometheus alert","prometheus_alertname":"Heartbeat","source":"prometheus"},"status":{"state":"active"}}]`)
	expectAPI(f, dogfoodAlerts)
	expectAPI(f, `{"status":"success","data":{"groups":[]}}`)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", out: &out}
	if err := c.run(context.Background(), Options{Action: "alerts"}); err != nil {
		t.Fatal(err)
	}
	want := "firing BackupStale warning No backup succeeded in 26 hours\nfiring Heartbeat The monitoring stack is alive\n"
	if out.String() != want {
		t.Fatalf("alerts = %q, want %q", out.String(), want)
	}
	if calls := f.Calls(); len(calls) >= 4 && !strings.Contains(strings.Join(calls[3].Argv, " "), "/api/datasources/proxy/uid/prometheus/api/v1/alerts") {
		t.Fatal(calls[3].Argv)
	}
	f.AssertDone(t)
}

func TestAlertsJSON(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "alerts", true: "empty"}[empty], func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			expectAPI(f, `[]`)
			body := dogfoodAlerts
			want := `[{"state":"firing","name":"BackupStale","severity":"warning","summary":"No backup succeeded in 26 hours"},{"state":"firing","name":"Heartbeat","severity":"","summary":"The monitoring stack is alive"}]` + "\n"
			if empty {
				body = `{"status":"success","data":{"alerts":[]}}`
				want = "[]\n"
			}
			expectAPI(f, body)
			expectAPI(f, `{"status":"success","data":{"groups":[]}}`)
			var out bytes.Buffer
			c := client{vm: f, app: "todo", out: &out}
			if err := c.run(context.Background(), Options{Action: "alerts", JSON: true}); err != nil {
				t.Fatal(err)
			}
			if out.String() != want {
				t.Fatalf("JSON = %q, want %q", out.String(), want)
			}
			f.AssertDone(t)
		})
	}
}

func TestAlertsFiringFirst(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	expectAPI(f, `[{"labels":{"alertname":"Custom","severity":"critical"},"annotations":{"summary":"A custom rule fired"},"status":{"state":"active"}},{"labels":{"alertname":"Hidden"},"status":{"state":"suppressed"}}]`)
	expectAPI(f, `{"status":"success","data":{"alerts":[{"state":"pending","labels":{"alertname":"Soon"},"annotations":{"summary":"Almost firing"}},{"state":"firing","labels":{"alertname":"Heartbeat"},"annotations":{"summary":"The monitoring stack is alive"}}]}}`)
	expectAPI(f, `{"status":"success","data":{"groups":[{"rules":[{"state":"pending","name":"Slow","labels":{"severity":"warning"},"annotations":{"summary":"A slow request"}},{"state":"pending","name":"Prometheus alert","labels":{"source":"prometheus"}}]}]}}`)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", out: &out}
	if err := c.run(context.Background(), Options{Action: "alerts"}); err != nil {
		t.Fatal(err)
	}
	want := "firing Custom critical A custom rule fired\nfiring Heartbeat The monitoring stack is alive\npending Soon Almost firing\npending Slow warning A slow request\nsilenced Hidden\n"
	if out.String() != want {
		t.Fatalf("alerts = %q, want %q", out.String(), want)
	}
	f.AssertDone(t)
}

func TestAlertsSilenced(t *testing.T) {
	t.Parallel()
	for _, jsonOutput := range []bool{false, true} {
		name := "text"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			expectAPI(f, `[{"labels":{"alertname":"Prometheus alert","prometheus_alertname":"BackupStale","source":"prometheus","severity":"warning","alertstate":"firing","grafana_folder":"spinup"},"status":{"state":"suppressed"}},{"labels":{"alertname":"Prometheus alert","prometheus_alertname":"Heartbeat","source":"prometheus"},"status":{"state":"active"}}]`)
			expectAPI(f, dogfoodAlerts)
			expectAPI(f, `{"status":"success","data":{"groups":[]}}`)
			var out bytes.Buffer
			c := client{vm: f, app: "todo", out: &out}
			if err := c.run(context.Background(), Options{Action: "alerts", JSON: jsonOutput}); err != nil {
				t.Fatal(err)
			}
			want := "firing Heartbeat The monitoring stack is alive\nsilenced BackupStale warning No backup succeeded in 26 hours\n"
			if jsonOutput {
				want = `[{"state":"firing","name":"Heartbeat","severity":"","summary":"The monitoring stack is alive"},{"state":"silenced","name":"BackupStale","severity":"warning","summary":"No backup succeeded in 26 hours"}]` + "\n"
			}
			if out.String() != want {
				t.Fatalf("alerts = %q, want %q", out.String(), want)
			}
			f.AssertDone(t)
		})
	}
}

func TestAlertsSilenceMatchesLabels(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	expectAPI(f, `[{"labels":{"alertname":"Prometheus alert","prometheus_alertname":"BackupStale","source":"prometheus","job":"backup","instance":"one"},"status":{"state":"suppressed"}},{"labels":{"alertname":"BackupStale","job":"backup","instance":"two"},"status":{"state":"suppressed"}}]`)
	expectAPI(f, `{"status":"success","data":{"alerts":[{"state":"firing","labels":{"alertname":"BackupStale","job":"backup","instance":"one"}},{"state":"firing","labels":{"alertname":"BackupStale","job":"backup","instance":"two"}},{"state":"pending","labels":{"alertname":"BackupStale","job":"other","instance":"one"}}]}}`)
	expectAPI(f, `{"status":"success","data":{"groups":[]}}`)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", out: &out}
	if err := c.run(context.Background(), Options{Action: "alerts"}); err != nil {
		t.Fatal(err)
	}
	want := "firing BackupStale\npending BackupStale\nsilenced BackupStale\nsilenced BackupStale\n"
	if out.String() != want {
		t.Fatalf("alerts = %q, want %q", out.String(), want)
	}
	f.AssertDone(t)
}
