package console

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const queryMaxBytes = 2 << 20

type logFilter struct {
	Service, Level, Since, Text string
}

var logService = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

var errQueryData = errors.New("invalid console source data")

// parseLogFilter is shared by the page and the relay. Decoding the raw query
// must succeed before calling this function, since Values cannot retain errors.
func parseLogFilter(values url.Values) (logFilter, error) {
	for key, list := range values {
		if !slices.Contains([]string{"service", "level", "since", "text"}, key) || len(list) != 1 {
			return logFilter{}, errQueryData
		}
	}
	f := logFilter{Service: values.Get("service"), Level: values.Get("level"), Since: values.Get("since"), Text: values.Get("text")}
	if f.Since == "" {
		f.Since = "1h"
	}
	if f.Service != "" && !logService.MatchString(f.Service) ||
		f.Level != "" && !slices.Contains([]string{"debug", "info", "warn", "error"}, f.Level) ||
		!slices.Contains([]string{"15m", "1h", "6h", "24h"}, f.Since) || len(f.Text) > 256 || !utf8.ValidString(f.Text) {
		return logFilter{}, errQueryData
	}
	for _, r := range f.Text {
		if unicode.IsControl(r) {
			return logFilter{}, errQueryData
		}
	}
	return f, nil
}

func (f logFilter) logQL() string {
	query := `{service_name=~".+"}`
	if f.Service != "" {
		query = "{service_name=" + strconv.Quote(f.Service) + "}"
	}
	if f.Text != "" {
		query += " |= " + strconv.Quote(f.Text)
	}
	if f.Level != "" {
		query += " | detected_level=" + strconv.Quote(f.Level)
	}
	return query
}

// queryClient keeps injected transports usable without inheriting cookies,
// redirect policies, or environment proxies from the caller.
func queryClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		clone := base.Clone()
		clone.Proxy = nil
		transport = clone
	}
	return &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func queryHandler(client *http.Client, now clock) http.Handler {
	client = queryClient(client)
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		target := r.RequestURI
		if target == "" {
			target = r.URL.RequestURI()
		}
		if len(target) > 8<<10 {
			queryWriteError(w, "query_too_long")
			return
		}
		if !slices.Contains([]string{"/healthz", "/v1/alerts", "/v1/logs"}, r.URL.Path) {
			queryWriteError(w, "not_found")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			queryWriteError(w, "method_not_allowed")
			return
		}
		for _, header := range []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
			if _, present := r.Header[http.CanonicalHeaderKey(header)]; present {
				queryWriteError(w, "invalid_filter")
				return
			}
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			queryWriteError(w, "invalid_filter")
			return
		}
		var filter logFilter
		if r.URL.Path == "/v1/logs" {
			filter, err = parseLogFilter(values)
		} else if len(values) != 0 {
			err = errQueryData
		}
		if err != nil {
			queryWriteError(w, "invalid_filter")
			return
		}
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			queryWriteError(w, "busy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		at := now().UTC()
		if r.URL.Path == "/v1/alerts" {
			result := queryAlerts(ctx, client, at)
			queryWriteJSON(w, http.StatusOK, result)
			return
		}
		result, err := queryLogs(ctx, client, filter, at)
		if err != nil {
			queryWriteError(w, "source_down")
			return
		}
		queryWriteJSON(w, http.StatusOK, result)
	})
}

func queryWriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func queryWriteError(w http.ResponseWriter, code string) {
	item := errorCatalog[code]
	queryWriteJSON(w, item.Status, wireError{Error: errorDetail{Code: code, Message: item.Message, Try: item.Try}})
}

