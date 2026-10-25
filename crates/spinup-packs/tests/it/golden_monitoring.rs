//! The monitoring pack must match the Go golden trees and keep the contracts of ADR 0010 §2
//! (Go `internal/templates/golden_monitoring_test.go`).
//!
//! As in Go, the tests run `promtool` and `docker compose config` only when the tool is on
//! `PATH`, and print the reason when they skip a tool. The Grafana entrypoint test always
//! runs the script with `sh`. The pack keeps `go-runtime.json` unchanged.

use crate::golden_deploy::{
    compose_config, lint_compose, on_path, parse_yaml, read, render, run_tool, select,
    tool_failure, write_tree,
};
use anyhow::{Context, Result, bail};
use spinup_exec::runner::Cmd;
use spinup_vfs::tree::Tree;
use std::collections::BTreeMap;
use std::path::Path;
use std::time::Duration;

/// Runs `promtool` in `dir` when it is on `PATH` (Go `promtool`).
pub(crate) fn promtool(dir: &Path, args: &[&str]) -> Result<()> {
    if !on_path("promtool", &format!("promtool {}", args[..2].join(" "))) {
        return Ok(());
    }
    if let Err(error) = run_tool(Cmd::new("promtool").args(args.iter().copied()).dir(dir)) {
        bail!("promtool {}: {}", args.join(" "), tool_failure(&error));
    }
    Ok(())
}

/// Renders the monitoring pack with a profile and keeps the files under `monitoring/`.
pub(crate) fn render_monitoring(profile: &str) -> Result<Tree> {
    let all = render(
        &["base", "api", "web", "deploy", "monitoring"],
        serde_json::json!({
            "app_name": "todo", "module": "example.com/todo",
            "modules": "ci,deploy,monitoring", "monitoring_profile": profile,
        }),
    )?;
    select(&all, |path| path.starts_with("monitoring/"))
}

#[test]
fn golden_monitoring() -> Result<()> {
    for profile in ["lite", "full"] {
        let Some(tree) = spinup_testutil::golden::run(&format!("monitoring/{profile}"))? else {
            return Ok(());
        };
        check_monitoring_tree(&tree, profile == "full")
            .with_context(|| format!("profile {profile}"))?;
    }
    Ok(())
}

#[test]
fn golden_monitoring_none() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    let tree = render_monitoring("none")?;
    let paths = tree.iter().map(|(path, _)| path).collect::<Vec<_>>();
    assert!(paths.is_empty(), "profile none renders {paths:?}");
    Ok(())
}

