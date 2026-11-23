package monitor

import "testing"

// TestMatchesPrometheusAlertIgnoresSource checks that the source label, which the
// forwarding rule replaces with "prometheus", does not stop the silence match.
func TestMatchesPrometheusAlertIgnoresSource(t *testing.T) {
	t.Parallel()
	original := map[string]string{"alertname": "BackupStale", "source": "backup", "severity": "warning"}
	forwarded := map[string]string{"alertname": "Prometheus alert", "prometheus_alertname": "BackupStale", "source": "prometheus", "severity": "warning"}
	if !matchesPrometheusAlert(original, forwarded) {
		t.Fatal("matchesPrometheusAlert() = false for a forwarded alert with a replaced source label")
	}
	forwarded["severity"] = "critical"
	if matchesPrometheusAlert(original, forwarded) {
		t.Fatal("matchesPrometheusAlert() = true for a different severity label")
	}
}
