package monitor

import (
	"bytes"
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestQueriesUseGrafanaProxy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ action, path, response string }{
		{"logs", "/api/datasources/proxy/uid/loki/loki/api/v1/query_range", `{"status":"success","data":{"result":[]}}`},
		{"trace", "/api/datasources/proxy/uid/tempo/api/traces/" + strings.Repeat("a", 32), `{}`},
	} {
		t.Run(tt.action, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "ps").Return("abc123", "", 0)
			f.Expect().Return(tt.response, "", 0)
			c := client{vm: f, app: "todo", profile: "full", now: func() time.Time { return time.Unix(3600, 0) }}
			if err := c.run(context.Background(), Options{Action: tt.action, TraceID: strings.Repeat("a", 32)}); err != nil {
				t.Fatal(err)
			}
			calls := f.Calls()
			if !slices.Contains(calls[0].Argv, "label=com.docker.compose.service=grafana") {
				t.Fatalf("container lookup must select Grafana: %v", calls[0].Argv)
			}
			args := calls[1].Argv
			if len(args) != 10 || args[0] != "sh" || args[3] != "spinup-grafana" || args[5] != "abc123" || !strings.Contains(args[2], `docker exec -i "$2"`) {
				t.Fatalf("request must exec in the Grafana container: %v", args)
			}
			if args[4] != "/opt/todo/secrets/GRAFANA_ADMIN_PASSWORD" || !strings.Contains(args[2], `cat "$1"`) {
				t.Fatalf("request must pipe the password from the VM secret: %v", args)
			}
			request, err := url.Parse(args[7])
			if err != nil || request.Host != "127.0.0.1:3000" || request.Path != tt.path {
				t.Fatalf("request URL = %q, error = %v", args[7], err)
			}
			f.AssertDone(t)
		})
	}
}

func TestLogsSelectNewestAndPrintChronologically(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("docker", "ps").Return("abc123", "", 0)
	f.Expect().Return(`{"status":"success","data":{"result":[{"values":[["3000000000","newest"],["1000000000","oldest"]]},{"values":[["2000000000","middle"]]}]}}`, "", 0)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", out: &out, now: func() time.Time { return time.Unix(3600, 0) }}
	if err := c.logs(context.Background(), Options{Since: "1h", Service: "todo-api"}); err != nil {
		t.Fatal(err)
	}
	var request *url.URL
	for _, arg := range f.Calls()[1].Argv {
		if strings.HasPrefix(arg, "http://") {
			var err error
			request, err = url.Parse(arg)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if request == nil {
		t.Fatal("request URL missing")
	}
	for key, want := range map[string]string{"direction": "backward", "limit": "1000", "start": "0", "end": "3600000000000", "query": `{service_name="todo-api"}`} {
		if got := request.Query().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if want := "1970-01-01T00:00:01Z oldest\n1970-01-01T00:00:02Z middle\n1970-01-01T00:00:03Z newest\n"; out.String() != want {
		t.Fatalf("logs = %q, want %q", out.String(), want)
	}
	f.AssertDone(t)
}