#[test]
fn golden_monitoring_spinup_toml() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    let all = render(
        &["base", "api", "deploy", "monitoring"],
        serde_json::json!({"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,monitoring"}),
    )?;
    // spinup.toml repeats the profile answer, and the question default is lite.
    let config = read(&all, "spinup.toml")?;
    assert!(
        config.contains(
            "[monitoring]\n# Pick the monitoring stack: lite, full, or none.\nprofile = \"lite\"\n"
        ),
        "spinup.toml does not select the lite profile:\n{config}"
    );
    assert!(
        all.contains("monitoring/compose.monitoring.yml"),
        "the default profile renders no compose.monitoring.yml"
    );
    Ok(())
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct Service {
    image: String,
    ports: Option<Vec<String>>,
    read_only: bool,
    cap_drop: Vec<String>,
    cap_add: Vec<String>,
    security_opt: Vec<String>,
    healthcheck: Option<yaml_serde::Value>,
    logging: Option<yaml_serde::Value>,
    networks: Option<yaml_serde::Value>,
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct Network {
    name: String,
    external: bool,
    internal: bool,
}

#[derive(serde::Deserialize)]
struct Compose {
    name: String,
    services: BTreeMap<String, Service>,
    #[serde(default)]
    networks: BTreeMap<String, Option<Network>>,
}

/// Reports whether a Compose `networks` value (a list or a mapping) names the network.
fn on_network(networks: Option<&yaml_serde::Value>, name: &str) -> bool {
    match networks {
        Some(yaml_serde::Value::Sequence(items)) => {
            items.iter().any(|item| item.as_str() == Some(name))
        }
        Some(yaml_serde::Value::Mapping(map)) => map.contains_key(name),
        _ => false,
    }
}

/// Checks the contracts of ADR 0010 §2 that a golden diff does not explain: the Compose
/// security lint, the pins, the profile services, the datasource UIDs, the rule files, and
/// the dashboard JSON (Go `checkMonitoringTree`).
fn check_monitoring_tree(tree: &Tree, full: bool) -> Result<()> {
    const COMPOSE_FILE: &str = "monitoring/compose.monitoring.yml";
    lint_compose(COMPOSE_FILE, &read(tree, COMPOSE_FILE)?)?;
    let parsed: Compose =
        parse_yaml(&read(tree, COMPOSE_FILE)?).with_context(|| format!("parse {COMPOSE_FILE}"))?;
    assert_eq!(parsed.name, "todo-monitoring", "project name");
    let mut want = vec!["alloy", "grafana", "loki", "prometheus"];
    if full {
        want.extend([
            "blackbox-exporter",
            "cadvisor",
            "node-exporter",
            "postgres-exporter",
            "tempo",
        ]);
    }
    want.sort_unstable();
    let got = parsed
        .services
        .keys()
        .map(String::as_str)
        .collect::<Vec<_>>();
    assert_eq!(got, want, "services");

    let stack = spinup_stack::load::embedded();
    for (name, service) in &parsed.services {
        let image = stack
            .image(name)
            .with_context(|| format!("service {name} has no pinned image"))?;
        assert_eq!(service.image, image.reference(), "service {name} image");
        assert!(
            service.read_only
                && service.cap_drop == ["ALL"]
                && service.cap_add.is_empty()
                && service
                    .security_opt
                    .iter()
                    .any(|opt| opt == "no-new-privileges:true")
                && service.logging.is_some(),
            "service {name} lost its hardening: read_only={} cap_drop={:?} cap_add={:?} security_opt={:?}",
            service.read_only,
            service.cap_drop,
            service.cap_add,
            service.security_opt
        );
        // The Loki image has no shell and no HTTP client (see the comment in the file).
        assert!(
            service.healthcheck.is_some() || name == "loki",
            "service {name} has no health check"
        );
        let want_ports = (name == "grafana").then(|| vec!["127.0.0.1:3000:3000".to_owned()]);
        assert_eq!(service.ports, want_ports, "service {name} ports");
    }
    assert!(
        on_network(parsed.services["alloy"].networks.as_ref(), "app"),
        "alloy is not on the app network: the app cannot reach alloy:4318"
    );
    let network = |name: &str| parsed.networks.get(name).map(|n| n.as_ref());
    match network("app") {
        Some(Some(n)) if n.name == "todo-app" && n.external => {}
        _ => bail!("the app network is not the external todo-app"),
    }
    match network("monitoring") {
        Some(Some(n)) if n.internal => {}
        _ => bail!("the monitoring network is not internal"),
    }
    match (network("db"), full) {
        (Some(Some(n)), true) if n.name == "todo-db" && n.external => {}
        (None, false) => {}
        _ => bail!("want the external todo-db network only in profile full"),
    }

    let alloy = read(tree, "monitoring/alloy/config.alloy")?;
    for needle in [
        r#"endpoint = "0.0.0.0:4317""#,
        r#"endpoint = "0.0.0.0:4318""#,
        r#"otelcol.processor.transform "redact""#,
        "delete_matching_keys(",
        "[REDACTED]",
        "http://prometheus:9090/api/v1/write",
        "http://loki:3100/otlp",
    ] {
        assert!(
            alloy.contains(needle),
            "config.alloy does not contain {needle:?}"
        );
    }
    assert_eq!(
        alloy.contains("http://tempo:4318"),
        full,
        "config.alloy exports to Tempo"
    );

    #[derive(serde::Deserialize)]
    struct Datasource {
        uid: String,
        #[serde(rename = "type")]
        kind: String,
    }
    #[derive(serde::Deserialize)]
    struct Datasources {
        datasources: Vec<Datasource>,
    }
    let datasources: Datasources = parse_yaml(&read(
        tree,
        "monitoring/grafana/provisioning/datasources/datasources.yml",
    )?)?;
    for ds in &datasources.datasources {
        assert_eq!(ds.uid, ds.kind, "datasource {} UID", ds.kind);
    }
    let uids = datasources
        .datasources
        .iter()
        .map(|ds| ds.uid.as_str())
        .collect::<Vec<_>>();
    let mut want_uids = vec!["prometheus", "loki"];
    if full {
        want_uids.push("tempo");
    }
    assert_eq!(uids, want_uids, "datasource UIDs");

    // Every dashboard is valid JSON, and spinup's dashboards use the fixed datasource UIDs.
    let mut rule_files = Vec::new();
    let mut dashboards = Vec::new();
    for (name, _) in tree.iter() {
        if name.starts_with("monitoring/grafana/dashboards/") && name.ends_with(".json") {
            dashboards.push(name);
        } else if name.starts_with("monitoring/prometheus/rules/")
            || name.starts_with("monitoring/alerts/")
        {
            rule_files.push(name);
        }
    }
    // The vendored dashboards need the full exporters, so only the full profile renders them.
    let min_dashboards = if full { 8 } else { 5 };
    assert!(
        dashboards.len() >= min_dashboards,
        "dashboards = {dashboards:?}, want spinup's five (plus the three vendored ones in full)"
    );
    for name in &dashboards {
        let data = read(tree, name)?;
        let dashboard: serde_json::Value =
            serde_json::from_str(&data).with_context(|| format!("{name} is not valid JSON"))?;
        let text = |key: &str| dashboard.get(key).and_then(|v| v.as_str()).unwrap_or("");
        let panels = dashboard
            .get("panels")
            .and_then(|v| v.as_array())
            .map_or(0, Vec::len);
        assert!(
            !text("uid").is_empty() && !text("title").is_empty() && panels > 0,
            "{name} has no uid, title, or panels"
        );
        assert!(
            !data.contains("${DS_"),
            "{name} uses a datasource input; use the provisioned UID"
        );
    }
    let mut want_rules = vec![
        "monitoring/alerts/custom.yml",
        "monitoring/prometheus/rules/app.yml",
        "monitoring/prometheus/rules/stack.yml",
    ];
    if full {
        want_rules.extend([
            "monitoring/prometheus/rules/certs.yml",
            "monitoring/prometheus/rules/host.yml",
            "monitoring/prometheus/rules/postgres.yml",
        ]);
    }
    want_rules.sort_unstable();
    rule_files.sort_unstable();
    assert_eq!(rule_files, want_rules, "rule files");

    let root = tempfile::tempdir()?;
    write_tree(tree, root.path())?;
    let mut args = vec!["check", "rules"];
    args.extend(rule_files.iter().copied());
    promtool(root.path(), &args)?;
    promtool(
        root.path(),
        &["check", "config", "monitoring/prometheus/prometheus.yml"],
    )?;
    compose_config(
        &root.path().join("monitoring"),
        "compose.monitoring.yml",
        &[("PUBLIC_URL", "https://todo.example.com")],
    )
}

/// Decodes a Go interpreted string literal with the escapes that the alloy config uses.
fn unquote(literal: &str) -> Result<String> {
    let inner = literal
        .strip_prefix('"')
        .and_then(|s| s.strip_suffix('"'))
        .context("not a quoted string")?;
    let mut out = String::new();
    let mut chars = inner.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('\\') => out.push('\\'),
            Some('"') => out.push('"'),
            Some('\'') => out.push('\''),
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            other => bail!("unsupported escape \\{other:?} in {literal}"),
        }
    }
    Ok(out)
}

