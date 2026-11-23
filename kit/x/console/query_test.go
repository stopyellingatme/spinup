package console

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type queryTransport func(*http.Request) (*http.Response, error)

func (f queryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func queryResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func queryRequest(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	return w
}

func TestQueryAlerts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	h := queryHandler(&http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != "http://prometheus:9090/api/v1/alerts" {
			t.Errorf("unexpected upstream: %s %s", r.Method, r.URL)
		}
		return queryResponse(`{"status":"success","data":{"alerts":[
   {"state":"firing","labels":{"alertname":"Heartbeat","instance":"one"}},
   {"state":"pending","labels":{"alertname":"Pending"}},
   {"state":"inactive","labels":{"alertname":"Inactive"}},
   {"state":"firing","labels":{"alertname":"BackupStale","severity":"warning","instance":"one"},"activeAt":"2026-11-22T18:00:00Z","annotations":{"secret":"hidden"}},
   {"state":"firing","labels":{"alertname":"Heartbeat","instance":"two"}}
  ]}}`), nil
	})}, func() time.Time { return now })
	w := queryRequest(t, h, "/v1/alerts")
	var got section[[]alert]
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.State != "ok" || got.Data == nil || len(*got.Data) != 3 {
		t.Fatalf("alerts: %d %s", w.Code, w.Body.String())
	}
	rows := *got.Data
	if rows[0].Name != "BackupStale" || rows[1].Name != "Heartbeat" || rows[2].Name != "Heartbeat" || rows[0].Severity == nil || *rows[0].Severity != "warning" || rows[0].Since == nil || !got.At.Equal(now) {
		t.Fatalf("unexpected alerts: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "instance") {
		t.Fatal("unprojected alert data escaped")
	}
}

