//! The deploy pack must match the Go golden tree and keep the contracts of ADR 0003 and
//! ADR 0009 §6 (Go `internal/templates/golden_deploy_test.go`).
//!
//! The Go test also runs `docker compose config` and `caddy adapt` when the tools are on
//! `PATH`. R1 does not port these tool runs. The Compose security lint (`composelint`) lives
//! in `spinup-doctor`, which is not a dev-dependency of `spinup-packs` (see the R1.15 notes).

use anyhow::{Context, Result};
use spinup_vfs::tree::Tree;

/// The Compose files that the deploy pack renders.
const DEPLOY_COMPOSE_FILES: [&str; 3] = [
    "deploy/compose.yml",
    "deploy/compose.override.yml",
    "deploy/compose.edge.yml",
];

/// Selects the files of the deploy pack (Go `deployGoldenPath`). The api and web packs own
/// the other files under `deploy/`.
fn deploy_golden_path(name: &str) -> bool {
    !matches!(
        name,
        "deploy/compose.dev.yml" | "deploy/Dockerfile.api" | "deploy/Dockerfile.web"
    ) && name.starts_with("deploy/")
}

/// Converts a JSON object literal to the answers map of the renderer.
pub(crate) fn answers(value: serde_json::Value) -> serde_json::Map<String, serde_json::Value> {
    value.as_object().cloned().unwrap_or_default()
}

/// Renders the packs and returns the whole tree.
pub(crate) fn render(packs: &[&str], value: serde_json::Value) -> Result<Tree> {
    let packs = packs.iter().map(|p| (*p).to_owned()).collect::<Vec<_>>();
    spinup_testutil::golden::render_pack(&packs, &answers(value))
}

/// Keeps the files that `keep` selects.
pub(crate) fn select(all: &Tree, keep: impl Fn(&str) -> bool) -> Result<Tree> {
    let mut selected = Tree::new();
    for (path, file) in all.iter().filter(|(path, _)| keep(path)) {
        selected.insert(path, file.data.to_vec(), file.mode)?;
    }
    Ok(selected)
}

/// Decodes YAML and applies the merge keys (`<<: *anchor`), as Go `yaml.v3` does.
pub(crate) fn parse_yaml<T: serde::de::DeserializeOwned>(data: &str) -> Result<T> {
    let mut value: yaml_serde::Value = yaml_serde::from_str(data)?;
    value.apply_merge()?;
    Ok(yaml_serde::from_value(value)?)
}

/// Reads a rendered text file, or fails with the missing path.
pub(crate) fn read(tree: &Tree, name: &str) -> Result<String> {
    let file = tree
        .get(name)
        .with_context(|| format!("the render has no {name}"))?;
    Ok(String::from_utf8(file.data.to_vec())?)
}

fn render_deploy(packs: &[&str], value: serde_json::Value) -> Result<Tree> {
    select(&render(packs, value)?, deploy_golden_path)
}

fn base_answers() -> serde_json::Value {
    serde_json::json!({"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy"})
}

#[test]
fn golden_deploy() -> Result<()> {
    let Some(tree) = spinup_testutil::golden::run("deploy")? else {
        return Ok(());
    };
    check_deploy_tree(&tree, true)
}

#[test]
fn golden_deploy_api_only() -> Result<()> {
    if !spinup_testutil::golden::selected("deploy") {
        return Ok(());
    }
    check_deploy_tree(
        &render_deploy(&["base", "api", "deploy"], base_answers())?,
        false,
    )
}

#[test]
fn golden_deploy_postgres_versions() -> Result<()> {
    if !spinup_testutil::golden::selected("deploy") {
        return Ok(());
    }
    let stack = spinup_stack::load::embedded();
    for version in ["17", "16"] {
        let mut value = base_answers();
        value["db_version"] = version.into();
        let tree = render_deploy(&["base", "api", "web", "deploy"], value)?;
        let image = stack.image(&format!("postgres-{version}"))?;
        let want = [
            (
                "deploy/Dockerfile.postgres",
                vec![format!("FROM {}\n", image.reference())],
            ),
            (
                "deploy/compose.edge.yml",
                vec![
                    "postgres_data:/var/lib/postgresql/data\n".to_owned(),
                    "PGDATA: /var/lib/postgresql/data/pgdata".to_owned(),
                    format!("todo-postgres:{}-pgbackrest", image.tag),
                ],
            ),
        ];
        for (name, needles) in want {
            let data = read(&tree, name)?;
            for needle in needles {
                assert!(
                    data.contains(&needle),
                    "postgres {version}: {name} does not contain {needle:?}"
                );
            }
        }
        check_deploy_tree(&tree, true)?;
    }
    Ok(())
}