/// Applies the rendered OTTL regexes in their export order (Go `TestMonitoringBodyRedaction`).
#[test]
fn golden_monitoring_body_redaction() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    let alloy = read(&render_monitoring("lite")?, "monitoring/alloy/config.alloy")?;
    // OTTL, Go, and the regex crate use the same RE2 regex syntax.
    let statement =
        regex::Regex::new(r#"replace_pattern\(body, ("(?:\\.|[^"\\])*"), ("(?:\\.|[^"\\])*")\)"#)?;
    let mut replacements = Vec::new();
    for captures in statement.captures_iter(&alloy) {
        replacements.push((
            regex::Regex::new(&unquote(&captures[1])?)?,
            unquote(&captures[2])?,
        ));
    }
    assert!(!replacements.is_empty(), "no log-body replacements found");
    for (name, body, secret) in [
        (
            "header",
            "request failed Authorization: Bearer abcdef123456.xyz",
            "abcdef123456.xyz",
        ),
        (
            "logfmt",
            r#"authorization="Bearer abcdef123456xyz""#,
            "abcdef123456xyz",
        ),
        ("basic", "authorization=Basic dXNlcjpwYXNz", "dXNlcjpwYXNz"),
        ("short credential", "Authorization: Bearer short", "short"),
        (
            "JSON spaces",
            r#"{"password":"hunter2 and more","message":"keep me"}"#,
            "hunter2 and more",
        ),
        (
            "single quotes",
            "password='hunter2 and more' message=keep",
            "hunter2 and more",
        ),
        (
            "escaped quote",
            r#"{"password":"hunter2 \" and more"}"#,
            "and more",
        ),
        ("ordinary text", "request completed successfully", ""),
    ] {
        let mut got = body.to_owned();
        for (pattern, replacement) in &replacements {
            got = pattern.replace_all(&got, replacement.as_str()).into_owned();
        }
        if secret.is_empty() {
            assert_eq!(got, body, "{name}: ordinary text changed");
            continue;
        }
        for word in secret.split_whitespace() {
            assert!(!got.contains(word), "{name}: secret remains in {got:?}");
        }
        assert!(
            got.contains("[REDACTED]"),
            "{name}: no redaction in {got:?}"
        );
        if body.contains("keep me") {
            assert!(
                got.contains("keep me"),
                "{name}: nonsecret field removed: {got:?}"
            );
        }
    }
    Ok(())
}