func TestQueryLogs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 20, 0, 0, 123456789, time.UTC)
	at := now.Add(-time.Minute).UnixNano()
	body := fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"service_name":"z-api","detected_level":"error","private":"hidden"},"values":[["%d","z line"],["%d","metadata line",{"service_name":"a-worker","detected_level":"warn","secret":"hidden"}]]},
 {"stream":{"service_name":"a-api"},"values":[["%d","no level"]]},
 {"stream":{"service_name":"b-api","detected_level":"info"},"values":[["%d","b line"]]}
 ]}}`, at, at, at, at+1)
	var sent *http.Request
	h := queryHandler(&http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) { sent = r; return queryResponse(body), nil })}, func() time.Time { return now })
	w := queryRequest(t, h, "/v1/logs")
	var got logs
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Version != 1 || len(got.Entries) != 4 || got.More || got.Query != `{service_name=~".+"}` {
		t.Fatalf("logs: %d %s", w.Code, w.Body.String())
	}
	if !got.Start.Equal(now.Add(-time.Hour)) || !got.End.Equal(now) {
		t.Fatalf("window: %+v", got)
	}
	if got.Entries[0].Service != "b-api" || got.Entries[1].Service != "a-api" || got.Entries[1].Level != nil || got.Entries[2].Service != "a-worker" || *got.Entries[2].Level != "warn" {
		t.Fatalf("tuple merge: %+v", got.Entries)
	}
	if strings.Contains(w.Body.String(), "hidden") {
		t.Fatal("private metadata escaped")
	}
	q := sent.URL.Query()
	if sent.Method != http.MethodGet || sent.URL.Host != "loki:3100" || sent.URL.Path != "/loki/api/v1/query_range" || len(q) != 5 || q.Get("direction") != "backward" || q.Get("limit") != "101" || q.Get("start") != strconv.FormatInt(got.Start.UnixNano(), 10) || q.Get("end") != strconv.FormatInt(now.UnixNano(), 10) || q.Get("query") != got.Query {
		t.Fatalf("upstream query: %s", sent.URL)
	}
	w = queryRequest(t, h, "/v1/logs?level=error")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Service != "z-api" || got.Query != `{service_name=~".+"} | detected_level="error"` {
		t.Fatalf("missing level matched filter: %s", w.Body.String())
	}
	w = queryRequest(t, h, "/v1/logs?service=a-worker&level=warn")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Service != "a-worker" {
		t.Fatalf("metadata filter: %s", w.Body.String())
	}
}

func TestQueryLogsLiteralAndMore(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	text := `"} | json | secret != "x\\` + "'"
	var tuples []any
	for i := 0; i < 101; i++ {
		tuples = append(tuples, []string{strconv.FormatInt(now.Add(-time.Duration(i)*time.Nanosecond).UnixNano(), 10), text})
	}
	data, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"service_name": "a-api", "detected_level": "info"}, "values": tuples}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := queryHandler(&http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		want := `{service_name="a-api"} |= ` + strconv.Quote(text) + ` | detected_level="info"`
		if r.URL.Query().Get("query") != want {
			t.Errorf("literal changed query structure: %s", r.URL.Query().Get("query"))
		}
		return queryResponse(string(data)), nil
	})}, func() time.Time { return now })
	w := queryRequest(t, h, "/v1/logs?"+url.Values{"service": {"a-api"}, "level": {"info"}, "since": {"15m"}, "text": {text}}.Encode())
	var got logs
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(got.Entries) != 100 || !got.More || !got.Start.Equal(now.Add(-15*time.Minute)) {
		t.Fatalf("bounded log result: %d %s", w.Code, w.Body.String())
	}
}

func TestQueryExplore(t *testing.T) {
	t.Parallel()
	for _, nanos := range []int{0, 1, 999999, 1000000, 123456789} {
		t.Run(strconv.Itoa(nanos), func(t *testing.T) {
			now := time.Date(2026, 11, 22, 20, 0, 0, nanos, time.UTC)
			h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
				return queryResponse(`{"status":"success","data":{"resultType":"streams","result":[]}}`), nil
			})}, func() time.Time { return now })
			text := `a "b" & c+%`
			w := queryRequest(t, h, "/v1/logs?"+url.Values{"text": {text}, "since": {"24h"}}.Encode())
			var got logs
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(got.ExplorePath)
			if err != nil {
				t.Fatal(err)
			}
			if u.Path != "/explore" || u.IsAbs() || u.Query().Get("schemaVersion") != "1" || u.Query().Get("orgId") != "1" || len(u.Query()) != 3 {
				t.Fatalf("explore path: %s", got.ExplorePath)
			}
			var panes map[string]struct {
				Datasource string `json:"datasource"`
				Queries    []struct {
					RefID      string `json:"refId"`
					Expr       string `json:"expr"`
					Datasource struct {
						Type string `json:"type"`
						UID  string `json:"uid"`
					} `json:"datasource"`
				} `json:"queries"`
				Range struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"range"`
			}
			if err := json.Unmarshal([]byte(u.Query().Get("panes")), &panes); err != nil {
				t.Fatal(err)
			}
			pane := panes["a"]
			end := now.UnixMilli()
			if nanos%1000000 != 0 {
				end++
			}
			if len(panes) != 1 || pane.Datasource != "loki" || len(pane.Queries) != 1 || pane.Queries[0].Expr != got.Query || pane.Queries[0].RefID != "A" || pane.Queries[0].Datasource.Type != "loki" || pane.Queries[0].Datasource.UID != "loki" || pane.Range.From != strconv.FormatInt(got.Start.UnixMilli(), 10) || pane.Range.To != strconv.FormatInt(end, 10) {
				t.Fatalf("explore window/query differs: %s", u.Query().Get("panes"))
			}
		})
	}
}

func TestQueryAllowlist(t *testing.T) {
	t.Parallel()
	calls := 0
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
		calls++
		return queryResponse(`{"status":"success","data":{"resultType":"streams","result":[]}}`), nil
	})}, time.Now)
	for _, raw := range []string{
		"service=a&service=b", "service=a&service=", "level=info&level=info", "since=1h&since=6h", "text=a&text=b", "query={}", "url=http://evil", "upstream=http://evil", "method=POST", "header=Authorization:x", "grafana_port=3000", "direction=forward", "limit=1", "start=1", "end=2", "level=trace", "level=INFO", "since=2h", "service=-a", "service=a/b", "service=" + strings.Repeat("a", 129), "text=" + url.QueryEscape(strings.Repeat("é", 129)), "text=hello%0Aworld", "text=%1B%5B31m", "text=%C2%85", "text=%FF", "text=%ZZ", "text=a;b", "service=%22%7D%20%7C%20json",
	} {
		t.Run(raw, func(t *testing.T) {
			w := queryRequest(t, h, "/v1/logs?"+raw)
			assertQueryError(t, w, 400, "invalid_filter")
		})
	}
	for _, path := range []string{"/v1/alerts?url=http://evil", "/v1/alerts?service=a", "/healthz?query=x"} {
		assertQueryError(t, queryRequest(t, h, path), 400, "invalid_filter")
	}
	for _, path := range []string{"/unknown", "/v1/logs/", "/api/v1/logs", "/loki/api/v1/push"} {
		assertQueryError(t, queryRequest(t, h, path), 404, "not_found")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodPatch, http.MethodOptions, http.MethodConnect, http.MethodTrace} {
		for _, path := range []string{"/v1/alerts", "/v1/logs", "/healthz"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, path, nil))
			assertQueryError(t, w, 405, "method_not_allowed")
			if w.Header().Get("Allow") != "GET" {
				t.Fatal("missing Allow")
			}
		}
	}
	for _, header := range []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/logs", nil)
		r.Header.Set(header, "http://evil")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertQueryError(t, w, 400, "invalid_filter")
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached upstream %d times", calls)
	}
	for _, raw := range []string{"", "service=&level=&since=&text=", "service=" + strings.Repeat("a", 128), "text=" + url.QueryEscape(strings.Repeat("é", 128)), "level=debug&since=6h", "level=warn&since=24h"} {
		if w := queryRequest(t, h, "/v1/logs?"+raw); w.Code != 200 {
			t.Fatalf("valid filter rejected: %s: %s", raw, w.Body.String())
		}
	}
	w := queryRequest(t, h, "/healthz")
	if w.Code != 200 || w.Body.String() != "ok\n" {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}
}