#[test]
fn golden_deploy_monitoring() -> Result<()> {
    if !spinup_testutil::golden::selected("deploy") {
        return Ok(());
    }
    for profile in ["lite", "full", "none"] {
        let tree = render_deploy(
            &["base", "api", "web", "deploy", "monitoring"],
            serde_json::json!({
                "app_name": "todo", "module": "example.com/todo",
                "modules": "ci,deploy,monitoring", "monitoring_profile": profile,
            }),
        )?;
        let compose = read(&tree, "deploy/compose.yml")?;
        let want = if profile == "none" { 0 } else { 2 };
        assert_eq!(
            compose
                .matches("OTEL_EXPORTER_OTLP_ENDPOINT: http://alloy:4318")
                .count(),
            want,
            "{profile}: OTLP endpoints"
        );
        assert_eq!(
            compose
                .matches("OTEL_RESOURCE_ATTRIBUTES: service.instance.id=${SPINUP_COLOR}")
                .count(),
            want,
            "{profile}: color resource attributes"
        );
        for service in ["api", "web"] {
            assert_eq!(
                compose.contains(&format!("OTEL_SERVICE_NAME: todo-{service}")),
                profile != "none",
                "{profile}: service name {service}"
            );
        }
        check_deploy_tree(&tree, true)?;
    }
    Ok(())
}

#[test]
fn golden_deploy_ignores_unselected_monitoring() -> Result<()> {
    if !spinup_testutil::golden::selected("deploy") {
        return Ok(());
    }
    let mut value = base_answers();
    value["monitoring_profile"] = "full".into();
    let tree = render_deploy(&["base", "api", "web", "deploy"], value)?;
    let compose = read(&tree, "deploy/compose.yml")?;
    assert!(
        !compose.contains("OTEL_"),
        "unselected monitoring sets the environment:\n{compose}"
    );
    Ok(())
}

/// Checks the contracts that a golden diff does not explain: the blue/green aliases, the
/// routes, and the pins (Go `checkDeployTree`, without the tool runs).
fn check_deploy_tree(tree: &Tree, web: bool) -> Result<()> {
    for name in DEPLOY_COMPOSE_FILES {
        let data = read(tree, name)?;
        parse_yaml::<yaml_serde::Value>(&data)
            .with_context(|| format!("{name} is not valid YAML"))?;
    }
    let compose = read(tree, "deploy/compose.yml")?;
    let edge = read(tree, "deploy/compose.edge.yml")?;
    let caddyfile = read(tree, "deploy/Caddyfile")?;
    let stack = spinup_stack::load::embedded();
    let caddy = stack.image("caddy")?;
    let pgbackrest = stack
        .apt
        .get("pgbackrest")
        .context("the stack has no apt pin for pgbackrest")?;

    let mut contains: Vec<(&str, Vec<String>)> = vec![
        (
            "deploy/compose.yml",
            [
                "name: todo-${SPINUP_COLOR:?",
                "include:\n  - compose.override.yml\n",
                "- api-${SPINUP_COLOR}\n",
                "SPINUP_ENV: production\n",
                "PUBLIC_ORIGIN: ${PUBLIC_ORIGIN:?",
                "DATABASE_URL_FILE: /run/secrets/DATABASE_URL\n",
                "- /opt/todo/secrets:/run/secrets:ro\n",
                "profiles: [migrate]\n",
                r#"command: ["migrate", "up"]"#,
                "name: todo-app\n    external: true\n",
                "name: todo-db\n    external: true\n",
            ]
            .map(String::from)
            .to_vec(),
        ),
        (
            "deploy/compose.edge.yml",
            vec![
                "name: todo\n".into(),
                format!("image: {}\n", caddy.reference()),
                "spinup.public: \"true\"\n".into(),
                "- /opt/todo/caddy:/etc/caddy:ro\n".into(),
                "POSTGRES_PASSWORD_FILE: /run/secrets/POSTGRES_PASSWORD\n".into(),
                "name: todo-db\n    internal: true\n".into(),
            ],
        ),
        (
            "deploy/Caddyfile",
            [
                "admin localhost:2019\n",
                "grace_period 10s\n",
                "{$PUBLIC_HOST} {\n",
                "encode zstd gzip\n",
                "import Caddyfile.d/*.caddy\n",
                "import upstream.caddy api 8080\n",
            ]
            .map(String::from)
            .to_vec(),
        ),
        (
            "deploy/Dockerfile.postgres",
            vec![
                format!("ARG PGBACKREST_VERSION={pgbackrest}\n"),
                r#""pgbackrest=${PGBACKREST_VERSION}-*""#.into(),
            ],
        ),
        (
            "deploy/cloud-init/user-data.yaml",
            [
                "#cloud-config\n",
                "/opt/todo/releases",
                "/opt/todo/secrets]",
                "[ufw, allow, 443/tcp]",
            ]
            .map(String::from)
            .to_vec(),
        ),
    ];
    if web {
        contains[0].1.extend([
            "- web-${SPINUP_COLOR}\n".into(),
            "API_URL: http://api-${SPINUP_COLOR}:8080\n".into(),
        ]);
        contains[2].1.extend([
            "handle /api/* {\n".into(),
            "import upstream.caddy web 3000\n".into(),
        ]);
    } else {
        for absent in ["WEB_IMAGE", "web-${SPINUP_COLOR}"] {
            assert!(
                !compose.contains(absent),
                "deploy/compose.yml of an API-only app contains {absent:?}"
            );
        }
        assert!(
            !caddyfile.contains("handle /api/*") && !caddyfile.contains("upstream.caddy web"),
            "deploy/Caddyfile of an API-only app routes to the web service"
        );
    }
    for (name, needles) in &contains {
        let data = read(tree, name)?;
        for needle in needles {
            assert!(data.contains(needle), "{name} does not contain {needle:?}");
        }
    }
    // ADR 0003: the plain service name resolves to both colors on the shared network.
    assert!(
        !compose.contains("http://api:8080"),
        "deploy/compose.yml uses http://api:8080; use the color alias api-${{SPINUP_COLOR}}"
    );
    // Only Caddy publishes ports. Postgres publishes none.
    assert!(
        !compose.contains("ports:") && edge.matches("ports:").count() == 1,
        "a service other than caddy publishes a port"
    );
    for (name, data) in [
        ("deploy/compose.yml", &compose),
        ("deploy/compose.edge.yml", &edge),
    ] {
        assert!(!data.contains(":latest"), "{name} uses a latest tag");
    }
    Ok(())
}

