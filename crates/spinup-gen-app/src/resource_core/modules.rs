//! Rebuild generated Rust module lists from disk and planned files.

use std::{collections::BTreeSet, path::Path};

use anyhow::{Result, bail};
use spinup_vfs::plan::{File, Plan};

use super::paths::{HANDLERS_MOD, MODELS_MOD, ROUTES_GEN};

/// Plans sorted handler, model, and route lists after additions or removals.
/// Deleted paths and `removed` paths do not appear in the lists.
///
/// # Errors
/// Returns directory read failures or invalid module names with a retry hint.
pub fn plan_module_lists(root: &Path, mut plan: Plan, removed: &[String]) -> Result<Plan> {
    let handlers = module_names(root, &plan, removed, "api/src/handlers")?;
    let models = module_names(root, &plan, removed, "api/src/models")?;
    let mut routes = "//! The routes of generated resources. spinup owns this file.\n\nuse kit::db::PgPool;\nuse utoipa_axum::router::OpenApiRouter;\n".to_string();
    if !handlers.is_empty() {
        routes.push_str("\nuse crate::handlers;\n");
    }
    routes.push_str("\n/// Merges the routes of each resource in name order.\npub fn routes() -> OpenApiRouter<PgPool> {\n");
    if handlers.is_empty() {
        routes.push_str("    OpenApiRouter::new()\n");
    } else {
        routes.push_str("    let mut router = OpenApiRouter::new();\n");
        for name in &handlers {
            routes.push_str(&format!(
                "    router = router.merge(handlers::{name}::routes());\n"
            ));
        }
        routes.push_str("    router\n");
    }
    routes.push_str("}\n");
    // Rebuild even when another planner supplied an earlier version of a list.
    plan.files
        .retain(|file| ![HANDLERS_MOD, MODELS_MOD, ROUTES_GEN].contains(&file.path.as_str()));
    for (path, data) in [
        (HANDLERS_MOD, module_list("handlers", &handlers)),
        (MODELS_MOD, module_list("models", &models)),
        (ROUTES_GEN, routes),
    ] {
        plan.add(File {
            path: path.into(),
            data: data.into_bytes(),
            class: "managed".into(),
            ..File::default()
        })?;
    }
    Ok(plan.sorted())
}

fn module_list(kind: &str, names: &BTreeSet<String>) -> String {
    let mut source = format!("//! The resource {kind}. spinup owns this file.\n");
    if !names.is_empty() {
        source.push('\n');
    }
    for name in names {
        source.push_str(&format!("pub mod {name};\n"));
    }
    source
}

fn module_names(
    root: &Path,
    plan: &Plan,
    removed: &[String],
    dir: &str,
) -> Result<BTreeSet<String>> {
    let mut paths = BTreeSet::new();
    match std::fs::read_dir(root.join(dir)) {
        Ok(entries) => {
            for entry in entries {
                let entry = entry.map_err(|error| directory_error(dir, error))?;
                if !entry
                    .file_type()
                    .map_err(|error| directory_error(dir, error))?
                    .is_dir()
                {
                    paths.insert(format!("{dir}/{}", entry.file_name().to_string_lossy()));
                }
            }
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(directory_error(dir, error)),
    }
    for file in &plan.files {
        if file.delete {
            paths.remove(&file.path);
        } else {
            paths.insert(file.path.clone());
        }
    }
    for path in removed {
        paths.remove(path);
    }
    let mut names = BTreeSet::new();
    for path in paths {
        let Some(name) = path
            .strip_prefix(&format!("{dir}/"))
            .and_then(|s| s.strip_suffix(".rs"))
        else {
            continue;
        };
        if name == "mod" || name.ends_with("_tests") || name.contains('/') {
            continue;
        }
        if !name.starts_with(|c: char| c.is_ascii_lowercase() || c == '_')
            || !name
                .bytes()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'_')
        {
            bail!(
                "cannot list Rust module {path:?}: its name is invalid\nTry: rename the file with a Rust module name"
            );
        }
        names.insert(name.into());
    }
    Ok(names)
}

fn directory_error(dir: &str, error: std::io::Error) -> anyhow::Error {
    anyhow::anyhow!("read {dir}: {error}\nTry: check the directory permissions")
}