// queryRead bounds the decoded body, including responses transparently decoded
// by net/http and explicit gzip responses from injected transports.
func queryRead(ctx context.Context, client *http.Client, target string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("console source did not answer")
	}
	var reader io.Reader = response.Body
	switch response.Header.Get("Content-Encoding") {
	case "":
	case "gzip":
		decoded, decodeErr := gzip.NewReader(reader)
		if decodeErr != nil {
			return errQueryData
		}
		defer func() { _ = decoded.Close() }()
		reader = decoded
	default:
		return errQueryData
	}
	data, err := io.ReadAll(io.LimitReader(reader, queryMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > queryMaxBytes || !utf8.Valid(data) {
		return errQueryData
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errQueryData
	}
	return nil
}

func queryAlerts(ctx context.Context, client *http.Client, at time.Time) section[[]alert] {
	var response struct {
		Status string `json:"status"`
		Data   *struct {
			Alerts []struct {
				State    string            `json:"state"`
				Labels   map[string]string `json:"labels"`
				ActiveAt *time.Time        `json:"activeAt"`
			} `json:"alerts"`
		} `json:"data"`
	}
	unavailable := func(err error) section[[]alert] {
		reason := "source_down"
		if errors.Is(err, errQueryData) {
			reason = "invalid"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		return section[[]alert]{State: "unavailable", Reason: &reason, At: &at}
	}
	if err := queryRead(ctx, client, "http://prometheus:9090/api/v1/alerts", &response); err != nil {
		return unavailable(err)
	}
	if response.Status != "success" || response.Data == nil || response.Data.Alerts == nil {
		return unavailable(errQueryData)
	}
	rows := make([]alert, 0)
	for _, item := range response.Data.Alerts {
		if item.State != "firing" {
			continue
		}
		name := queryClean(item.Labels["alertname"])
		if name == "" || len(rows) == 256 {
			return unavailable(errQueryData)
		}
		var severity *string
		if value := queryClean(item.Labels["severity"]); value != "" {
			severity = &value
		}
		if item.ActiveAt != nil {
			value := item.ActiveAt.UTC()
			item.ActiveAt = &value
		}
		rows = append(rows, alert{Name: name, Severity: severity, Since: item.ActiveAt})
	}
	slices.SortFunc(rows, func(a, b alert) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		if n := strings.Compare(queryString(a.Severity), queryString(b.Severity)); n != 0 {
			return n
		}
		if a.Since == nil && b.Since != nil {
			return -1
		}
		if a.Since != nil && b.Since == nil {
			return 1
		}
		if a.Since != nil {
			return a.Since.Compare(*b.Since)
		}
		return 0
	})
	result := section[[]alert]{State: "ok", At: &at, Data: &rows}
	data, err := json.Marshal(result)
	// Include the newline emitted by the JSON encoder in the wire budget.
	if err != nil || len(data)+1 > 128<<10 {
		return unavailable(errQueryData)
	}
	return result
}

func queryLogs(ctx context.Context, client *http.Client, filter logFilter, end time.Time) (logs, error) {
	duration, _ := time.ParseDuration(filter.Since) // parseLogFilter already validated this enum.
	result := logs{Version: 1, Start: end.Add(-duration), End: end, Query: filter.logQL(), Entries: make([]line, 0)}
	values := url.Values{
		"query": {result.Query}, "start": {strconv.FormatInt(result.Start.UnixNano(), 10)},
		"end": {strconv.FormatInt(end.UnixNano(), 10)}, "direction": {"backward"}, "limit": {"101"},
	}
	var response struct {
		Status string `json:"status"`
		Data   *struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string   `json:"stream"`
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := queryRead(ctx, client, "http://loki:3100/loki/api/v1/query_range?"+values.Encode(), &response); err != nil {
		return logs{}, err
	}
	if response.Status != "success" || response.Data == nil || response.Data.ResultType != "streams" || response.Data.Result == nil {
		return logs{}, errQueryData
	}
	for _, stream := range response.Data.Result {
		if stream.Stream == nil || stream.Values == nil {
			return logs{}, errQueryData
		}
		for _, tuple := range stream.Values {
			entry, err := queryLine(tuple, stream.Stream)
			if err != nil {
				return logs{}, err
			}
			if entry.At.Before(result.Start) || entry.At.After(end) ||
				filter.Service != "" && entry.Service != filter.Service ||
				filter.Level != "" && queryString(entry.Level) != filter.Level ||
				filter.Text != "" && !strings.Contains(entry.Line, filter.Text) {
				continue
			}
			entry.Service = queryClean(entry.Service)
			if entry.Level != nil {
				value := queryClean(*entry.Level)
				entry.Level = &value
			}
			entry.Line = queryRedact(entry.Line)
			result.Entries = append(result.Entries, entry)
		}
	}
	slices.SortFunc(result.Entries, func(a, b line) int {
		if n := b.At.Compare(a.At); n != 0 {
			return n
		}
		if n := strings.Compare(a.Service, b.Service); n != 0 {
			return n
		}
		if n := strings.Compare(queryString(a.Level), queryString(b.Level)); n != 0 {
			return n
		}
		return strings.Compare(a.Line, b.Line)
	})
	if len(result.Entries) > 100 {
		result.More = true
		result.Entries = result.Entries[:100]
	}
	result.ExplorePath = queryExplore(result.Query, result.Start, result.End)
	return result, nil
}

func queryLine(tuple []json.RawMessage, labels map[string]string) (line, error) {
	if len(tuple) != 2 && len(tuple) != 3 {
		return line{}, errQueryData
	}
	var at, body *string
	if json.Unmarshal(tuple[0], &at) != nil || at == nil || json.Unmarshal(tuple[1], &body) != nil || body == nil {
		return line{}, errQueryData
	}
	nanos, err := strconv.ParseInt(*at, 10, 64)
	if err != nil {
		return line{}, errQueryData
	}
	service, level := labels["service_name"], labels["detected_level"]
	if len(tuple) == 3 {
		var metadata map[string]string
		if json.Unmarshal(tuple[2], &metadata) != nil || metadata == nil {
			return line{}, errQueryData
		}
		if value, ok := metadata["service_name"]; ok {
			service = value
		}
		if value, ok := metadata["detected_level"]; ok {
			level = value
		}
	}
	result := line{At: time.Unix(0, nanos).UTC(), Service: service, Line: *body}
	if level != "" {
		result.Level = &level
	}
	return result, nil
}

func queryExplore(expr string, start, end time.Time) string {
	endMillis := end.UnixMilli()
	if end.Nanosecond()%int(time.Millisecond) != 0 {
		endMillis++
	}
	datasource := map[string]string{"type": "loki", "uid": "loki"}
	panes, _ := json.Marshal(map[string]any{"a": map[string]any{
		"datasource": "loki", "queries": []any{map[string]any{"refId": "A", "datasource": datasource, "expr": expr}},
		"range": map[string]string{"from": strconv.FormatInt(start.UnixMilli(), 10), "to": strconv.FormatInt(endMillis, 10)},
	}})
	return "/explore?schemaVersion=1&panes=" + url.QueryEscape(string(panes)) + "&orgId=1"
}

func queryString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// queryEscapes removes terminal CSI, OSC, and string commands, including their
// payloads. The remaining control characters are removed by queryClean.
var queryEscapes = regexp.MustCompile(`(?s)(?:(?:\x1b\[|\x{009b})[0-?]*[ -/]*[@-~]|(?:\x1b\]|\x{009d})[^\x07\x1b\x{009c}]*(?:\x07|\x1b\\|\x{009c}|$)|(?:\x1b[P^_]|\x{0090}|\x{009e}|\x{009f}).*?(?:\x1b\\|\x{009c}|$)|\x1b[ -/]*[0-~])`)

func queryClean(value string) string {
	value = queryEscapes.ReplaceAllString(value, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

// These are the four body patterns in the managed Alloy configuration.
var queryBodyRedactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(bearer|basic) [a-z0-9._~+/=-]{8,}`), "${1} [REDACTED]"},
	{regexp.MustCompile(`(?i)((password|passwd|secret|token|api_key|apikey|access_key|private_key|authorization)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:(?:bearer|basic)\s+)?[^"'&\s,}]+)`), "${1}[REDACTED]"},
	{regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^:/@\s]+):[^@\s]+@`), "${1}:[REDACTED]@"},
	{regexp.MustCompile(`eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`), "[REDACTED_JWT]"},
}

func queryRedact(value string) string {
	value = queryClean(value)
	for _, rule := range queryBodyRedactions {
		value = rule.pattern.ReplaceAllString(value, rule.replacement)
	}
	if len(value) > 4096 {
		end := 4096
		for !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	return value
}
