//! Empty jobs, worker wiring, and generator compatibility for Rust apps.

use anyhow::Result;
use serde_json::json;

/// Renders the empty module list with the generator syntax.
pub(crate) fn empty_module(template: &str) -> Result<String> {
    let mut env = spinup_render::env::environment(None);
    env.set_syntax(
        env.syntax()
            .to_builder()
            .trim_blocks(true)
            .lstrip_blocks(true)
            .build()?,
    );
    env.add_global("names", Vec::<String>::new());
    Ok(env.render_str(template, ())?)
}

#[test]
fn golden_jobs() -> Result<()> {
    if !spinup_testutil::golden::selected("jobs") {
        return Ok(());
    }
    let tree = crate::golden_deploy::render(
        &["base", "api", "jobs"],
        json!({"app_name": "todo", "module": "example.com/todo", "modules": "ci,jobs"}),
    )?;
    let module = crate::golden_deploy::read(&tree, "api/src/jobs/mod.rs")?;
    assert_eq!(
        module,
        empty_module(include_str!(
            "../../../spinup-gen-app/templates/job/mod.rs.j2"
        ))?
    );
    let main = crate::golden_deploy::read(&tree, "api/src/main.rs")?;
    for want in [
        "\"worker\"",
        "kit::jobs::worker_command(",
        "crate::jobs::register(worker, &pool)?",
    ] {
        assert!(main.contains(want), "missing worker wiring: {want}");
    }
    assert!(!main.contains("mailers.register_jobs"));
    assert!(!tree.iter().any(|(path, _)| path.ends_with(".go")));
    assert_eq!(
        tree.iter()
            .filter(|(path, _)| path.starts_with("api/migrations/"))
            .count(),
        2
    );
    let embedded = spinup_packs::embed::embedded()?;
    let jobs = spinup_packs::loader::load(&embedded, "jobs")?;
    assert_eq!(jobs.requires, ["api"]);
    assert_eq!(jobs.files.len(), 1);
    assert_eq!(jobs.files[0].class, "managed");
    spinup_testutil::golden::run("jobs")?;
    Ok(())
}

#[test]
fn golden_jobs_off_keeps_api_main() -> Result<()> {
    if !spinup_testutil::golden::selected("jobs") {
        return Ok(());
    }
    let plain = crate::golden_deploy::render(
        &["base", "api"],
        json!({"app_name": "todo", "module": "example.com/todo"}),
    )?;
    let ci = crate::golden_deploy::render(
        &["base", "api", "ci"],
        json!({"app_name": "todo", "module": "example.com/todo", "modules": "ci"}),
    )?;
    let main = crate::golden_deploy::read(&plain, "api/src/main.rs")?;
    assert_eq!(main, crate::golden_deploy::read(&ci, "api/src/main.rs")?);
    assert!(!main.contains("jobs"));
    Ok(())
}