func assertQueryError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var got wireError
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %d %q", w.Code, w.Body.String())
	}
	want := errorCatalog[code]
	if w.Code != status || got.Error.Code != code || got.Error.Message != want.Message || got.Error.Try != want.Try {
		t.Fatalf("error: %d %s", w.Code, w.Body.String())
	}
}

func TestQueryAllowlistHeadersAndRedirect(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		if len(r.Header) != 0 || r.Host != "loki:3100" || r.URL.User != nil {
			t.Errorf("request headers/host forwarded: %+v", r)
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 4*time.Second {
			t.Error("missing bounded deadline")
		}
		response := queryResponse("private upstream diagnostics")
		response.StatusCode = http.StatusFound
		response.Header.Set("Location", "http://evil/secret")
		return response, nil
	}), CheckRedirect: func(_ *http.Request, _ []*http.Request) error { t.Error("caller redirect policy used"); return nil }}
	h := queryHandler(client, time.Now)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/v1/logs", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Cookie", "session=secret")
	r.Header.Set("Forwarded", "host=evil")
	r.Header.Set("X-Forwarded-Host", "evil")
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("X-Scope-OrgID", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertQueryError(t, w, 503, "source_down")
	if strings.Contains(w.Body.String(), "private") || w.Header().Get("Location") != "" {
		t.Fatal("redirect or diagnostic escaped")
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers: %v", w.Header())
	}
	if client.CheckRedirect == nil {
		t.Fatal("caller client mutated")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(_ *http.Request) (*url.URL, error) { t.Error("proxy used"); return nil, nil }
	// Inspect the production transport without opening a socket.
	safe := queryClient(&http.Client{Transport: transport})
	if safe.Transport.(*http.Transport).Proxy != nil || transport.Proxy == nil || safe.Jar != nil {
		t.Fatal("environment proxy disabled incorrectly")
	}
}

func TestQueryLimits(t *testing.T) {
	t.Parallel()
	valid := `{"status":"success","data":{"resultType":"streams","result":[]}}`
	cases := []struct {
		name, body, encoding string
		status               int
	}{
		{"oversize", strings.Repeat(" ", 2<<20) + valid, "", 200},
		{"gzip bomb", strings.Repeat(" ", 2<<20) + valid, "gzip", 200},
		{"broken gzip", "not compressed", "gzip", 200},
		{"unsupported encoding", valid, "br", 200},
		{"bad JSON", "{private diagnostics", "", 200},
		{"trailing JSON", valid + ` {}`, "", 200},
		{"missing data", `{"status":"success"}`, "", 200},
		{"missing result", `{"status":"success","data":{"resultType":"streams"}}`, "", 200},
		{"wrong result type", `{"status":"success","data":{"resultType":"matrix","result":[]}}`, "", 200},
		{"upstream failure", valid, "", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
				body := tc.body
				if tc.encoding == "gzip" && tc.name != "broken gzip" {
					var b bytes.Buffer
					gz := gzip.NewWriter(&b)
					if _, err := gz.Write([]byte(body)); err != nil {
						t.Fatal(err)
					}
					if err := gz.Close(); err != nil {
						t.Fatal(err)
					}
					body = b.String()
				}
				r := queryResponse(body)
				r.StatusCode = tc.status
				r.Header.Set("Content-Encoding", tc.encoding)
				return r, nil
			})}, time.Now)
			assertQueryError(t, queryRequest(t, h, "/v1/logs"), 503, "source_down")
		})
	}
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
		t.Error("oversized target reached upstream")
		return nil, errors.New("private")
	})}, time.Now)
	assertQueryError(t, queryRequest(t, h, "/v1/logs?text="+strings.Repeat("x", 8192)), 414, "query_too_long")
}

