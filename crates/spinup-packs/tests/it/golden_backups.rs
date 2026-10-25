//! The backups pack must match the Go golden tree and keep the pgBackRest contracts (Go
//! `internal/templates/golden_backups_test.go`).
//!
//! The Go test also merges the Compose files with `docker compose config` and runs
//! `promtool test rules` when the tools are on `PATH`. R1 does not port these tool runs. The
//! Compose security lint (`composelint`) lives in `spinup-doctor`, which is not a
//! dev-dependency of `spinup-packs` (see the R1.15 notes).

use crate::golden_deploy::{parse_yaml, read, render};
use anyhow::Result;
use spinup_vfs::tree::Tree;

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

/// Checks the contracts that a golden diff does not explain: the stanza and the secret
/// loader (Go `checkBackupsTree`, without the lint and the tool runs).
fn check_backups_tree(all: &Tree, pgdata: &str) -> Result<()> {
    let override_file = read(all, "deploy/compose.backups.yml")?;
    parse_yaml::<yaml_serde::Value>(&override_file)?;
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
    Ok(())
}
