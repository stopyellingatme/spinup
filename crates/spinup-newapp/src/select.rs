//! Pack composition and application planning.

use anyhow::{Result as AnyResult, bail};
use serde_json::{Map, Value};
use spinup_manifest::lock::Lock;
use spinup_packs::loader::{Pack, list, load};
use spinup_ui::prompt::Prompter;
use spinup_vfs::plan::Plan;
use std::{
    collections::BTreeMap,
    path::{Component, Path, PathBuf},
};

/// Explicit answers and execution settings for a new application.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// The positional application name.
    pub name: String,
    /// The parent directory. An empty path selects the working directory.
    pub dir: PathBuf,
    /// The CLI version, or an empty string for `dev`.
    pub version: String,
    /// The local kit directory supplied by `SPINUP_KIT_REPLACE`.
    pub kit_replace: PathBuf,
    /// Omits the web frontend.
    pub api_only: bool,
    /// Omits Git initialization.
    pub no_git: bool,
    /// Reports the plan without writing it.
    pub dry_run: bool,
    /// Reports the result as JSON.
    pub json: bool,
    /// Permits prompts after the caller verifies terminal availability.
    pub interactive: bool,
    /// Explicit question values supplied by flags.
    pub answers: Map<String, Value>,
    /// Plugin sources with optional version suffixes.
    pub plugins: Vec<String>,
    /// Approves plugins without a prompt in builds with plugin support.
    pub trust: bool,
    /// Requires cached plugins in builds with plugin support.
    pub offline: bool,
}

/// The plan, validated lock, and output metadata of a new app.
#[derive(Debug, Clone)]
pub struct ResultPlan {
    /// The absolute application directory.
    pub dir: PathBuf,
    /// Pack names in dependency order.
    pub packs: Vec<String>,
    /// The files to apply. Generation never writes these files.
    pub plan: Plan,
    /// The first schema 3 lock.
    pub lock: Lock,
    /// Nonfatal notices for the user.
    pub warnings: Vec<String>,
    /// The absolute local kit directory, when selected.
    pub kit_replace: Option<PathBuf>,
}

/// The Go-compatible name for the generated result.
pub type Result = ResultPlan;

/// Loads every public embedded pack in name order.
pub fn catalog() -> AnyResult<Vec<Pack>> {
    let tree = spinup_packs::embed::embedded()?;
    list(&tree)?
        .into_iter()
        .filter(|name| !name.starts_with('_'))
        .map(|name| load(&tree, &name))
        .collect()
}