func TestQueryLimitsBusyAndDeadline(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		return queryResponse(`{"status":"success","data":{"resultType":"streams","result":[]}}`), nil
	})}, time.Now)
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { done <- queryRequest(t, h, "/v1/logs") }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("queries did not enter")
		}
	}
	w := queryRequest(t, h, "/v1/alerts")
	assertQueryError(t, w, 429, "busy")
	close(release)
	for range 2 {
		if w := <-done; w.Code != 200 {
			t.Fatalf("in-flight query failed: %d", w.Code)
		}
	}
	// Cancellation must reach the upstream. The response must hide its diagnostics.
	h = queryHandler(&http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}, time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/logs", nil))
	assertQueryError(t, w, 503, "source_down")
	// The relay sets its own deadline even when the caller supplies none.
	h = queryHandler(&http.Client{Transport: queryTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 4*time.Second || time.Until(deadline) <= 0 {
			t.Errorf("deadline: %v, %v", deadline, ok)
		}
		return nil, context.DeadlineExceeded
	})}, time.Now)
	w = queryRequest(t, h, "/v1/alerts")
	var got section[[]alert]
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.State != "unavailable" || got.Reason == nil || *got.Reason != "timeout" || got.Data != nil || got.At == nil {
		t.Fatalf("alert timeout: %d %s", w.Code, w.Body.String())
	}
}

func TestQueryAlertsLimits(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"status":"success","data":{"alerts":[{"state":"firing","labels":{}}]}}`,
		`{"status":"success","data":{"alerts":[{"state":"firing","labels":{"alertname":"A"},"activeAt":"invalid"}]}}`,
		`{"status":"success","data":{}}`,
		`{"status":"error","error":"private diagnostics"}`,
		`invalid private diagnostics`,
		`{"status":"success","data":{"alerts":[{"state":"firing","labels":{"alertname":"` + strings.Repeat("x", 128<<10) + `"}}]}}`,
		// JSON escapes each less-than sign as six bytes. The wire size exceeds the budget.
		`{"status":"success","data":{"alerts":[{"state":"firing","labels":{"alertname":"` + strings.Repeat("<", 30<<10) + `"}}]}}`,
		`{"status":"success","data":{"alerts":[` + strings.TrimSuffix(strings.Repeat(`{"state":"firing","labels":{"alertname":"A"}},`, 257), ",") + `]}}`,
	} {
		h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) { return queryResponse(body), nil })}, time.Now)
		w := queryRequest(t, h, "/v1/alerts")
		var got section[[]alert]
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.State != "unavailable" || got.Reason == nil || *got.Reason != "invalid" || got.Data != nil || got.At == nil || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("invalid alerts: %s", w.Body.String())
		}
	}
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
		return queryResponse(`{"status":"success","data":{"alerts":[]}}`), nil
	})}, time.Now)
	w := queryRequest(t, h, "/v1/alerts")
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty alerts: %s", w.Body.String())
	}
}

func TestQueryLogsMalformedTuples(t *testing.T) {
	t.Parallel()
	for _, tuple := range []string{`[]`, `["1"]`, `["not-nanoseconds","line"]`, `[1,"line"]`, `["1",null]`, `["1","line",[]]`, `["1","line",null]`, `["1","line",{},"extra"]`, `["1","line",{"detected_level":true}]`} {
		h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
			return queryResponse(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"service_name":"api"},"values":[` + tuple + `]}]}}`), nil
		})}, time.Now)
		assertQueryError(t, queryRequest(t, h, "/v1/logs"), 503, "source_down")
	}
}

