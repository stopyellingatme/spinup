//! Every pack set renders with strict undefined and without the Go stack sections (R2 gate
//! item 6, rust-plan.md decision 11).
//!
//! The sets are the module combinations of `scripts/rust/app-check.sh` (`make rs-app-check`),
//! plus the default set of `spinup new`. The environment of the renderer is strict: a
//! template that reads a removed field, such as `data.Stack.GoModules`, fails the render.

use std::collections::BTreeMap;

use anyhow::{Context, Result, bail};
use serde_json::{Map, Value, json};
use spinup_packs::loader::Pack;
use spinup_render::render::{Input, render};

/// One pack set: the name, the flags of `spinup new` as answers, and `--api-only`.
struct Set {
    name: &'static str,
    answers: Value,
    api_only: bool,
}

/// The sets of `scripts/rust/app-check.sh`, plus `default` (no flags).
fn sets() -> Vec<Set> {
    vec![
        Set {
            name: "default",
            answers: json!({"app_name": "app", "module": "example.com/app"}),
            api_only: false,
        },
        Set {
            name: "small",
            answers: json!({"app_name": "app", "module": "example.com/small"}),
            api_only: true,
        },
        Set {
            name: "embed",
            answers: json!({"app_name": "app", "module": "example.com/embed", "frontend_mode": "embed"}),
            api_only: false,
        },
        Set {
            name: "full",
            answers: json!({
                "app_name": "app",
                "module": "github.com/example/full",
                "modules": "ci,auth,jobs,mail",
                "auth_oidc": true,
            }),
            api_only: false,
        },
        Set {
            name: "auth",
            answers: json!({"app_name": "app", "module": "example.com/auth", "modules": "auth"}),
            api_only: false,
        },
        Set {
            name: "jobs",
            answers: json!({"app_name": "app", "module": "example.com/jobs", "modules": "jobs"}),
            api_only: false,
        },
        Set {
            name: "mail",
            answers: json!({"app_name": "app", "module": "example.com/mail", "modules": "mail"}),
            api_only: false,
        },
        Set {
            name: "auth-mail",
            answers: json!({"app_name": "app", "module": "example.com/authmail", "modules": "auth,mail"}),
            api_only: false,
        },
    ]
}

/// Loads every public embedded pack by name.
fn catalog() -> Result<BTreeMap<String, Pack>> {
    let tree = spinup_packs::embed::embedded()?;
    let mut packs = BTreeMap::new();
    for name in spinup_packs::loader::list(&tree)? {
        if !name.starts_with('_') {
            packs.insert(name.clone(), spinup_packs::loader::load(&tree, &name)?);
        }
    }
    Ok(packs)
}

/// Selects the core packs and the modules, and orders them by dependency with name order
/// as the tie breaker, as `spinup_newapp::select` does.
fn select(
    catalog: &BTreeMap<String, Pack>,
    answers: &Map<String, Value>,
    api_only: bool,
) -> Result<Vec<Pack>> {
    let mut names = vec!["base".to_owned(), "api".to_owned()];
    if !api_only {
        names.push("web".into());
    }
    let modules = match answers.get("modules") {
        Some(Value::String(text)) => text.clone(),
        Some(other) => bail!("modules must be a string, got {other}"),
        // The default of the `modules` question in the base pack.
        None => "ci,deploy".into(),
    };
    names.extend(
        modules
            .split(',')
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(str::to_owned),
    );
    let mut selected = BTreeMap::new();
    for name in names {
        let pack = catalog
            .get(&name)
            .with_context(|| format!("pack {name:?} is missing"))?;
        selected.insert(name, pack.clone());
    }
    let mut ordered: Vec<Pack> = Vec::new();
    while ordered.len() < selected.len() {
        let next = selected
            .values()
            .find(|pack| {
                !ordered.iter().any(|p| p.name == pack.name)
                    && pack
                        .requires
                        .iter()
                        .all(|r| ordered.iter().any(|p| &p.name == r))
            })
            .with_context(|| {
                format!(
                    "packs {:?} have a missing or cyclic requirement",
                    selected.keys()
                )
            })?;
        ordered.push(next.clone());
    }
    Ok(ordered)
}

#[test]
fn every_pack_set_renders_strictly_without_go_sections() -> Result<()> {
    let catalog = catalog()?;
    let stack = spinup_stack::load::embedded();
    for set in sets() {
        let mut answers = set.answers.as_object().cloned().unwrap_or_default();
        if set.api_only {
            answers
                .entry("frontend_mode")
                .or_insert_with(|| "node".into());
        }
        let packs = select(&catalog, &answers, set.api_only)?;
        let plan = render(Input {
            packs: &packs,
            answers: &answers,
            stack: Some(stack),
            template_version: "0.0.0-test",
        })
        .with_context(|| format!("render the {} set strictly", set.name))?;
        assert!(
            plan.files.iter().any(|f| f.path == "api/src/main.rs"),
            "the {} set has no api/src/main.rs",
            set.name
        );
        for file in &plan.files {
            assert!(
                !file.path.ends_with(".go") && !file.path.ends_with("go.mod"),
                "the {} set renders the Go file {}",
                set.name,
                file.path
            );
        }
    }
    Ok(())
}

#[test]
fn template_data_has_no_go_sections() -> Result<()> {
    let stack = spinup_stack::load::embedded();
    let data = serde_json::to_value(stack)?;
    for name in ["Go", "GoModules", "GoTools"] {
        assert!(data.get(name).is_none(), "data.Stack has {name}");
    }
    let env = spinup_render::env::environment(Some(stack));
    let context = minijinja::Value::from(minijinja::value::Serde(json!({"data": {"Stack": data}})));
    assert_eq!(
        env.render_str("[[ data.Stack.Rust.Toolchain ]]", context.clone())?,
        stack.rust.toolchain
    );
    for source in [
        "[[ data.Stack.Go.Toolchain ]]",
        "[[ data.Stack.GoModules ]]",
        "[% for k, v in data.Stack.GoTools|items %][[ k ]][% endfor %]",
        "[[ gomod('github.com/stopyellingatme/spinup/kit') ]]",
    ] {
        assert!(
            env.render_str(source, context.clone()).is_err(),
            "{source} renders"
        );
    }
    Ok(())
}