/// Plans an application without writing its target directory.
pub fn generate(
    options: &Options,
    mut prompter: Option<&mut dyn Prompter>,
) -> AnyResult<ResultPlan> {
    let packs = catalog()?;
    let mut options = options.clone();
    if options.api_only {
        options
            .answers
            .entry("frontend_mode")
            .or_insert_with(|| "node".into());
    }
    let core: Vec<_> = packs
        .iter()
        .filter(|p| {
            matches!(p.name.as_str(), "base" | "api") || (p.name == "web" && !options.api_only)
        })
        .cloned()
        .collect();
    let questions = crate::questions::questions(&core)?;
    let (answers, mut warnings) = match prompter.as_mut() {
        Some(p) => crate::questions::complete_answers(&options, &questions, Some(&mut **p))?,
        None => crate::questions::complete_answers(&options, &questions, None)?,
    };
    if answers.get("frontend_mode").and_then(Value::as_str) == Some("embed") {
        if options.api_only {
            bail!(
                "cannot embed a frontend with --api-only: the web pack is absent\nTry: remove --api-only, or use --frontend-mode node"
            );
        }
        if answers
            .get("modules")
            .and_then(Value::as_array)
            .is_some_and(|modules| modules.iter().any(|m| m == "auth"))
        {
            bail!(
                "cannot use auth in embed mode: its pages require SvelteKit form actions\nTry: use --frontend-mode node, or remove auth from --modules"
            );
        }
    }
    let selected = select_packs(&packs, &answers, options.api_only)?;
    let questions = crate::questions::questions(&selected)?;
    // Drop flags for unselected packs before the second completion.
    options.answers = questions
        .iter()
        .filter_map(|q| answers.get(&q.key).map(|v| (q.key.clone(), v.clone())))
        .collect();
    let (answers, extra) = crate::questions::complete_answers(&options, &questions, prompter)?;
    warnings.extend(extra);
    let answers = spinup_render::answers::validate_answers(&selected, &answers)?;
    if answers.get("app_name").and_then(Value::as_str) != Some(&options.name)
        || spinup_vfs::path::clean_path(&options.name).ok().as_deref() != Some(&options.name)
        || options.name.contains('/')
    {
        bail!(
            "invalid app_name {:?}\nTry: supply a portable application name",
            options.name
        );
    }
    let dir = absolute(&options.dir.join(&options.name))?;
    check_target(&dir)?;
    let stack = spinup_stack::load::embedded();
    let version = spinup_tplstore::store::current_version(&options.version);
    let mut plan = spinup_render::render::render(spinup_render::render::Input {
        packs: &selected,
        answers: &answers,
        stack: Some(stack),
        template_version: &version,
    })?;
    let kit_replace = if options.kit_replace.as_os_str().is_empty() {
        None
    } else {
        Some(crate::kit::apply_replace(&mut plan, &options.kit_replace)?)
    };
    let names: Vec<_> = selected.iter().map(|p| p.name.clone()).collect();
    let lock =
        crate::lockfile::new_lock(&options.version, &version, &names, &answers, &plan, stack)?;
    crate::plugins::check_plugins(&options, &dir, &lock)?;
    Ok(ResultPlan {
        dir,
        packs: names,
        plan,
        lock,
        warnings,
        kit_replace,
    })
}

/// Selects core packs and explicit modules, then orders their dependencies.
pub fn select_packs(
    packs: &[Pack],
    answers: &Map<String, Value>,
    api_only: bool,
) -> AnyResult<Vec<Pack>> {
    let available: BTreeMap<_, _> = packs.iter().map(|p| (p.name.as_str(), p)).collect();
    let modules: Vec<_> = available
        .values()
        .filter(|p| {
            !matches!(p.name.as_str(), "base" | "api" | "web")
                && (!api_only || !p.requires.iter().any(|r| r == "web"))
        })
        .map(|p| p.name.as_str())
        .collect();
    let mut selected = BTreeMap::new();
    for name in if api_only {
        vec!["base", "api"]
    } else {
        vec!["base", "api", "web"]
    } {
        let pack = available.get(name).ok_or_else(|| anyhow::anyhow!("required pack {name:?} is missing\nTry: reinstall spinup, or pass --api-only if the web pack is absent"))?;
        selected.insert(name.to_string(), (*pack).clone());
    }
    let values = answers
        .get("modules")
        .and_then(Value::as_array)
        .ok_or_else(|| {
            anyhow::anyhow!(
                "invalid modules answer\nTry: pass --modules {}",
                modules.join(",")
            )
        })?;
    for value in values {
        let name = value.as_str().ok_or_else(|| {
            anyhow::anyhow!(
                "invalid modules answer: expected a module name\nTry: pass --modules {}",
                modules.join(",")
            )
        })?;
        let pack = available.get(name);
        if api_only && pack.is_some_and(|p| p.requires.iter().any(|r| r == "web")) {
            bail!(
                "module {name:?} needs the web frontend, and --api-only leaves it out\nTry: drop --api-only, or remove {name} from --modules"
            );
        }
        if matches!(name, "base" | "api" | "web") || pack.is_none() {
            bail!(
                "unknown module {name:?}, available modules: {}\nTry: pass --modules {}",
                modules.join(", "),
                modules.join(",")
            );
        }
        selected.insert(name.to_string(), (*pack.unwrap()).clone());
    }
    order_packs(&selected)
}

