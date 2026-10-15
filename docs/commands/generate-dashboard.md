# Generate dashboards and alerts

Use `spinup g dashboard` to create Grafana dashboards.
Use `spinup g alert` to add Prometheus alert rules.
The app must include the monitoring pack.

The generators write only user-owned files.
`spinup upgrade` does not change these files.

## Dashboard

```console
$ spinup new demo --module example.com/demo --modules deploy,monitoring --no-git --non-interactive
...
$ cd demo
$ spinup g dashboard service-health --panel 'Request rate=sum(rate(http_server_request_duration_seconds_count[5m]))'
Created 1, updated 0 files
Next: spinup monitor dashboards sync
$ spinup g dashboard Traffic
Error: generator "dashboard" failed: invalid dashboard name "Traffic": use lower-case letters, digits, and hyphens
  Try: spinup g dashboard service-health
[exit 1]
```

The generator writes `monitoring/grafana/dashboards/custom/<name>.json`.
The dashboard UID is stable and starts with `spinup-`.
It uses the fixed `prometheus` datasource UID.

Pass `--panel "title=expr"` more than once to add time-series panels.
Without a panel, the generator adds an app request-rate panel.

## Alert

```console
$ spinup g alert HighErrors --expr 'sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m])) > 0' --for 5m --severity critical --summary 'Too many server errors'
...
Created 0, updated 1 files
Next: spinup monitor alerts
$ spinup g alert HighErrors --expr 'up == 0'
Error: generator "alert" failed: alert "HighErrors" already exists
  Try: choose another alert name
[exit 1]
```

The generator adds a rule to `monitoring/alerts/custom.yml`.
When `promtool` is on `PATH`, the generator checks the file with `promtool check rules` and prints its output first.
It keeps comments and existing rules.
Use `--severity warning` or `--severity critical`.
The default duration is `5m`.

When `promtool` is available, the generator runs `promtool check rules` after it writes the rule.
When it is unavailable, the generator skips validation and prints a note.
