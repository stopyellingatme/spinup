//! The backups pack must match the Go golden tree and keep the pgBackRest contracts (Go
//! `internal/templates/golden_backups_test.go`).
//!
//! As in Go, the test merges the Compose files with `docker compose config` and runs
//! `promtool test rules` only when the tool is on `PATH`. It prints the reason when it skips
//! a tool.

use crate::golden_deploy::{
    lint_compose, on_path, read, render, run_tool, tool_failure, write_deploy_tree,
};
use anyhow::{Context, Result};
use spinup_exec::runner::Cmd;
use spinup_vfs::tree::Tree;
use std::path::Path;

/// The secret loader of Go `internal/backup.SecretLoader`. The Compose file and the backup
/// package must load the secrets the same way.
const SECRET_LOADER: &str = r#"for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; "#;

fn render_backups(db_version: Option<&str>) -> Result<Tree> {
    let mut value = serde_json::json!({
        "app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,backups",
    });
    if let Some(version) = db_version {
        value["db_version"] = version.into();
    }
    render(&["base", "api", "web", "deploy", "backups"], value)
}

#[test]
fn golden_backups() -> Result<()> {
    if spinup_testutil::golden::run("backups")?.is_none() {
        return Ok(());
    }
    check_backups_tree(&render_backups(None)?, "/var/lib/postgresql/18/docker")
}

#[test]
fn golden_backups_postgres_16() -> Result<()> {
    if !spinup_testutil::golden::selected("backups") {
        return Ok(());
    }
    check_backups_tree(
        &render_backups(Some("16"))?,
        "/var/lib/postgresql/data/pgdata",
    )
}

/// Checks the contracts that a golden diff does not explain: the Compose security lint, the
/// merged Compose configuration, the stanza, and the secret loader (Go `checkBackupsTree`).
fn check_backups_tree(all: &Tree, pgdata: &str) -> Result<()> {
    let override_file = read(all, "deploy/compose.backups.yml")?;
    lint_compose("deploy/compose.backups.yml", &override_file)?;
    let conf = read(all, "deploy/pgbackrest/pgbackrest.conf")?;
    for want in [
        "repo1-type=s3\n".to_owned(),
        "repo1-cipher-type=aes-256-cbc\n".into(),
        "repo1-path=/todo\n".into(),
        "repo1-retention-full=4\n".into(),
        "\n[todo]\n".into(),
        format!("pg1-path={pgdata}\n"),
        "spool-path=/var/lib/pgbackrest/spool\n".into(),
    ] {
        assert!(
            conf.contains(&want),
            "pgbackrest.conf does not contain {want:?}"
        );
    }
    // Secrets come from the environment only. The file holds no key or passphrase option.
    for forbidden in [
        "repo1-s3-key",
        "repo1-cipher-pass",
        "repo1-s3-bucket",
        "repo1-s3-endpoint",
    ] {
        assert!(
            !conf.contains(&format!("{forbidden}=")),
            "pgbackrest.conf sets the secret option {forbidden}"
        );
    }
    for want in [
        "archive_mode=on\n".to_owned(),
        "archive_command=pgbackrest --stanza=todo archive-push %p\n".into(),
        "config_file=/etc/postgresql/postgresql.conf\n".into(),
        "PGBACKREST_CONFIG: /var/lib/pgbackrest/pgbackrest.conf\n".into(),
        "- /opt/todo/backups:/var/lib/pgbackrest\n".into(),
        "name: todo-backup\n".into(),
        format!(
            r#"{}exec docker-entrypoint.sh "$$@""#,
            SECRET_LOADER.replace('$', "$$")
        ),
    ] {
        assert!(
            override_file.contains(&want),
            "compose.backups.yml does not contain {want:?}"
        );
    }
    assert!(
        !override_file.contains("ports:"),
        "compose.backups.yml publishes a port"
    );
    let (_root, dir) = write_deploy_tree(all)?;
    merged_edge(&dir)
}

