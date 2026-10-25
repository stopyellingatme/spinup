//! Optional module metadata and dependency selection.

use crate::answers::{ask_question, default_value, questions, validate_answers};
use anyhow::{Result, bail};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use spinup_manifest::lock::Lock;
use spinup_packs::{
    embed::embedded,
    loader::{Pack, list, load},
};
use spinup_project::config::Config;
use spinup_ui::prompt::Prompter;
use spinup_vfs::tree::Tree;
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::OnceLock,
};

/// Metadata for one optional built-in pack.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Module {
    /// The pack name.
    pub name: String,
    /// The purpose of the module.
    pub description: String,
    /// Direct pack dependencies.
    pub requires: Vec<String>,
    /// Whether any dependency requires the web pack.
    pub needs_web: bool,
}

fn core(name: &str) -> bool {
    matches!(name, "base" | "api" | "web")
}

fn catalog() -> &'static Vec<Module> {
    static CATALOG: OnceLock<Vec<Module>> = OnceLock::new();
    CATALOG.get_or_init(|| {
        let tree = embedded().expect("embedded module tree is invalid");
        let packs: BTreeMap<_, _> = list(&tree)
            .expect("embedded pack list is invalid")
            .into_iter()
            .filter(|name| !name.starts_with('_'))
            .map(|name| {
                let pack = load(&tree, &name).expect("embedded module pack is invalid");
                (name, pack)
            })
            .collect();
        packs
            .iter()
            .filter(|(name, _)| !core(name))
            .map(|(name, pack)| Module {
                name: name.clone(),
                description: pack.description.clone(),
                requires: pack.requires.clone(),
                needs_web: needs_web(name, &packs, &mut BTreeSet::new()),
            })
            .collect()
    })
}

fn needs_web(name: &str, packs: &BTreeMap<String, Pack>, visiting: &mut BTreeSet<String>) -> bool {
    if name == "web" {
        return true;
    }
    assert!(
        visiting.insert(name.into()),
        "invalid embedded pack dependency: {name}"
    );
    let pack = packs
        .get(name)
        .expect("embedded pack dependency is missing");
    let result = pack
        .requires
        .iter()
        .any(|name| needs_web(name, packs, visiting));
    visiting.remove(name);
    result
}

/// Returns independent metadata in name order.
pub fn all() -> Vec<Module> {
    catalog().clone()
}

/// Returns one optional module. Core packs have no module entry.
pub fn get(name: &str) -> Option<Module> {
    catalog().iter().find(|m| m.name == name).cloned()
}

/// Tests a known module name against the configuration.
///
/// # Panics
/// Panics for an unknown name, which indicates a programming error.
pub fn enabled(config: Option<&Config>, name: &str) -> bool {
    assert!(get(name).is_some(), "unknown built-in module {name:?}");
    config.is_some_and(|c| c.modules.iter().any(|m| m == name))
}

/// Reports whether the lock selects the web pack.
pub fn web_enabled(lock: Option<&Lock>) -> bool {
    lock.is_some_and(|lock| lock.packs.iter().any(|name| name == "web"))
}

/// The complete result of adding modules, without file writes.
#[derive(Debug, Clone, PartialEq)]
pub struct Selection {
    /// All target packs in dependency order.
    pub packs: Vec<String>,
    /// Completed target answers.
    pub answers: Map<String, Value>,
    /// New modules in name order.
    pub added: Vec<String>,
    /// Automatically selected dependencies in name order.
    pub required: Vec<String>,
}

/// Project state and explicit answers for a module addition.
pub struct SelectOptions<'a> {
    /// Current project configuration.
    pub config: &'a Config,
    /// Previous selection and answers.
    pub lock: &'a Lock,
    /// Packs from the locked template version.
    pub fs: &'a Tree,
    /// Requested module names.
    pub names: &'a [String],
    /// Explicit answers for newly selected packs.
    pub answers: &'a Map<String, Value>,
    /// Allows questions after the caller confirms terminal availability.
    pub interactive: bool,
    /// The question interface. Non-interactive selection never calls it.
    pub prompter: Option<&'a mut dyn Prompter>,
}

