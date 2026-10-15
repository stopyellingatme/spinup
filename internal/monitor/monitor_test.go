package monitor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestReadCommands(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ action, body, want string }{
		{"logs", `{"status":"success","data":{"resultType":"streams","result":[{"values":[["1000000000","hello"]]}]}}`, "1970-01-01T00:00:01Z hello"},
		{"alerts", `[{"labels":{"alertname":"Down"},"status":{"state":"active"}}]`, "Down"},
		{"trace", `{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"spanId":"a","name":"GET /","startTimeUnixNano":"0","endTimeUnixNano":"1000000"}]}]}]}`, "api GET / 1ms"},
	} {
		t.Run(tt.action, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "ps").Return("abc123\n", "", 0)
			f.Expect("sh", "-c").Return(tt.body, "", 0)
			if tt.action == "alerts" {
				f.Expect("docker", "ps").Return("abc123", "", 0)
				f.Expect("sh", "-c").Return(`{"status":"success","data":{"groups":[{"rules":[{"name":"Slow","state":"pending"}]}]}}`, "", 0)
			}
			var out bytes.Buffer
			c := client{vm: f, app: "todo", profile: "full", out: &out, now: func() time.Time { return time.Unix(3600, 0) }}
			err := c.run(context.Background(), Options{Action: tt.action, TraceID: strings.Repeat("a", 32), Since: "1h"})
			if err != nil || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output=%q error=%v", out.String(), err)
			}
			f.AssertDone(t)
		})
	}
}
