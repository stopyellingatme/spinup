package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type alert struct {
	State    string `json:"state"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

func (c *client) alerts(ctx context.Context, o Options) error {
	data, err := c.api(ctx, "grafana", "GET", "/api/alertmanager/grafana/api/v2/alerts", nil)
	if err != nil {
		return err
	}
	var active []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Status      struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err = json.Unmarshal(data, &active); err != nil {
		return failure("decode alerts", err)
	}
	alerts := []alert{}
	for _, a := range active {
		// Read forwarded alerts from Prometheus to preserve their names and summaries.
		if a.Status.State == "suppressed" || a.Labels["source"] == "prometheus" || a.Labels["prometheus_alertname"] != "" {
			continue
		}
		state := a.Status.State
		if state == "active" {
			state = "firing"
		}
		alerts = append(alerts, alert{state, a.Labels["alertname"], a.Labels["severity"], a.Annotations["summary"]})
	}
	data, err = c.api(ctx, "prometheus", "GET", "/api/v1/alerts", nil)
	if err != nil {
		return err
	}
	var prometheus struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				State       string            `json:"state"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err = json.Unmarshal(data, &prometheus); err != nil {
		return failure("decode Prometheus alerts", err)
	}
	if prometheus.Status != "success" {
		return failure("read Prometheus alerts", fmt.Errorf("the Prometheus request failed"))
	}
	for _, a := range prometheus.Data.Alerts {
		alerts = append(alerts, alert{a.State, a.Labels["alertname"], a.Labels["severity"], a.Annotations["summary"]})
	}
	pending, err := c.pending(ctx)
	if err != nil {
		return err
	}
	alerts = append(alerts, pending...)
	sort.SliceStable(alerts, func(i, j int) bool { return alerts[i].State == "firing" && alerts[j].State != "firing" })
	if o.JSON {
		if err = json.NewEncoder(c.out).Encode(alerts); err != nil {
			return failure("write alerts", err)
		}
		return nil
	}
	for _, a := range alerts {
		fields := []string{a.State, a.Name}
		if a.Severity != "" {
			fields = append(fields, a.Severity)
		}
		if a.Summary != "" {
			fields = append(fields, a.Summary)
		}
		if _, err = fmt.Fprintln(c.out, strings.Join(fields, " ")); err != nil {
			return failure("write alerts", err)
		}
	}
	return nil
}

func (c *client) pending(ctx context.Context) ([]alert, error) {
	data, err := c.api(ctx, "grafana", "GET", "/api/prometheus/grafana/api/v1/rules", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Status string
		Data   struct {
			Groups []struct {
				Rules []struct {
					Name, State         string
					Labels, Annotations map[string]string
				}
			}
		}
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return nil, failure("decode pending alerts", err)
	}
	if response.Status != "success" {
		return nil, failure("read pending alerts", fmt.Errorf("the Grafana request failed"))
	}
	var alerts []alert
	for _, g := range response.Data.Groups {
		for _, r := range g.Rules {
			if strings.EqualFold(r.State, "pending") && r.Labels["source"] != "prometheus" {
				alerts = append(alerts, alert{"pending", r.Name, r.Labels["severity"], r.Annotations["summary"]})
			}
		}
	}
	return alerts, nil
}