/// Selects dependencies and validates answers without changing project state.
pub fn select(mut options: SelectOptions<'_>) -> Result<Selection> {
    if options.names.is_empty() {
        bail!("select modules: no module names\nTry: spinup module add <name>");
    }
    for name in options.names {
        if get(name).is_none() {
            bail!("add module {name:?}: unknown module\nTry: spinup module list");
        }
    }
    let mut answers = Map::new();
    for (key, raw) in &options.lock.answers {
        answers.insert(
            key.clone(),
            serde_json::from_str(raw.get()).map_err(|e| {
                anyhow::anyhow!("read locked answer {key:?}: {e}\nTry: restore spinup.lock")
            })?,
        );
    }
    let embed = answers.get("frontend_mode").and_then(Value::as_str) == Some("embed");
    let mut selected = BTreeMap::new();
    let mut visiting = BTreeSet::new();
    for name in options
        .lock
        .packs
        .iter()
        .chain(&options.config.modules)
        .chain(options.names)
    {
        add(
            name,
            options.fs,
            options.lock,
            embed,
            &mut selected,
            &mut visiting,
        )?;
    }
    // Repeatedly select the first ready name, matching Go's stable topological order.
    let mut ordered = Vec::new();
    let mut done = BTreeSet::new();
    while ordered.len() < selected.len() {
        let ready = selected.iter().find(|(name, pack)| {
            !done.contains(name.as_str()) && pack.requires.iter().all(|dep| done.contains(dep))
        });
        let Some((name, pack)) = ready else {
            bail!("select modules: dependency cycle\nTry: use packs without cyclic requirements");
        };
        done.insert(name.clone());
        ordered.push(pack.clone());
    }
    let fresh: Vec<_> = ordered
        .iter()
        .filter(|pack| !options.lock.packs.contains(&pack.name))
        .cloned()
        .collect();
    let mut enabled: Vec<_> = ordered
        .iter()
        .filter(|pack| get(&pack.name).is_some())
        .map(|pack| pack.name.clone())
        .collect();
    enabled.sort();
    answers.insert(
        "modules".into(),
        serde_json::to_value(enabled).expect("module names serialize"),
    );
    let questions = questions(&fresh)?;
    for question in &questions {
        let mut q = question.clone();
        if let Some(value) = options.answers.get(&q.key) {
            answers.insert(q.key.clone(), value.clone());
            continue;
        }
        if q.key == "monitoring_profile" && q.choices.contains(&options.config.monitoring.profile) {
            q.default.clone_from(&options.config.monitoring.profile);
            answers.remove(&q.key);
        }
        if answers.contains_key(&q.key) {
            continue;
        }
        let mut value = default_value(&q);
        if options.interactive {
            let prompter = options.prompter.as_deref_mut().ok_or_else(|| {
                anyhow::anyhow!(
                    "ask module question {:?}: no prompter\nTry: pass --{}",
                    q.key,
                    q.key.replace('_', "-")
                )
            })?;
            value = Some(ask_question(prompter, &q, value.as_ref())?);
        }
        if let Some(value) = value {
            answers.insert(q.key, value);
        }
    }
    for key in options.answers.keys() {
        if !questions.iter().any(|q| &q.key == key) {
            bail!(
                "answer module question {key:?}: it does not belong to a new pack\nTry: pass flags only for modules being added"
            );
        }
    }
    let mut added: Vec<_> = fresh
        .iter()
        .filter(|pack| !core(&pack.name))
        .map(|pack| pack.name.clone())
        .collect();
    added.sort();
    let required = added
        .iter()
        .filter(|name| !options.names.contains(name))
        .cloned()
        .collect();
    Ok(Selection {
        packs: ordered.iter().map(|pack| pack.name.clone()).collect(),
        answers: validate_answers(&ordered, &answers)?,
        added,
        required,
    })
}

