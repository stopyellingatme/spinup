package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *client) logs(ctx context.Context, o Options) error {
	since := o.Since
	if since == "" {
		since = "1h"
	}
	duration, err := time.ParseDuration(since)
	if err != nil || duration <= 0 {
		return failure("query logs", fmt.Errorf("--since must be a positive duration"))
	}
	query := o.Query
	if query == "" {
		if o.Service != "" {
			query = serviceSelector(c.app, o.Service)
		} else {
			query = `{service_name=~".+"}`
		}
	}
	now := c.now()
	values := url.Values{"query": {query}, "start": {strconv.FormatInt(now.Add(-duration).UnixNano(), 10)}, "end": {strconv.FormatInt(now.UnixNano(), 10)}, "direction": {"backward"}, "limit": {"1000"}}
	data, err := c.api(ctx, "loki", "GET", "/loki/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	var response struct {
		Status string
		Data   struct {
			Result []struct {
				Stream map[string]string
				Values [][]string
			}
		}
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return failure("decode logs", err)
	}
	if response.Status != "success" {
		return failure("query logs", fmt.Errorf("the Loki query failed"))
	}
	type line struct {
		at     int64
		text   string
		labels map[string]string
	}
	var lines []line
	for _, stream := range response.Data.Result {
		for _, v := range stream.Values {
			if len(v) != 2 {
				return failure("decode logs", fmt.Errorf("invalid log entry"))
			}
			at, e := strconv.ParseInt(v[0], 10, 64)
			if e != nil {
				return failure("decode log timestamp", e)
			}
			lines = append(lines, line{at, v[1], stream.Stream})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at < lines[j].at })
	enc := json.NewEncoder(c.out)
	for _, l := range lines {
		at := time.Unix(0, l.at).UTC().Format(time.RFC3339Nano)
		if o.JSON {
			entry := logEntry{Time: at, Service: l.labels["service_name"], Level: logLevel(l.labels), Message: l.text, Attributes: l.labels}
			if err = enc.Encode(entry); err != nil {
				return failure("write logs", err)
			}
			continue
		}
		fields := []string{at}
		if service := l.labels["service_name"]; service != "" {
			fields = append(fields, service)
		}
		if level := logLevel(l.labels); level != "" {
			fields = append(fields, level)
		}
		fields = append(fields, l.text)
		fmt.Fprintln(c.out, strings.Join(append(fields, logAttributes(l.labels)...), " "))
	}
	return nil
}