func TestQueryRedaction(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct{ raw, want string }{
		{"Bearer abcdefghijkl Basic abcdefghijkl", "Bearer [REDACTED] Basic [REDACTED]"},
		{`password="quoted secret" passwd='secret phrase' token=topsecret api_key=abcdefgh authorization=Bearer abcdefgh`, `password=[REDACTED] passwd=[REDACTED] token=[REDACTED] api_key=[REDACTED] authorization=[REDACTED]`},
		{`{"password":"quote\"secret","private_key":"line\nsecret"}`, `{"password":[REDACTED],"private_key":[REDACTED]}`},
		{`postgres://alice:dbsecret@postgres/app eyJabc.eyJdef.signature`, `postgres://alice:[REDACTED]@postgres/app [REDACTED_JWT]`},
		{"\x1b[31mred\x1b[0m\n\r\t\x00\x1b]0;private-title\x07done", "reddone"},
		{"\u009b31mred\u009b0m\u009dprivate-title\u009cdone\x1b(B", "reddone"},
		{strings.Repeat("é", 2050), strings.Repeat("é", 2048)},
		{strings.Repeat("a", 4095) + "é", strings.Repeat("a", 4095)},
	}
	for _, tc := range cases {
		b, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"service_name": "api\n", "detected_level": "info\x1b[0m"}, "values": []any{[]string{strconv.FormatInt(now.UnixNano(), 10), tc.raw}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) { return queryResponse(string(b)), nil })}, func() time.Time { return now })
		w := queryRequest(t, h, "/v1/logs")
		var got logs
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Entries) != 1 || got.Entries[0].Line != tc.want || got.Entries[0].Service != "api" || *got.Entries[0].Level != "info" || !utf8.ValidString(got.Entries[0].Line) {
			t.Fatalf("redaction: %s", w.Body.String())
		}
	}
}

func TestQueryLogsEqualTimestamps(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	at := strconv.FormatInt(now.UnixNano(), 10)
	body := `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"service_name":"api","detected_level":"warn"},"values":[["` + at + `","warning"]]},
 {"stream":{"service_name":"api","detected_level":"info"},"values":[["` + at + `","z"],["` + at + `","a"]]},
 {"stream":{"service_name":"api"},"values":[["` + strconv.FormatInt(now.Add(time.Nanosecond).UnixNano(), 10) + `","future"],["` + strconv.FormatInt(now.Add(-time.Hour-time.Nanosecond).UnixNano(), 10) + `","old"],["` + strconv.FormatInt(now.Add(-time.Hour).UnixNano(), 10) + `","start"]]}
 ]}}`
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) { return queryResponse(body), nil })}, func() time.Time { return now })
	w := queryRequest(t, h, "/v1/logs")
	var got logs
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 4 || got.Entries[0].Line != "a" || got.Entries[1].Line != "z" || got.Entries[2].Line != "warning" || got.Entries[3].Line != "start" {
		t.Fatalf("tie order or inclusive window: %s", w.Body.String())
	}
}

func TestQueryLimitsExactDecodedSize(t *testing.T) {
	t.Parallel()
	body := `{"status":"success","data":{"resultType":"streams","result":[]}}`
	body += strings.Repeat(" ", (2<<20)-len(body))
	for _, compressed := range []bool{false, true} {
		h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
			payload := body
			if compressed {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				if _, err := gz.Write([]byte(payload)); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				payload = b.String()
			}
			r := queryResponse(payload)
			if compressed {
				r.Header.Set("Content-Encoding", "gzip")
			}
			return r, nil
		})}, time.Now)
		if w := queryRequest(t, h, "/v1/logs"); w.Code != 200 {
			t.Fatalf("2 MiB body rejected (gzip=%v): %s", compressed, w.Body.String())
		}
	}
}

func TestQueryAlertsSortingAndControls(t *testing.T) {
	t.Parallel()
	h := queryHandler(&http.Client{Transport: queryTransport(func(_ *http.Request) (*http.Response, error) {
		return queryResponse(`{"status":"success","data":{"alerts":[
 {"state":"firing","labels":{"alertname":"A","severity":"warning"}},
 {"state":"firing","labels":{"alertname":"A","severity":"info"},"activeAt":"2026-11-22T20:00:00+01:00"},
 {"state":"firing","labels":{"alertname":"A","severity":"info"},"activeAt":"2026-11-22T18:00:00Z"},
 {"state":"firing","labels":{"alertname":"A\n\u001b[31m","severity":"info\r"}}
 ]}}`), nil
	})}, time.Now)
	w := queryRequest(t, h, "/v1/alerts")
	var got section[[]alert]
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data == nil || len(*got.Data) != 4 {
		t.Fatalf("alerts: %s", w.Body.String())
	}
	rows := *got.Data
	if rows[0].Name != "A" || rows[0].Since != nil || rows[1].Since.Hour() != 18 || rows[2].Since.Hour() != 19 || *rows[3].Severity != "warning" {
		t.Fatalf("alert order: %s", w.Body.String())
	}
}