/// Go `TestMonitoringDashboardCoverage`.
#[test]
fn golden_monitoring_dashboard_coverage() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    for profile in ["lite", "full"] {
        let tree = render_monitoring(profile)?;
        let runtime = read(&tree, "monitoring/grafana/dashboards/stack/go-runtime.json")?;
        assert!(
            !runtime.contains("process_cpu_seconds_total"),
            "{profile}: the runtime dashboard queries a CPU metric the API does not produce"
        );
        let backups = read(&tree, "monitoring/grafana/dashboards/stack/backups.json")?;
        assert_eq!(
            backups.contains("pg_stat_archiver_"),
            profile == "full",
            "{profile}: WAL panels present"
        );
        assert!(
            backups.contains("backup_last_age_seconds"),
            "{profile}: backup age panel missing"
        );
        let app = read(&tree, "monitoring/prometheus/rules/app.yml")?;
        assert!(
            !app.contains("<app>-web") && app.contains("API"),
            "{profile}: HTTP RED must describe API coverage"
        );
    }
    Ok(())
}

/// Go `TestMonitoringAlloyJob`: the self exporter supplies job=integrations/self, which
/// overrides job_name.
#[test]
fn golden_monitoring_alloy_job() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    let alloy = read(&render_monitoring("lite")?, "monitoring/alloy/config.alloy")?;
    assert!(
        alloy.contains(r#"replacement = "alloy""#) && alloy.contains(r#"target_label = "job""#),
        "the self scrape must replace the exporter job label with alloy"
    );
    Ok(())
}

