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
		if a.Labels["source"] == "prometheus" || a.Labels["prometheus_alertname"] != "" {
			continue
		}
		state := a.Status.State
		switch state {
		case "active":
			state = "firing"
		case "suppressed":
			state = "silenced"
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
		state := a.State
		for _, forwarded := range active {
			if forwarded.Status.State == "suppressed" && matchesPrometheusAlert(a.Labels, forwarded.Labels) {
				state = "silenced"
				break
			}
		}
		alerts = append(alerts, alert{state, a.Labels["alertname"], a.Labels["severity"], a.Annotations["summary"]})
	}
	pending, err := c.pending(ctx)
	if err != nil {
		return err
	}
	alerts = append(alerts, pending...)
	sort.SliceStable(alerts, func(i, j int) bool {
		if alerts[i].State == "silenced" || alerts[j].State == "silenced" {
			return alerts[i].State != "silenced" && alerts[j].State == "silenced"
		}
		return alerts[i].State == "firing" && alerts[j].State != "firing"
	})
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

// matchesPrometheusAlert compares the original labels with Grafana's forwarded labels.
// Grafana replaces alertname with its rule title, replaces source, and adds its own labels.
func matchesPrometheusAlert(original, forwarded map[string]string) bool {
	if original["alertname"] == "" || forwarded["prometheus_alertname"] != original["alertname"] {
		return false
	}
	for name, value := range original {
		if name == "alertname" {
			name = "prometheus_alertname"
		}
		// The forwarding rule sets source=prometheus, and Grafana replaces a query label
		// that has the same name. The original source value is therefore not comparable.
		if name == "source" {
			continue
		}
		if actual, ok := forwarded[name]; !ok || actual != value {
			return false
		}
	}
	return true
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
