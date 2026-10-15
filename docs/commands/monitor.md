# Monitor commands

The `monitor` commands operate the monitoring stack through SSH.
Each command accepts `--env` to select the environment in `spinup.toml`.
The environment needs the `monitoring` pack and the `lite` or `full` profile.
The commands use your SSH configuration and require Docker access on the VM.
Use `--accept-new-host-key` to accept a new SSH host key.

```console
$ spinup monitor --help
...
$ spinup monitor open --help
...
$ spinup monitor logs --help
...
$ spinup monitor trace --help
...
$ spinup monitor alerts --help
...
$ spinup monitor silence --help
...
$ spinup monitor test-alert --help
...
$ spinup monitor dashboards sync --help
...
```

`monitor open` prints a local URL and opens the browser.
Use `--no-browser` to open the URL yourself.
Keep the command active while you use Grafana.
Press Ctrl-C to close the SSH tunnel.
Grafana requires its own login.
The tunnel binds only to the local loopback address.

<!-- doctest: skip -->
```console
$ spinup monitor open --env production --no-browser
...
$ spinup monitor status --env production
...
$ spinup monitor logs --env production --service todo-api --since 1h
...
$ spinup monitor logs --env production --query '{service_name="todo-api"} |= "error"'
...
$ spinup monitor trace --env production 0123456789abcdef0123456789abcdef
...
```

`status` reports the profile, container health, and volume sizes.
`logs` prints up to 1000 lines with timestamps, in time order.
The default interval is one hour.
`--query` replaces the `--service` filter with a LogQL query.
The `--service` filter uses the `service_name` label.
`trace` prints a span tree with each service, span name, and duration.
Traces require the `full` profile.

<!-- doctest: skip -->
```console
$ spinup monitor alerts --env production
...
$ spinup monitor silence 'alertname=HighErrorRate' --for 2h --env production
...
$ spinup monitor test-alert --env production
...
```

`alerts` lists active alerts and pending Grafana rules.
`silence` accepts one exact matcher in the form `label=value`.
It prints the silence ID.
`test-alert` posts `SpinupTestAlert` to Grafana's Alertmanager and prints its test ID.
The alert remains active for five minutes.
Grafana routes the alert to the configured contact points.
Check the contact points to confirm delivery.
The commands read `GRAFANA_ADMIN_PASSWORD` from `/opt/<app>/secrets/` on the VM.
The password stays on the VM.

<!-- doctest: skip -->
```console
$ spinup monitor dashboards sync --env production
...
```

`dashboards sync` exports dashboards from the Grafana UI into `monitoring/grafana/dashboards/custom/<uid>.json`.
It removes the database ID and sorts the JSON keys.
It leaves dashboards provisioned from `stack/` unchanged.
It skips provisioned dashboards when Grafana does not report their source.
It rejects symlinks in the destination path.
Review the exported files and commit them to git.
The command never writes into `stack/`.

After each release, the deploy hook posts a Grafana annotation with the release ID, git SHA, and user.
The hook skips Grafana when its monitoring container is absent or stopped.
The deployer reports a hook failure as a warning.