/// A healthy stack stays quiet. Missing and stale data fire the absence alerts (Go
/// `TestMonitoringAbsenceAlerts`).
#[test]
fn golden_monitoring_absence_alerts() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    for profile in ["lite", "full"] {
        let root = tempfile::tempdir()?;
        write_tree(&render_monitoring(profile)?, root.path())?;
        std::fs::write(root.path().join("absence.yml"), ABSENCE_TESTS)?;
        promtool(root.path(), &["test", "rules", "absence.yml"])
            .with_context(|| format!("profile {profile}"))?;
    }
    Ok(())
}

const ABSENCE_TESTS: &str = r#"rule_files:
  - monitoring/prometheus/rules/stack.yml
  - monitoring/prometheus/rules/app.yml
evaluation_interval: 1m
tests:
  - interval: 1m
    input_series:
      - series: 'up{job="alloy",instance="self"}'
        values: '1+0x20'
      - series: 'http_server_request_duration_seconds_count{job="todo-api"}'
        values: '0+1x20'
    alert_rule_test:
      - eval_time: 15m
        alertname: AlloyMissing
        exp_alerts: []
      - eval_time: 15m
        alertname: APIRequestMetricsMissing
        exp_alerts: []
  - interval: 1m
    input_series:
      - series: 'up{job="alloy",instance="self"}'
        values: '1 1 stale _x15'
      - series: 'http_server_request_duration_seconds_count{job="todo-api"}'
        values: '1 2 stale _x15'
    promql_expr_test:
      - expr: 'ALERTS{alertname="AlloyMissing",alertstate="firing"}'
        eval_time: 4m
        exp_samples: []
      - expr: 'ALERTS{alertname="AlloyMissing",alertstate="firing"}'
        eval_time: 8m
        exp_samples:
          - labels: 'ALERTS{alertname="AlloyMissing",alertstate="firing",job="alloy",severity="critical"}'
            value: 1
      - expr: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing"}'
        eval_time: 13m
        exp_samples:
          - labels: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing",severity="warning"}'
            value: 1
  - interval: 1m
    promql_expr_test:
      - expr: 'ALERTS{alertname=~"AlloyMissing|APIRequestMetricsMissing",alertstate="firing"}'
        eval_time: 15m
        exp_samples:
          - labels: 'ALERTS{alertname="AlloyMissing",alertstate="firing",job="alloy",severity="critical"}'
            value: 1
          - labels: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing",severity="warning"}'
            value: 1
"#;

const FAKE_WGET: &str = r#"#!/bin/sh
if [ -f "$TEST_ROOT/started" ]; then
 touch "$TEST_ROOT/healthy"
 exit 0
fi
exit 1
"#;

const FAKE_GRAFANA: &str = r#"#!/bin/sh
for arg do
 if [ "$arg" = --help ]; then
  if [ "$SUPPORTED" = true ]; then echo --password-from-stdin; fi
  exit 0
 fi
done
if [ ! -f "$TEST_ROOT/healthy" ]; then exit 2; fi
printf '%s\n' "$@" > "$TEST_ROOT/args"
IFS= read -r password
printf '%s' "$password" > "$TEST_ROOT/password"
if [ "$RESET_FAILS" = true ]; then exit 1; fi
touch "$TEST_ROOT/reset"
"#;

const FAKE_SERVER: &str = r#"#!/bin/sh
trap 'exit 0' TERM INT
touch "$TEST_ROOT/started"
# A reset failure must stop the server. The outer test timeout catches a missing stop.
if [ "$RESET_FAILS" = true ]; then
 while :; do sleep 0.1; done
fi
if [ "$SUPPORTED" = true ]; then
 while [ ! -f "$TEST_ROOT/reset" ]; do sleep 0.1; done
fi
exit 0
"#;