/// Merges `compose.edge.yml` and `compose.backups.yml` with `docker compose config` and
/// checks the merged postgres service (Go `mergedEdge`). The command needs the docker CLI,
/// but no daemon.
fn merged_edge(dir: &Path) -> Result<()> {
    if !on_path("docker", "docker compose config") {
        return Ok(());
    }
    let output = run_tool(
        Cmd::new("docker")
            .args([
                "compose",
                "-f",
                "compose.edge.yml",
                "-f",
                "compose.backups.yml",
                "config",
                "--format",
                "json",
            ])
            .dir(dir)
            .env("DOCKER_CLI_HINTS", "false")
            .env("PUBLIC_HOST", "todo.example.com"),
    )
    .map_err(|error| anyhow::anyhow!("docker compose config: {}", tool_failure(&error)))?;
    let merged: serde_json::Value = serde_json::from_slice(&output.stdout)?;
    let pg = &merged["services"]["postgres"];
    let strings = |value: &serde_json::Value| -> Vec<String> {
        value
            .as_array()
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_owned))
                    .collect()
            })
            .unwrap_or_default()
    };
    // docker compose config writes the escaped form ($$) of a literal dollar sign.
    let entrypoint = strings(&pg["entrypoint"]);
    assert!(
        entrypoint.len() == 4
            && entrypoint[0] == "sh"
            && entrypoint[2].replace("$$", "$").starts_with(SECRET_LOADER),
        "postgres entrypoint = {entrypoint:?}"
    );
    let command = strings(&pg["command"]);
    assert!(
        command.first().map(String::as_str) == Some("postgres")
            && command
                .iter()
                .any(|arg| arg == "archive_command=pgbackrest --stanza=todo archive-push %p"),
        "postgres command = {command:?}"
    );
    let ports = pg["ports"].as_array().map_or(0, Vec::len);
    let cap_drop = strings(&pg["cap_drop"]);
    assert!(
        pg["read_only"].as_bool() == Some(true)
            && cap_drop.iter().any(|c| c == "ALL")
            && ports == 0,
        "postgres lost its hardening: read_only={} cap_drop={cap_drop:?} ports={}",
        pg["read_only"],
        pg["ports"]
    );
    for network in ["db", "backup"] {
        assert!(
            pg["networks"].get(network).is_some(),
            "postgres is not on the {network} network: {}",
            pg["networks"]
        );
    }
    let networks = &merged["networks"];
    assert!(
        networks["db"]["internal"].as_bool() == Some(true)
            && networks["backup"]["internal"].as_bool() != Some(true)
            && networks["backup"]["name"].as_str() == Some("todo-backup"),
        "networks = {networks}"
    );
    let mut mounts = std::collections::BTreeMap::new();
    for volume in pg["volumes"].as_array().into_iter().flatten() {
        mounts.insert(
            format!(
                "{}:{}",
                volume["source"].as_str().unwrap_or(""),
                volume["target"].as_str().unwrap_or("")
            ),
            volume["read_only"].as_bool().unwrap_or(false),
        );
    }
    for (mount, read_only) in [
        ("/opt/todo/backups:/var/lib/pgbackrest", false),
        ("/opt/todo/secrets:/run/secrets", true),
        ("/opt/todo/postgres:/etc/postgresql", true),
    ] {
        assert_eq!(
            mounts.get(mount),
            Some(&read_only),
            "postgres mount {mount} missing or read_only differs: {mounts:?}"
        );
    }
    let env = &pg["environment"];
    assert!(
        env["PGBACKREST_CONFIG"].as_str() == Some("/var/lib/pgbackrest/pgbackrest.conf")
            && !env["POSTGRES_PASSWORD_FILE"]
                .as_str()
                .unwrap_or("")
                .is_empty(),
        "postgres environment = {env}"
    );
    Ok(())
}

/// The BackupStale alert fires when the backup metrics are absent or stale (Go
/// `TestBackupStaleWithoutMetrics`).
#[test]
fn golden_backups_stale_without_metrics() -> Result<()> {
    if !spinup_testutil::golden::selected("backups") {
        return Ok(());
    }
    let tree = crate::golden_monitoring::render_monitoring("lite")?;
    let (root, _) = write_deploy_tree(&tree)?;
    std::fs::write(root.path().join("backup-absence.yml"), BACKUP_ABSENCE_TESTS)
        .context("write backup-absence.yml")?;
    crate::golden_monitoring::promtool(root.path(), &["test", "rules", "backup-absence.yml"])
}

const BACKUP_ABSENCE_TESTS: &str = r#"rule_files:
  - monitoring/prometheus/rules/app.yml
evaluation_interval: 1m
tests:
  - interval: 1m
    promql_expr_test:
      - expr: 'ALERTS{alertname="BackupStale",alertstate="firing"}'
        eval_time: 14m
        exp_samples: []
      - expr: 'ALERTS{alertname="BackupStale",alertstate="firing"}'
        eval_time: 15m
        exp_samples:
          - labels: 'ALERTS{alertname="BackupStale",alertstate="firing",job="backups",severity="critical"}'
            value: 1
  - interval: 1m
    input_series:
      - series: 'backup_last_success_timestamp_seconds{job="backups",kind="full"}'
        values: '1+0x20'
    promql_expr_test:
      - expr: 'ALERTS{alertname="BackupStale",alertstate="firing"}'
        eval_time: 20m
        exp_samples: []
  - interval: 1m
    input_series:
      - series: 'backup_last_success_timestamp_seconds{job="backups",kind="full"}'
        values: '1 1 stale _x20'
    promql_expr_test:
      - expr: 'ALERTS{alertname="BackupStale",alertstate="firing"}'
        eval_time: 18m
        exp_samples:
          - labels: 'ALERTS{alertname="BackupStale",alertstate="firing",job="backups",severity="critical"}'
            value: 1
  - interval: 1m
    input_series:
      - series: 'backup_last_success_timestamp_seconds{job="backups",kind="full"}'
        values: '1+0x1640'
    promql_expr_test:
      - expr: 'ALERTS{alertname="BackupStale",alertstate="firing"}'
        eval_time: 27h
        exp_samples:
          - labels: 'ALERTS{alertname="BackupStale",alertstate="firing",job="backups",severity="critical"}'
            value: 1
"#;