/// Orders packs by dependency, with name order as the deterministic tie breaker.
pub fn order_packs(selected: &BTreeMap<String, Pack>) -> AnyResult<Vec<Pack>> {
    let mut result: Vec<Pack> = Vec::new();
    while result.len() < selected.len() {
        let mut ready = None;
        for (name, pack) in selected {
            if result.iter().any(|p| &p.name == name) {
                continue;
            }
            let mut complete = true;
            for dependency in &pack.requires {
                if !selected.contains_key(dependency) {
                    bail!(
                        "pack {name:?} requires missing pack {dependency:?}\nTry: select the required module with --modules"
                    );
                }
                if !result.iter().any(|p| &p.name == dependency) {
                    complete = false;
                }
            }
            if complete {
                ready = Some(pack.clone());
                break;
            }
        }
        match ready {
            Some(pack) => result.push(pack),
            None => bail!(
                "template packs contain a dependency cycle\nTry: use packs without cyclic requirements"
            ),
        }
    }
    Ok(result)
}

/// Accepts an absent or empty real directory. Rejects target symlinks.
pub fn check_target(dir: &Path) -> AnyResult<()> {
    let metadata = match std::fs::symlink_metadata(dir) {
        Ok(metadata) => metadata,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(creation_error(e)),
    };
    if !metadata.is_dir() {
        bail!(
            "cannot create app at {dir:?}: target is not a directory\nTry: choose a new or empty directory"
        );
    }
    if std::fs::read_dir(dir)
        .map_err(creation_error)?
        .next()
        .transpose()
        .map_err(creation_error)?
        .is_some()
    {
        bail!(
            "cannot create app at {dir:?}: target directory is not empty\nTry: choose a new or empty directory"
        );
    }
    Ok(())
}

pub(crate) fn absolute(path: &Path) -> AnyResult<PathBuf> {
    let path = std::path::absolute(path).map_err(creation_error)?;
    let mut result = PathBuf::new();
    for component in path.components() {
        match component {
            Component::ParentDir => {
                result.pop();
            }
            Component::CurDir => {}
            other => result.push(other.as_os_str()),
        }
    }
    Ok(result)
}

