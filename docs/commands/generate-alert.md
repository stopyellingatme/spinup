---
title: generate alert — add a Prometheus alert
parent: Command guides
---

# generate alert — add a Prometheus alert

`spinup g alert <name> --expr <PromQL>` adds an alert rule to your app.
Prometheus fires the alert when the expression stays true for a set time, and Grafana sends the notification.
Use it for problems that you want to hear about, such as server errors.
The app needs the `monitoring` module.

```console
$ spinup new demo --module example.com/demo --modules deploy,monitoring --no-git --non-interactive
...
$ cd demo
$ spinup g alert HighErrors --expr 'sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m])) > 0' --for 5m --severity critical --summary 'Too many server errors'
...
Created 0, updated 1 files
Next: spinup monitor alerts
...
$ grep -o 'alert: HighErrors' monitoring/alerts/custom.yml
alert: HighErrors
```

The command adds the rule to `monitoring/alerts/custom.yml`.
This is your file: it keeps your comments and rules, and `spinup upgrade` never changes it.
Review the file and commit it.

## Flags

| Flag | What it does | Default | Example |
| --- | --- | --- | --- |
| `--expr` | The PromQL expression. Required. | none | `--expr 'up == 0'` |
| `--for` | How long the expression must stay true before the alert fires. | `5m` | `--for 1h30m` |
| `--severity` | The label `severity`: `warning` or `critical`. | `warning` | `--severity critical` |
| `--summary` | The short text in the notification. | `Alert expression is true: <expr>` | `--summary 'Service is down'` |

The alert name uses letters, digits, and underscores, such as `HighErrors`.
A name that exists already fails.

```console
$ spinup g alert Down --expr 'up == 0' --for 1h30m --severity warning --summary 'Service is down' --dry-run | head -n 1
update monitoring/alerts/custom.yml (seed)
$ spinup g alert HighErrors --expr 'up == 0'
Error: generator "alert" failed: alert "HighErrors" already exists
  Try: choose another alert name
[exit 1]
```

Write `--for` as a Prometheus duration: whole numbers with units, from large to small, such as `90s`, `5m`, or `1h30m`.

```console
$ spinup g alert Down --expr 'up == 0' --for 90x
Error: generator "alert" failed: invalid --for duration "90x": use a Prometheus duration
  Try: pass --for 5m
[exit 1]
```

## Check the expression

If `promtool` is on your `PATH`, the command checks a copy of the rules first.
A bad expression stops the command, and your file stays as it was.
After the write, the command runs `promtool check rules` again and prints the result.
Without `promtool`, the command skips the check and says so.

<!-- doctest: skip -->
```console
$ spinup g alert Bad --expr 'sum('
Error: generator "alert" failed: validate monitoring/alerts/custom.yml before writing: ...
  could not parse expression: 1:5: parse error: unclosed left parenthesis
  Try: correct --expr, --for, or the existing alert rules
```

## Load the alert

Prometheus loads every `*.yml` file in `monitoring/alerts/` when you deploy.

<!-- doctest: skip -->
```console
$ spinup deploy --env production
$ spinup monitor alerts --env production
```

`spinup monitor alerts` lists the alerts that are firing or pending.
To test the notification path, run `spinup monitor test-alert`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `--expr is required` | Pass the expression, such as `--expr 'up == 0'`. |
| `invalid severity` | Use `warning` or `critical`. |
| `the app has no monitoring pack` | Create the app with `--modules ...,monitoring`. |
| The alert never fires | Run the expression in Grafana Explore. It must return a value. |

## See also

- [generate dashboard](generate-dashboard.md): add a Grafana dashboard
- [Monitoring](../monitoring.md): the stack alerts and the notification channels
- [monitor](monitor.md): `spinup monitor alerts` and `spinup monitor test-alert`
- Flag reference: [`generate alert`](../site/commands/generate-alert.md)
