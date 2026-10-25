//! The web pack must match the Go golden tree, take every version from the stack versions,
//! and keep the file classes that upgrades depend on (Go
//! `internal/templates/golden_web_test.go`).

use crate::golden_deploy::{read, render};
use anyhow::{Context, Result};
use std::collections::BTreeMap;

#[test]
fn golden_web() -> Result<()> {
    spinup_testutil::golden::run("web")?;
    Ok(())
}

/// The web pack takes every version from the stack versions (Go `TestWebPins`).
#[test]
fn golden_web_pins() -> Result<()> {
    if !spinup_testutil::golden::selected("web") {
        return Ok(());
    }
    #[derive(serde::Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct Package {
        #[serde(default)]
        dependencies: BTreeMap<String, String>,
        #[serde(default)]
        dev_dependencies: BTreeMap<String, String>,
    }
    let stack = spinup_stack::load::embedded();
    let tree = render(
        &["base", "api", "web"],
        serde_json::json!({"app_name": "todo", "module": "example.com/todo"}),
    )?;
    let package: Package = serde_json::from_str(&read(&tree, "web/package.json")?)
        .context("parse web/package.json")?;
    let mut errors = Vec::new();
    for (name, version) in package
        .dependencies
        .iter()
        .chain(package.dev_dependencies.iter())
    {
        match stack.npm_version(name) {
            Ok(want) if want == version => {}
            Ok(want) => errors.push(format!("{name} = {version:?}, want the stack pin {want:?}")),
            Err(error) => errors.push(format!("{name}: {error}")),
        }
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));

    let docker = read(&tree, "deploy/Dockerfile.web")?;
    let node = stack.image("node")?.reference();
    assert_eq!(
        docker.matches(&format!("FROM {node}")).count(),
        2,
        "Dockerfile.web stages on {node}"
    );
    for want in [
        "ARG PUBLIC_ORIGIN",
        "pnpm exec svelte-kit sync",
        "USER node",
        "HEALTHCHECK",
        "BODY_SIZE_LIMIT=",
        "SHUTDOWN_TIMEOUT=",
    ] {
        assert!(
            docker.contains(want),
            "Dockerfile.web does not contain {want:?}"
        );
    }
    Ok(())
}

/// The web pack keeps the file classes that upgrades depend on (ADR 0008 §2, Go
/// `TestWebPackClasses`).
#[test]
fn golden_web_pack_classes() -> Result<()> {
    if !spinup_testutil::golden::selected("web") {
        return Ok(());
    }
    let embedded = spinup_packs::embed::embedded()?;
    let pack = spinup_packs::loader::load(&embedded, "web")?;
    let classes = pack
        .files
        .iter()
        .map(|file| (file.path.as_str(), file.class.as_str()))
        .collect::<BTreeMap<_, _>>();
    let mut errors = Vec::new();
    for (path, want) in [
        ("web/vite.config.js", "managed"),
        ("web/tsconfig.json", "managed"),
        ("web/components.json", "scaffold"),
        ("web/src/app.html", "scaffold"),
        ("web/src/app.css", "scaffold"),
        ("web/src/app.d.ts", "scaffold"),
        ("web/src/hooks.server.ts", "scaffold"),
        ("web/src/lib/utils.ts", "scaffold"),
        ("web/src/lib/api/client.ts", "scaffold"),
        ("web/src/lib/api/schema.d.ts", "seed"),
        ("web/src/routes/+layout.svelte", "scaffold"),
        ("web/src/routes/+layout.server.ts", "scaffold"),
        ("web/src/routes/+page.svelte", "scaffold"),
        ("web/src/routes/healthz/+server.ts", "managed"),
        ("deploy/Dockerfile.web", "managed"),
        ("web/pnpm-lock.yaml", "seed"),
        ("web/src/lib/components/ui/card/card.svelte", "seed"),
    ] {
        let got = classes.get(path).copied().unwrap_or_default();
        if got != want {
            errors.push(format!("{path}: class {got:?}, want {want:?}"));
        }
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));
    let registry = pack
        .questions
        .iter()
        .find(|question| question.key == "shadcn_registry");
    assert!(
        registry.is_some_and(|q| q.r#type == "string" && q.default == "1.7.0"),
        "shadcn_registry question = {registry:?}, want a string with default 1.7.0"
    );
    Ok(())
}