fn add(
    name: &str,
    tree: &Tree,
    lock: &Lock,
    embed: bool,
    selected: &mut BTreeMap<String, Pack>,
    visiting: &mut BTreeSet<String>,
) -> Result<()> {
    if name == "auth" && embed {
        bail!(
            "cannot use auth in embed mode: its pages require SvelteKit form actions\nTry: use --frontend-mode node, or remove auth from --modules"
        );
    }
    if selected.contains_key(name) {
        return Ok(());
    }
    if !visiting.insert(name.into()) {
        bail!(
            "select module {name:?}: dependency cycle\nTry: use packs without cyclic requirements"
        );
    }
    if name == "web" && !web_enabled(Some(lock)) {
        bail!(
            "add modules: a requested module requires web, but this app is API-only\nTry: select modules without a web requirement"
        );
    }
    let pack = load(tree, name)?;
    for dependency in &pack.requires {
        add(dependency, tree, lock, embed, selected, visiting)?;
    }
    visiting.remove(name);
    selected.insert(name.into(), pack);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn lock(modules: &[&str], web: bool) -> Lock {
        let mut lock = Lock::new();
        lock.packs = ["base", "api"]
            .into_iter()
            .chain(modules.iter().copied())
            .map(str::to_owned)
            .collect();
        if web {
            lock.packs.push("web".into());
        }
        lock.answers = json!({"app_name":"demo", "module":"example.com/demo", "modules":modules, "db_version":"18"}).as_object().unwrap().iter().map(|(k,v)| (k.clone(), serde_json::value::to_raw_value(v).unwrap())).collect();
        lock
    }

    #[test]
    fn catalog_matches_optional_packs() {
        let tree = embedded().unwrap();
        let expected: Vec<_> = list(&tree)
            .unwrap()
            .into_iter()
            .filter(|n| !core(n) && !n.starts_with('_'))
            .collect();
        let catalog = all();
        assert_eq!(
            catalog.iter().map(|m| m.name.clone()).collect::<Vec<_>>(),
            expected
        );
        assert!(catalog.iter().all(|m| !m.description.is_empty()));
        assert!(get("auth").unwrap().needs_web);
        let mut auth = get("auth").unwrap();
        auth.requires.clear();
        assert!(get("auth").unwrap().requires.contains(&"web".into()));
        assert!(get("web").is_none());
        assert!(!enabled(None, "jobs"));
        assert!(enabled(
            Some(&Config {
                modules: vec!["jobs".into()],
                ..Config::default()
            }),
            "jobs"
        ));
        assert!(web_enabled(Some(&lock(&[], true))));
        assert!(!web_enabled(None));
    }

    #[test]
    fn selections_and_rejections() {
        let tree = embedded().unwrap();
        for (name, web, mode, failure) in [
            ("monitoring", false, "", ""),
            ("jobs", false, "", ""),
            ("auth", false, "", "API-only"),
            ("auth", true, "embed", "embed mode"),
            ("auth", true, "node", ""),
            ("bogus", false, "", "unknown module"),
            ("web", true, "", "unknown module"),
        ] {
            let config = Config::default();
            let mut previous = lock(&[], web);
            if !mode.is_empty() {
                previous.answers.insert(
                    "frontend_mode".into(),
                    serde_json::value::to_raw_value(mode).unwrap(),
                );
            }
            let original = previous.clone();
            let names = vec![name.into(), name.into()];
            let flags = Map::new();
            let result = select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &names,
                answers: &flags,
                interactive: false,
                prompter: None,
            });
            if !failure.is_empty() {
                let error = result.unwrap_err().to_string();
                assert!(
                    error.contains(failure) && error.contains("\nTry:"),
                    "{error}"
                );
            } else {
                let result = result.unwrap();
                assert!(result.added.contains(&name.into()));
                if name == "monitoring" {
                    assert_eq!(result.required, ["deploy"]);
                }
            }
            assert_eq!(previous.packs, original.packs);
            assert_eq!(
                previous.answers["modules"].get(),
                original.answers["modules"].get()
            );
        }
    }

    #[test]
    fn locked_questions_plugins_and_cycles() {
        let mut tree = Tree::new();
        for (path, source) in [
            ("base/pack.toml", "name='base'"),
            ("api/pack.toml", "name='api'\nrequires=['base']"),
            ("extra/pack.toml", "name='extra'\nrequires=['api']"),
            (
                "jobs/pack.toml",
                "name='jobs'\nrequires=['api']\n[[questions]]\nkey='queue_name'\nprompt='Queue name'\ntype='string'",
            ),
        ] {
            tree.insert(path, source.as_bytes().to_vec(), 0o644)
                .unwrap();
        }
        let config = Config::default();
        let mut previous = lock(&[], false);
        previous.packs.push("extra".into());
        let names = ["jobs".into()];
        let mut flags = Map::new();
        let error = select(SelectOptions {
            config: &config,
            lock: &previous,
            fs: &tree,
            names: &names,
            answers: &flags,
            interactive: false,
            prompter: None,
        })
        .unwrap_err()
        .to_string();
        assert!(error.contains("--queue-name"), "{error}");
        flags.insert("queue_name".into(), "priority".into());
        let result = select(SelectOptions {
            config: &config,
            lock: &previous,
            fs: &tree,
            names: &names,
            answers: &flags,
            interactive: false,
            prompter: None,
        })
        .unwrap();
        assert_eq!(result.packs, ["base", "api", "extra", "jobs"]);
        assert_eq!(result.added, ["jobs"]);
        assert_eq!(result.answers["queue_name"], "priority");
        flags.insert("monitoring_profile".into(), "full".into());
        assert!(
            select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &names,
                answers: &flags,
                interactive: false,
                prompter: None
            })
            .unwrap_err()
            .to_string()
            .contains("does not belong")
        );
        tree.insert(
            "jobs/pack.toml",
            b"name='jobs'\nrequires=['jobs']".to_vec(),
            0o644,
        )
        .unwrap();
        assert!(
            select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &names,
                answers: &Map::new(),
                interactive: false,
                prompter: None
            })
            .unwrap_err()
            .to_string()
            .contains("dependency cycle")
        );
    }

    #[test]
    fn prompting_policy_and_existing_modules() {
        use spinup_ui::prompt::{Answer, ScriptedPrompter};
        let tree = embedded().unwrap();
        let config = Config::default();
        let previous = lock(&[], false);
        let names = ["monitoring".into()];
        for interactive in [false, true] {
            let mut prompter =
                ScriptedPrompter::new().answer("monitoring-profile", Answer::Index(1));
            let result = select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &names,
                answers: &Map::new(),
                interactive,
                prompter: Some(&mut prompter),
            })
            .unwrap();
            assert_eq!(prompter.asked().len(), usize::from(interactive));
            assert_eq!(
                result.answers["monitoring_profile"],
                if interactive { "full" } else { "lite" }
            );
        }
        let error = select(SelectOptions {
            config: &config,
            lock: &previous,
            fs: &tree,
            names: &names,
            answers: &Map::new(),
            interactive: true,
            prompter: None,
        })
        .unwrap_err()
        .to_string();
        assert!(error.contains("--monitoring-profile"));
        let previous = lock(&["jobs"], false);
        let names = ["jobs".into()];
        let result = select(SelectOptions {
            config: &config,
            lock: &previous,
            fs: &tree,
            names: &names,
            answers: &Map::new(),
            interactive: false,
            prompter: None,
        })
        .unwrap();
        assert!(result.added.is_empty() && result.required.is_empty());
        assert!(
            select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &[],
                answers: &Map::new(),
                interactive: false,
                prompter: None
            })
            .unwrap_err()
            .to_string()
            .contains("no module names")
        );
        assert!(std::panic::catch_unwind(|| enabled(None, "bogus")).is_err());
    }

    #[test]
    fn monitoring_preference_and_flags() {
        let tree = embedded().unwrap();
        let names = ["monitoring".into()];
        for (configured, locked, flag, expected) in [
            ("full", "", "", "full"),
            ("none", "", "", "none"),
            ("full", "lite", "", "full"),
            ("full", "", "lite", "lite"),
            ("bad", "", "", "lite"),
            ("", "", "wrong", "error"),
        ] {
            let mut config = Config::default();
            config.monitoring.profile = configured.into();
            let mut previous = lock(&["ci", "deploy"], false);
            if !locked.is_empty() {
                previous.answers.insert(
                    "monitoring_profile".into(),
                    serde_json::value::to_raw_value(locked).unwrap(),
                );
            }
            let mut flags = Map::new();
            if !flag.is_empty() {
                flags.insert("monitoring_profile".into(), flag.into());
            }
            let result = select(SelectOptions {
                config: &config,
                lock: &previous,
                fs: &tree,
                names: &names,
                answers: &flags,
                interactive: false,
                prompter: None,
            });
            if expected == "error" {
                assert!(
                    result
                        .unwrap_err()
                        .to_string()
                        .contains("--monitoring-profile")
                );
            } else {
                assert_eq!(result.unwrap().answers["monitoring_profile"], expected);
            }
        }
    }
}
