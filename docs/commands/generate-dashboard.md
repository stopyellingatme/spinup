---
title: generate dashboard — add a Grafana dashboard
parent: Command guides
---

# generate dashboard — add a Grafana dashboard

`spinup g dashboard <name>` adds a Grafana dashboard with time-series panels to your app.
Use it for the numbers that matter to your app, such as the request rate or the queue size.
The app needs the `monitoring` module.

```console
$ spinup new demo --module example.com/demo --modules deploy,monitoring --no-git --non-interactive
...
$ cd demo
$ spinup g dashboard service-health --panel 'Request rate=sum(rate(http_server_request_duration_seconds_count[5m]))'
Created 1, updated 0 files
Next: spinup monitor dashboards sync
```

The command writes `monitoring/grafana/dashboards/custom/service-health.json`.
This is your file: `spinup upgrade` never changes it.
Review the file and commit it.

## Add panels

Each panel is a title and a PromQL expression, joined with `=`.
Pass `--panel` once for each panel.
The panels sit side by side, two in each row.

```console
$ spinup g dashboard errors --panel 'Server errors=sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m]))' --panel 'All requests=sum(rate(http_server_request_duration_seconds_count[5m]))' --dry-run
create monitoring/grafana/dashboards/custom/errors.json (seed)
```

Without `--panel`, the dashboard gets one panel, "Request rate", for the API.

```console
$ spinup g dashboard overview
Created 1, updated 0 files
Next: spinup monitor dashboards sync
$ grep '"title"' monitoring/grafana/dashboards/custom/overview.json
      "title": "Request rate",
  "title": "overview",
```

## What the file contains

The JSON is a standard Grafana dashboard:

- The dashboard `uid` is stable and starts with `spinup-`, such as `spinup-service-health`. Grafana updates the same dashboard when you deploy again.
- Each panel reads from the data source with the UID `prometheus`.
- The tags are `spinup` and `custom`.
- The time range is the last 6 hours.

## Name the dashboard

Use lower-case letters, digits, and hyphens.

```console
$ spinup g dashboard Traffic
Error: generator "dashboard" failed: invalid dashboard name "Traffic": use lower-case letters, digits, and hyphens
  Try: spinup g dashboard service-health
[exit 1]
```

If the file exists with different content, the command stops.
Pass `--force` to replace it, and `--diff` to see the change first.

## Load the dashboard

Grafana loads the files in `custom/` when you deploy.

<!-- doctest: skip -->
```console
$ spinup deploy --env production
```

To go the other way, build a dashboard in the Grafana editor and save it into `custom/`:

<!-- doctest: skip -->
```console
$ spinup monitor dashboards sync --env production
```

See [Monitoring](../monitoring.md) for the stack dashboards and the profiles.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `invalid panel "x": use title=expr` | Write the panel as `title=expression`, in quotes. |
| `the app has no monitoring pack` | Create the app with `--modules ...,monitoring`. |
| A panel shows "No data" | Run the expression in Grafana Explore. Check the metric name. |

## See also

- [generate alert](generate-alert.md): add a Prometheus alert rule
- [Monitoring](../monitoring.md)
- [monitor](monitor.md): `spinup monitor dashboards sync`
- Flag reference: [`generate dashboard`](../site/commands/generate-dashboard.md)