/// The monitoring Compose file mounts only the restricted secrets and labels the config hash
/// (Go `TestMonitoringConfigLabelsAndSecrets`).
#[test]
fn golden_deploy_monitoring_labels_and_secrets() -> Result<()> {
    if !spinup_testutil::golden::selected("deploy") {
        return Ok(());
    }
    #[derive(serde::Deserialize)]
    struct Service {
        #[serde(default)]
        labels: std::collections::BTreeMap<String, String>,
        #[serde(default)]
        volumes: Vec<String>,
        #[serde(default)]
        environment: std::collections::BTreeMap<String, String>,
    }
    #[derive(serde::Deserialize)]
    struct Compose {
        services: std::collections::BTreeMap<String, Service>,
    }
    for profile in ["lite", "full"] {
        let tree = crate::golden_monitoring::render_monitoring(profile)?;
        let compose: Compose = parse_yaml(&read(&tree, "monitoring/compose.monitoring.yml")?)?;
        for (service, key) in [
            ("alloy", "ALLOY"),
            ("prometheus", "PROMETHEUS"),
            ("loki", "LOKI"),
            ("tempo", "TEMPO"),
            ("grafana", "GRAFANA"),
            ("blackbox-exporter", "BLACKBOX"),
        ] {
            if let Some(cfg) = compose.services.get(service) {
                assert_eq!(
                    cfg.labels.get("spinup.config-hash").map(String::as_str),
                    Some(format!("${{{key}_CONFIG_HASH:-}}").as_str()),
                    "{profile}: {service} config hash label"
                );
            }
        }
        for (service, cfg) in &compose.services {
            assert!(
                !cfg.volumes
                    .iter()
                    .any(|mount| mount.starts_with("/opt/todo/secrets:")),
                "{profile}: {service} mounts all application secrets"
            );
            if service == "grafana" || service == "postgres-exporter" {
                assert!(
                    cfg.volumes
                        .iter()
                        .any(|mount| mount == "/opt/todo/monitoring-secrets:/run/secrets:ro"),
                    "{profile}: {service} misses the restricted secrets mount"
                );
            }
        }
        if profile == "full" {
            assert_eq!(
                compose.services["postgres-exporter"]
                    .environment
                    .get("DATA_SOURCE_PASS_FILE")
                    .map(String::as_str),
                Some("/run/secrets/POSTGRES_PASSWORD"),
                "postgres-exporter uses the wrong secret"
            );
        }
    }
    Ok(())
}