// logEntry is one line of `monitor logs --json`.
type logEntry struct {
	Time       string            `json:"time"`
	Service    string            `json:"service,omitempty"`
	Level      string            `json:"level,omitempty"`
	Message    string            `json:"message"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// serviceSelector builds the stream selector for --service. The deploy pack names a service
// <app>-<service>, so the short name `api` also selects `<app>-api`.
func serviceSelector(app, service string) string {
	if app == "" || strings.HasPrefix(service, app+"-") {
		return "{service_name=" + strconv.Quote(service) + "}"
	}
	return "{service_name=~" + strconv.Quote(regexp.QuoteMeta(service)+"|"+regexp.QuoteMeta(app+"-"+service)) + "}"
}

// hiddenLogLabels are the labels that the text output does not print as attributes. They name
// the stream or repeat the level, and they make each line long. The JSON output has all labels.
var hiddenLogLabels = map[string]bool{
	"service_name": true, "service_instance_id": true, "service_version": true, "deployment_environment_name": true,
	"host_name": true, "scope_name": true, "observed_timestamp": true, "flags": true, "span_id": true,
	"detected_level": true, "severity_text": true, "severity_number": true, "level": true,
}

func logLevel(labels map[string]string) string {
	for _, key := range []string{"severity_text", "detected_level", "level"} {
		if v := labels[key]; v != "" {
			return strings.ToUpper(v)
		}
	}
	return ""
}

// logAttributes returns the attributes of a log line as sorted key=value fields. An API log
// line has its data there: the message of a request is only "HTTP request".
func logAttributes(labels map[string]string) []string {
	var keys []string
	for key := range labels {
		if !hiddenLogLabels[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	for _, key := range keys {
		value := labels[key]
		if value == "" || strings.ContainsAny(value, " \t\n\"=") {
			value = strconv.Quote(value)
		}
		fields = append(fields, key+"="+value)
	}
	return fields
}

type span struct {
	SpanID       string      `json:"spanId"`
	ParentSpanID string      `json:"parentSpanId"`
	Name         string      `json:"name"`
	Start        json.Number `json:"startTimeUnixNano"`
	End          json.Number `json:"endTimeUnixNano"`
	service      string
}

type batch struct {
	Resource struct {
		Attributes []struct {
			Key   string
			Value struct {
				StringValue string `json:"stringValue"`
			}
		}
	}
	ScopeSpans                  []struct{ Spans []span } `json:"scopeSpans"`
	InstrumentationLibrarySpans []struct{ Spans []span } `json:"instrumentationLibrarySpans"`
}

func (c *client) trace(ctx context.Context, id string) error {
	if c.profile != "full" {
		return failure("read trace", fmt.Errorf("traces require the full monitoring profile (current: %s)", c.profile))
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{32}$`).MatchString(id) {
		return failure("read trace", fmt.Errorf("the trace ID must contain 32 hexadecimal characters"))
	}
	data, err := c.api(ctx, "tempo", "GET", "/api/traces/"+id, nil)
	if err != nil {
		return err
	}
	var response struct {
		Batches       []batch
		ResourceSpans []batch `json:"resourceSpans"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return failure("decode trace", err)
	}
	var spans []span
	for _, b := range append(response.Batches, response.ResourceSpans...) {
		service := "unknown"
		for _, a := range b.Resource.Attributes {
			if a.Key == "service.name" {
				service = a.Value.StringValue
			}
		}
		for _, scope := range b.ScopeSpans {
			for _, s := range scope.Spans {
				s.service = service
				spans = append(spans, s)
			}
		}
		for _, scope := range b.InstrumentationLibrarySpans {
			for _, s := range scope.Spans {
				s.service = service
				spans = append(spans, s)
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { a, _ := spans[i].Start.Int64(); b, _ := spans[j].Start.Int64(); return a < b })
	known := map[string]bool{}
	for _, s := range spans {
		known[s.SpanID] = true
	}
	seen := map[string]bool{}
	var printSpan func(span, int) error
	printSpan = func(s span, depth int) error {
		if seen[s.SpanID] {
			return nil
		}
		seen[s.SpanID] = true
		start, e := s.Start.Int64()
		if e != nil {
			return e
		}
		end, e := s.End.Int64()
		if e != nil {
			return e
		}
		if end < start {
			return fmt.Errorf("span ends before it starts")
		}
		fmt.Fprintf(c.out, "%s%s %s %s\n", strings.Repeat("  ", depth), s.service, s.Name, time.Duration(end-start))
		for _, child := range spans {
			if child.ParentSpanID == s.SpanID {
				if e = printSpan(child, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for _, s := range spans {
		if !known[s.ParentSpanID] {
			if err = printSpan(s, 0); err != nil {
				return failure("decode span", err)
			}
		}
	}
	if len(seen) != len(spans) {
		return failure("decode trace", fmt.Errorf("trace has duplicate IDs or cyclic parents"))
	}
	return nil
}

func (c *client) silence(ctx context.Context, o Options) error {
	name, value, ok := strings.Cut(o.Matcher, "=")
	if !ok || !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`).MatchString(name) || value == "" {
		return failure("silence alert", fmt.Errorf("the matcher must be label=value"))
	}
	duration, err := time.ParseDuration(o.For)
	if err != nil || duration <= 0 {
		return failure("silence alert", fmt.Errorf("--for must be a positive duration"))
	}
	now := c.now().UTC()
	body := map[string]any{"matchers": []map[string]any{{"name": name, "value": value, "isRegex": false, "isEqual": true}}, "startsAt": now.Format(time.RFC3339Nano), "endsAt": now.Add(duration).Format(time.RFC3339Nano), "createdBy": "spinup", "comment": "Created by spinup monitor silence"}
	data, err := c.api(ctx, "grafana", "POST", "/api/alertmanager/grafana/api/v2/silences", body)
	if err != nil {
		return err
	}
	var response struct {
		SilenceID string `json:"silenceID"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return failure("decode silence", err)
	}
	if response.SilenceID == "" {
		return failure("create silence", fmt.Errorf("the Grafana response has no silence ID"))
	}
	fmt.Fprintln(c.out, response.SilenceID)
	return nil
}

func (c *client) testAlert(ctx context.Context) error {
	now := c.now().UTC()
	id := "spinup-test-" + strconv.FormatInt(now.UnixNano(), 10)
	body := []map[string]any{{"labels": map[string]string{"alertname": "SpinupTestAlert", "spinup_test_id": id, "app": c.app, "severity": "warning"}, "annotations": map[string]string{"summary": "spinup test alert " + id}, "startsAt": now.Format(time.RFC3339Nano), "endsAt": now.Add(5 * time.Minute).Format(time.RFC3339Nano)}}
	if _, err := c.api(ctx, "grafana", "POST", "/api/alertmanager/grafana/api/v2/alerts", body); err != nil {
		return err
	}
	fmt.Fprintln(c.out, id)
	return nil
}