/// Writes a file with mode 0700 and closes it before a later run (Go `WriteExecutable`).
fn write_executable(path: &Path, data: &str) -> Result<()> {
    std::fs::create_dir_all(path.parent().context("a file path has no parent")?)?;
    let file = std::fs::File::create(path)?;
    std::io::Write::write_all(&mut &file, data.as_bytes())?;
    file.sync_all()?;
    drop(file);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

/// The Grafana entrypoint rotates the admin password through stdin, or prints the manual
/// reset guidance (Go `TestMonitoringGrafanaPassword`).
#[test]
#[cfg_attr(
    windows,
    ignore = "runs a POSIX sh entrypoint; Go excludes templates on Windows (scripts/ci/windows-excluded.txt)"
)]
fn golden_monitoring_grafana_password() -> Result<()> {
    if !spinup_testutil::golden::selected("monitoring") {
        return Ok(());
    }
    const PASSWORD: &str = "new ' \" $(touch injected) `touch injected` ; $HOME \\\\ password";
    let entrypoint = read(
        &render_monitoring("lite")?,
        "monitoring/grafana/entrypoint.sh",
    )?;
    for (name, supported, reset_fails) in [
        ("rotate", true, false),
        ("reset failure", true, true),
        ("manual fallback", false, false),
    ] {
        let dir = tempfile::tempdir()?;
        let root = dir.path();
        let path_of = |name: &str| root.join(name).to_string_lossy().into_owned();
        for (file, data) in [
            ("secrets/GRAFANA_ADMIN_PASSWORD", PASSWORD),
            ("source/.keep", ""),
            ("bin/wget", FAKE_WGET),
            ("bin/grafana", FAKE_GRAFANA),
            ("server.sh", FAKE_SERVER),
        ] {
            write_executable(&root.join(file), data)?;
        }
        let script = entrypoint
            .replace("/run/secrets", &path_of("secrets"))
            .replace("/etc/spinup/provisioning", &path_of("source"))
            .replace("/run.sh", &path_of("server.sh"));
        let script_path = root.join("entrypoint.sh");
        write_executable(&script_path, &script)?;
        let path = format!(
            "{}:{}",
            path_of("bin"),
            std::env::var("PATH").unwrap_or_default()
        );
        let result = run_tool(Cmd {
            timeout: Some(Duration::from_secs(5)),
            ..Cmd::new("sh")
                .args([script_path.to_string_lossy().into_owned()])
                .dir(root)
                .env("TEST_ROOT", root.to_string_lossy().into_owned())
                .env("PATH", path)
                .env("GF_PATHS_PROVISIONING", path_of("provisioning"))
                .env("SUPPORTED", supported.to_string())
                .env("RESET_FAILS", reset_fails.to_string())
        });
        let (stdout, stderr) = match &result {
            Ok(output) => (output.stdout.clone(), output.stderr.clone()),
            Err(error) => error
                .output()
                .map(|o| (o.stdout.clone(), o.stderr.clone()))
                .unwrap_or_default(),
        };
        let stdout = String::from_utf8_lossy(&stdout).into_owned();
        let stderr = String::from_utf8_lossy(&stderr).into_owned();
        if reset_fails {
            assert!(
                result.is_err(),
                "{name}: Grafana started despite a failed password reset"
            );
            assert!(
                stderr.contains("Try:"),
                "{name}: the reset failure has no guidance: {stderr}"
            );
        } else if let Err(error) = &result {
            bail!("{name}: start Grafana: {}", tool_failure(error));
        }
        if supported {
            let got = std::fs::read_to_string(root.join("password"))?;
            assert_eq!(
                got, PASSWORD,
                "{name}: the password changed in the stdin transfer"
            );
            let args = std::fs::read_to_string(root.join("args"))?;
            assert!(
                args.contains("--password-from-stdin") && !args.contains(PASSWORD),
                "{name}: the password must enter the CLI through stdin: {args}"
            );
            assert!(
                args.contains("cfg:default.paths.data="),
                "{name}: the CLI must use the server database: {args}"
            );
        } else {
            assert!(
                stderr.contains("manual reset"),
                "{name}: an unsupported CLI needs manual reset guidance: {stderr}"
            );
        }
        assert!(
            !root.join("injected").exists(),
            "{name}: the password ran as shell code"
        );
        assert!(
            !format!("{stdout}{stderr}").contains(PASSWORD),
            "{name}: the password leaked in the startup output"
        );
    }
    Ok(())
}