pub(crate) fn creation_error(error: impl std::fmt::Display) -> anyhow::Error {
    anyhow::anyhow!(
        "Cannot create the application: {error}\nTry: check the target directory and its permissions"
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use spinup_ui::prompt::{Answer, ScriptedPrompter};

    fn options(dir: &Path) -> Options {
        Options {
            name: "todo".into(),
            dir: dir.into(),
            api_only: true,
            no_git: true,
            answers: json!({"module":"example.com/todo"})
                .as_object()
                .unwrap()
                .clone(),
            ..Options::default()
        }
    }

    #[test]
    fn generation_is_pure_and_locks_final_bytes() {
        let parent = tempfile::tempdir().unwrap();
        let kit = tempfile::tempdir().unwrap();
        let mut options = options(parent.path());
        options.kit_replace = kit.path().into();
        let first = generate(&options, None).unwrap();
        let second = generate(&options, None).unwrap();
        assert!(!first.dir.exists());
        assert_eq!(first.packs, ["base", "api", "ci", "deploy"]);
        assert_eq!(first.plan, second.plan);
        assert_eq!(
            first.lock.marshal().unwrap(),
            second.lock.marshal().unwrap()
        );
        assert_eq!(first.lock.kit.crate_name, "spinup-kit");
        assert_eq!(
            first.lock.kit.version,
            spinup_stack::load::embedded()
                .crate_version("spinup-kit")
                .unwrap()
        );
        assert_eq!(first.lock.template_version, "0.0.0-dev");
        assert_eq!(first.lock.behavior_version, 1);
        assert_eq!(first.lock.engine, "minijinja");
        assert!(first.lock.formatter.is_empty());
        assert!(!first.lock.stack_versions.is_empty());
        assert!(!first.lock.files.contains_key(".env"));
        assert_eq!(
            first.lock.files["api/cmd/api/main.go"].class,
            spinup_manifest::lock::Class::Scaffold
        );
        for file in &first.plan.files {
            assert_eq!(
                first.lock.files[&file.path].hash,
                spinup_manifest::hash::hash(&file.data)
            );
        }
        let module = String::from_utf8(first.plan.get("api/go.mod").unwrap().data).unwrap();
        assert!(module.contains("replace github.com/stopyellingatme/spinup/kit => "));
    }

    #[test]
    fn selected_questions_and_flags_control_prompts() {
        let parent = tempfile::tempdir().unwrap();
        let mut options = options(parent.path());
        options.interactive = true;
        let mut prompt = ScriptedPrompter::new()
            .answer("modules", Answer::Text("ci,deploy".into()))
            .answer("db-version", Answer::Index(0))
            .answer("ci-deploy-on-push", Answer::Bool(false));
        let result = generate(&options, Some(&mut prompt)).unwrap();
        assert_eq!(
            prompt.asked(),
            ["modules", "db-version", "ci-deploy-on-push"]
        );
        assert_eq!(result.lock.answers["module"].get(), "\"example.com/todo\"");
        assert_eq!(result.lock.answers["ci_deploy_on_push"].get(), "false");

        options.api_only = false;
        options.answers = json!({"module":"example.com/todo", "db_version":"18", "frontend_mode":"node", "shadcn_registry":"1.7.0", "package_manager":"pnpm", "api_client":"openapi-fetch"}).as_object().unwrap().clone();
        let mut prompt = ScriptedPrompter::new()
            .answer("modules", Answer::Text("deploy,monitoring".into()))
            .answer("monitoring-profile", Answer::Index(1));
        let result = generate(&options, Some(&mut prompt)).unwrap();
        assert!(prompt.asked().contains(&"monitoring-profile".into()));
        assert_eq!(result.lock.answers["monitoring_profile"].get(), "\"full\"");
    }

    #[test]
    fn omitted_modules_drop_their_answers_and_generated_services() {
        let parent = tempfile::tempdir().unwrap();
        for (api_only, modules, dropped, value) in [
            (false, "ci,deploy", "monitoring_profile", "full"),
            (true, "ci,deploy", "shadcn_registry", "1.8.0"),
            (true, "ci", "unknown_answer", "ignored"),
        ] {
            let mut options = options(parent.path());
            options.api_only = api_only;
            options.answers.insert("modules".into(), modules.into());
            options.answers.insert(dropped.into(), value.into());
            let result = generate(&options, None).unwrap();
            assert!(!result.lock.answers.contains_key(dropped));
            assert_eq!(result.lock.answers["db_version"].get(), "\"18\"");
            let config = String::from_utf8(result.plan.get("spinup.toml").unwrap().data).unwrap();
            assert!(!config.contains("profile = \"full\""));
            if let Some(compose) = result.plan.get("deploy/compose.yml") {
                let compose = String::from_utf8(compose.data).unwrap();
                assert!(!compose.contains("OTEL_"));
                if api_only {
                    assert!(!compose.contains("WEB_IMAGE"));
                }
            }
        }
    }

    #[test]
    fn module_selection_errors_and_embed_guards() {
        let parent = tempfile::tempdir().unwrap();
        for (name, api_only, modules, frontend, expected) in [
            ("todo", true, "nope", "node", "available modules"),
            ("todo", true, "base", "node", "available modules"),
            ("../todo", true, "ci", "node", "app_name"),
            ("todo", true, "auth", "node", "web frontend"),
            ("todo", true, "monitoring", "node", "missing pack"),
            ("todo", true, "", "embed", "--api-only"),
            ("todo", false, "auth", "embed", "embed"),
        ] {
            let mut options = options(parent.path());
            options.name = name.into();
            options.api_only = api_only;
            options.answers.insert("modules".into(), modules.into());
            options
                .answers
                .insert("frontend_mode".into(), frontend.into());
            let error = generate(&options, None).unwrap_err().to_string();
            assert!(
                error.contains(expected) && error.contains("Try:"),
                "{error}"
            );
        }
    }

    #[test]
    fn explicit_empty_modules_and_module_fallback() {
        let parent = tempfile::tempdir().unwrap();
        let mut options = options(parent.path());
        options.answers = json!({"modules":""}).as_object().unwrap().clone();
        let result = generate(&options, None).unwrap();
        assert_eq!(result.packs, ["base", "api"]);
        assert_eq!(result.warnings.len(), 1);
        assert!(result.warnings[0].contains("--module"));
        for (version, expected) in [
            ("dev", "0.0.0-dev"),
            ("v1.2.3", "1.2.3"),
            ("1.2.3-01", "0.0.0-dev"),
        ] {
            options.version = version.into();
            assert_eq!(
                generate(&options, None).unwrap().lock.template_version,
                expected
            );
        }
    }

    #[test]
    fn ci_workflows_match_the_selected_web_pack() {
        let parent = tempfile::tempdir().unwrap();
        for (api_only, modules, ci, web) in [
            (true, "ci", true, false),
            (false, "ci", true, true),
            (false, "deploy", false, true),
        ] {
            let mut options = options(parent.path());
            options.api_only = api_only;
            options.answers.insert("modules".into(), modules.into());
            let result = generate(&options, None).unwrap();
            let workflow = result.plan.get(".github/workflows/ci.yml");
            assert_eq!(workflow.is_some(), ci);
            if let Some(file) = workflow {
                assert_eq!(
                    String::from_utf8(file.data).unwrap().contains("\n  web:\n"),
                    web
                );
            }
            let e2e = result.plan.get(".github/workflows/e2e.yml");
            assert_eq!(e2e.is_some(), ci && web);
            if let Some(file) = e2e {
                assert_eq!(file.class, "managed");
                assert!(file.template.starts_with("web/"));
            }
        }
    }

    #[test]
    fn dependency_order_cycle_and_missing_pack() {
        let mut packs: BTreeMap<String, Pack> =
            [("ci", vec![]), ("api", vec!["base"]), ("base", vec![])]
                .into_iter()
                .map(|(name, dependencies)| {
                    (
                        name.into(),
                        Pack {
                            name: name.into(),
                            requires: dependencies.into_iter().map(String::from).collect(),
                            ..Pack::default()
                        },
                    )
                })
                .collect();
        assert_eq!(
            order_packs(&packs)
                .unwrap()
                .iter()
                .map(|p| p.name.as_str())
                .collect::<Vec<_>>(),
            ["base", "api", "ci"]
        );
        packs.get_mut("base").unwrap().requires = vec!["api".into()];
        assert!(
            order_packs(&packs)
                .unwrap_err()
                .to_string()
                .contains("cycle")
        );
        packs.get_mut("base").unwrap().requires = vec!["missing".into()];
        assert!(
            order_packs(&packs)
                .unwrap_err()
                .to_string()
                .contains("missing pack")
        );
    }

    #[test]
    fn target_accepts_only_new_or_empty_directories() {
        let parent = tempfile::tempdir().unwrap();
        let target = parent.path().join("todo");
        check_target(&target).unwrap();
        std::fs::create_dir(&target).unwrap();
        check_target(&target).unwrap();
        std::fs::write(target.join("keep"), b"data").unwrap();
        assert!(
            check_target(&target)
                .unwrap_err()
                .to_string()
                .contains("not empty")
        );
        let file = parent.path().join("file");
        std::fs::write(&file, b"").unwrap();
        assert!(
            check_target(&file)
                .unwrap_err()
                .to_string()
                .contains("not a directory")
        );
        #[cfg(unix)]
        {
            let link = parent.path().join("link");
            std::os::unix::fs::symlink(&target, &link).unwrap();
            assert!(
                check_target(&link)
                    .unwrap_err()
                    .to_string()
                    .contains("not a directory")
            );
        }
    }
}
