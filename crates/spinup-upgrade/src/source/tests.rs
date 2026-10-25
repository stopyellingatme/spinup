//! The tests of pack and plugin selection (Go `internal/upgrade/source_test.go` and the
//! overlay tests). Each test name holds `plan`, so the unit check `test(plan)` selects it.

use std::path::Path;
use std::sync::Arc;

use spinup_manifest::lock::Lock;
use spinup_plugin_api::{Meta, PluginError};
use spinup_vfs::plan::File;
use spinup_vfs::tree::Tree;

use super::*;
use crate::plan::testing::{FakeRenderer, spec, version};

/// Renders one file for each plugin, with the plugin answer `greeting`.
struct FakePlugins;

impl PluginSource for FakePlugins {
    fn fetch(
        &self,
        _request: &mut spinup_plugin_api::FetchRequest<'_>,
    ) -> Result<Vec<Loaded>, PluginError> {
        Ok(Vec::new())
    }

    fn trust(&self, _root: &Path, _plugins: &[Loaded], _lock: &Lock) -> Result<(), PluginError> {
        Ok(())
    }

    fn render(&self, plugin: &Loaded, answers: &Map<String, Value>) -> Result<Plan, PluginError> {
        let mut plan = Plan::new();
        let name = &plugin.meta.name;
        let data = answers
            .get("greeting")
            .and_then(Value::as_str)
            .unwrap_or("none");
        plan.add(File {
            path: format!("{name}.txt"),
            data: format!("{data}\n").into_bytes(),
            mode: 0o644,
            class: "managed".into(),
            template: format!("{name}/files/greeting.tmpl"),
            ..File::default()
        })
        .map_err(PluginError::Other)?;
        Ok(plan)
    }
}

fn plugin(name: &str, version: &str, sha: &str) -> Loaded {
    plugin_requiring(name, version, sha, &[])
}

fn plugin_requiring(name: &str, version: &str, sha: &str, requires: &[&str]) -> Loaded {
    let mut tree = Tree::new();
    let meta = format!("name = {name:?}\nrequires = {requires:?}\n");
    tree.insert("pack.toml", meta.into_bytes(), 0o644).unwrap();
    Loaded {
        meta: Meta {
            name: name.into(),
            version: version.into(),
            api: 1,
            ..Meta::default()
        },
        source: format!("path:./plugins/{name}"),
        sha256: sha.into(),
        tree: Arc::new(tree),
    }
}

#[test]
fn plan_render_adds_plugin_files_with_prefix() {
    let v = version("0.2.0", &[spec("app.txt", "app").from("old/app.txt")]);
    let templates = Templates {
        packs: &FakeRenderer,
        plugins: &FakePlugins,
    };
    let set = overlay(&v, &["base".into()], &[plugin("hello", "v1.0.0", "aa")]).unwrap();
    assert_eq!(set.names.as_deref().unwrap(), ["base", "hello"]);
    let mut answers = Map::new();
    answers.insert("greeting".into(), "hi".into());
    let rendered = render(&templates, &v, &set, &[], &answers).unwrap();
    let paths: Vec<_> = rendered
        .plan
        .files
        .iter()
        .map(|f| f.path.as_str())
        .collect();
    assert_eq!(paths, ["app.txt", "hello.txt"]);
    let hello = rendered.plan.get("hello.txt").unwrap();
    assert_eq!(hello.template, "plugin/hello/files/greeting.tmpl");
    assert_eq!(hello.data, b"hi\n");
    assert_eq!(rendered.renames["app.txt"], "old/app.txt");
    assert_eq!(rendered.answers, answers);
    let entries = set.lock_plugins();
    assert_eq!(entries.len(), 1);
    assert_eq!(
        (
            entries[0].name.as_str(),
            entries[0].version.as_str(),
            entries[0].api
        ),
        ("hello", "v1.0.0", 1)
    );
}

#[test]
fn plan_render_without_plugin_support_fails_for_plugins() {
    let v = version("0.2.0", &[]);
    let null = spinup_plugin_api::null_source::NullSource::new();
    let templates = Templates {
        packs: &FakeRenderer,
        plugins: &null,
    };
    let set = overlay(&v, &["base".into()], &[plugin("hello", "v1.0.0", "aa")]).unwrap();
    let err = render(&templates, &v, &set, &[], &Map::new()).unwrap_err();
    assert!(format!("{err:#}").contains("Try:"), "{err:#}");
    let plain = render(
        &templates,
        &v,
        &PackSet::default(),
        &["base".into()],
        &Map::new(),
    );
    assert!(plain.unwrap().plan.files.is_empty());
}

#[test]
fn plan_overlay_rejects_collisions() {
    let v = version("0.2.0", &[]);
    for plugins in [
        vec![plugin("base", "v1.0.0", "aa")],
        vec![plugin("x", "v1.0.0", "aa"), plugin("x", "v1.0.0", "bb")],
    ] {
        let err = overlay(&v, &["base".into()], &plugins).unwrap_err();
        assert!(format!("{err:#}").contains("Try: rename the plugin pack"));
    }
    let kept = overlay(&v, &["x".into(), "base".into()], &[plugin("x", "v1", "aa")]).unwrap();
    assert_eq!(kept.names.unwrap(), ["x", "base"]);
}

#[test]
fn plan_locked_and_target_packs() {
    let v = version("0.2.0", &[]);
    let mut lock = Lock::new();
    lock.packs = vec!["base".into(), "old-plugin".into(), "web".into()];
    lock.plugins = vec![spinup_manifest::lock::Plugin {
        name: "old-plugin".into(),
        version: "v1.0.0".into(),
        sha256: "aa".into(),
        api: 1,
    }];
    let none = locked_packs(&lock, None, &[]).unwrap();
    assert!(none.names.is_none() && none.plugins.is_empty());
    let err = locked_packs(&lock, None, &[plugin("old-plugin", "v1.0.0", "aa")]).unwrap_err();
    assert!(format!("{err:#}").contains("Try: restore the old template version"));
    let old = locked_packs(&lock, Some(&v), &[plugin("old-plugin", "v1.0.0", "aa")]).unwrap();
    assert_eq!(old.names.unwrap(), ["base", "old-plugin", "web"]);
    let new = target_packs(&lock, &v, None, &[plugin("new-plugin", "v2.0.0", "bb")]).unwrap();
    assert_eq!(new.names.unwrap(), ["base", "web", "new-plugin"]);
    let target = ["base".to_string(), "ci".into()];
    let new = target_packs(&lock, &v, Some(&target), &[]).unwrap();
    assert_eq!(new.names.unwrap(), ["base", "ci"]);
}

#[test]
fn plan_plugin_chain() {
    let mut lock = Lock::new();
    lock.template_version = "0.2.0".into();
    lock.plugins = vec![spinup_manifest::lock::Plugin {
        name: "hello".into(),
        version: "v1.0.0".into(),
        sha256: "aa".into(),
        api: 1,
    }];
    let same = [plugin("hello", "1.0.0", "aa")];
    assert!(plugin_chain(&lock, &same, Vec::new()).is_empty());
    assert_eq!(plugin_chain(&lock, &same, vec!["0.3.0".into()]), ["0.3.0"]);
    for changed in [
        vec![],
        vec![plugin("hello", "v1.1.0", "aa")],
        vec![plugin("hello", "v1.0.0", "bb")],
        vec![plugin("other", "v1.0.0", "aa")],
        vec![plugin("hello", "vv1.0.0", "aa")],
    ] {
        assert_eq!(
            plugin_chain(&lock, &changed, Vec::new()),
            ["0.2.0"],
            "{changed:?}"
        );
    }
}

#[test]
fn plan_overlay_checks_plugin_requires() {
    let v = version("0.2.0", &[]);
    let needs_web = [plugin_requiring("hello", "v1.0.0", "aa", &["web"])];
    let err = overlay(&v, &["base".into()], &needs_web).unwrap_err();
    let text = format!("{err:#}");
    assert!(
        text.contains("required pack \"web\" is not selected"),
        "{text}"
    );
    assert!(text.contains("Try: enable the required module"), "{text}");
    let set = overlay(&v, &["base".into(), "web".into()], &needs_web).unwrap();
    assert_eq!(set.names.unwrap(), ["base", "web", "hello"]);
    let broken = Loaded {
        tree: Arc::new(Tree::new()),
        ..plugin("broken", "v1.0.0", "aa")
    };
    let err = overlay(&v, &["base".into()], &[broken]).unwrap_err();
    assert!(format!("{err:#}").contains("Try:"));
}

mod real {
    //! Plans through the real renderer [`Packs`] and the embedded converted packs.

    use serde_json::value::to_raw_value;
    use spinup_exec::runner::Cancellation;
    use spinup_manifest::hash::{hash, normalize};
    use spinup_manifest::lock::{Class, FileEntry};
    use spinup_plugin_api::null_source::NullSource;
    use tempfile::TempDir;

    use super::*;
    use crate::contract::{Action, Plan as StepPlan};
    use crate::lock::{answers as lock_answers, next_lock};
    use crate::plan::testing::write;
    use crate::plan::{PlanInput, build_plan, class_of};

    const NULL: NullSource = NullSource::new();

    fn templates() -> Templates<'static> {
        Templates {
            packs: &Packs,
            plugins: &NULL,
        }
    }

    /// Returns the embedded packs and stack versions as template version `name`, with
    /// `edit` applied to an in-memory copy of the pack tree.
    fn embedded(name: &str, edit: impl FnOnce(&mut Tree)) -> Version {
        let current = spinup_tplstore::store::current("0.0.0-dev").unwrap();
        let mut tree = (*current.packs).clone();
        edit(&mut tree);
        Version {
            version: name.into(),
            packs: Arc::new(tree),
            ..(*current).clone()
        }
    }

    fn replace(tree: &mut Tree, path: &str, edit: impl FnOnce(String) -> String) {
        let file = tree.get(path).unwrap().clone();
        let text = String::from_utf8(file.data.to_vec()).unwrap();
        tree.insert(path, edit(text).into_bytes(), file.mode)
            .unwrap();
    }

    fn base() -> Vec<String> {
        vec!["base".into()]
    }

    /// Renders `version` into `root` and returns the lock of that render.
    fn generate(root: &std::path::Path, version: &Version) -> Lock {
        let mut answers = Map::new();
        answers.insert("app_name".into(), "app".into());
        answers.insert("module".into(), "example.com/app".into());
        let rendered = Packs.render(version, &base(), &answers).unwrap();
        let mut lock = Lock::new();
        lock.template_version.clone_from(&version.version);
        lock.packs = base();
        for (key, value) in &rendered.answers {
            lock.answers
                .insert(key.clone(), to_raw_value(value).unwrap());
        }
        for file in &rendered.plan.files {
            write(root, &file.path, &file.data, file.mode);
            let class = class_of(&file.class);
            let seed = class == Class::Seed;
            let text = |value: String| if seed { String::new() } else { value };
            lock.files.insert(
                file.path.clone(),
                FileEntry {
                    class,
                    template: text(file.template.clone()),
                    hash: text(hash(&file.data)),
                    mode: text(format!("{:04o}", file.mode & 0o777)),
                },
            );
        }
        lock.validate().unwrap();
        lock
    }

    fn plan(root: &std::path::Path, lock: &Lock, old: &Version, new: &Version) -> StepPlan {
        let answers = lock_answers(lock).unwrap();
        let set = PackSet::default();
        build_plan(&PlanInput {
            root,
            lock,
            old: Some(old),
            new,
            old_answers: &answers,
            new_answers: &answers,
            old_packs: &set,
            new_packs: &set,
            templates: templates(),
            merger: None,
            force_managed: false,
            cancel: &Cancellation::new(),
        })
        .unwrap()
    }

    #[test]
    fn plan_real_packs_one_changed_file() {
        let old = embedded("0.1.0", |_| {});
        let new = embedded("0.2.0", |tree| {
            replace(tree, "base/files/.gitignore", |text| {
                text + "/upgrade-test\n"
            });
        });
        let root = TempDir::new().unwrap();
        let lock = generate(root.path(), &old);
        let p = plan(root.path(), &lock, &old, &new);
        assert_eq!((p.from.as_str(), p.to.as_str()), ("0.1.0", "0.2.0"));
        assert!(p.tombstoned.is_empty() && p.missing_managed.is_empty());
        assert!(
            p.files
                .iter()
                .any(|d| d.path == "README.md" && d.action == Action::Keep)
        );
        for d in &p.files {
            let want = match (d.path.as_str(), d.class) {
                (".gitignore", _) => Action::Overwrite,
                (_, Class::Seed) => Action::Keep,
                _ => Action::Unchanged,
            };
            assert_eq!(d.action, want, "{d:?}");
        }
        let ignore = p.files.iter().find(|d| d.path == ".gitignore").unwrap();
        assert!(String::from_utf8_lossy(&ignore.data).ends_with("/upgrade-test\n"));
        assert_eq!(ignore.template, "base/files/.gitignore");
        let answers = lock_answers(&lock).unwrap();
        let next = next_lock(&templates(), &lock, &p, &new, &answers, &PackSet::default()).unwrap();
        assert_eq!(
            next.files[".gitignore"].hash,
            hash(&normalize(&ignore.data))
        );
        assert_eq!(next.files["spinup.toml"], lock.files["spinup.toml"]);
        assert_eq!(next.answers.len(), lock.answers.len());
        assert_eq!(next.template_version, "0.2.0");
    }

    #[test]
    fn plan_real_packs_rename() {
        let old = embedded("0.1.0", |_| {});
        let new = embedded("0.2.0", |tree| {
            replace(tree, "base/pack.toml", |text| {
                text.replace(
                    "path = \".env.example\"",
                    "path = \"env/[[ data.app_name ]].example\"\nrenamed_from = \".env.example\"",
                )
            });
        });
        let root = TempDir::new().unwrap();
        let lock = generate(root.path(), &old);
        let answers = lock_answers(&lock).unwrap();
        let rendered = Packs.render(&new, &base(), &answers).unwrap();
        assert_eq!(
            rendered.renames.into_iter().collect::<Vec<_>>(),
            [("env/app.example".to_string(), ".env.example".to_string())]
        );
        let p = plan(root.path(), &lock, &old, &new);
        let moved = p
            .files
            .iter()
            .find(|d| d.path == "env/app.example")
            .unwrap();
        assert_eq!(moved.action, Action::Rename, "{moved:?}");
        assert_eq!(moved.from.as_deref(), Some(".env.example"));
        assert!(!p.files.iter().any(|d| d.path == ".env.example"));
        let next = next_lock(&templates(), &lock, &p, &new, &answers, &PackSet::default()).unwrap();
        assert!(!next.files.contains_key(".env.example"));
        assert_eq!(
            next.files["env/app.example"].hash,
            lock.files[".env.example"].hash
        );
    }

    #[test]
    fn plan_real_packs_errors() {
        let old = embedded("0.1.0", |_| {});
        let err = Packs
            .render(&old, &["missing".into()], &Map::new())
            .unwrap_err();
        let text = format!("{err:#}");
        assert!(
            text.contains("\"missing\"") && text.contains("0.1.0") && text.contains("Try:"),
            "{text}"
        );
        let err = Packs.render(&old, &base(), &Map::new()).unwrap_err();
        assert!(format!("{err:#}").contains("Try:"), "missing answers");
        let bad = embedded("0.2.0", |tree| {
            replace(tree, "base/pack.toml", |text| {
                text.replace(
                    "path = \".env.example\"",
                    "path = \"env.example\"\nrenamed_from = \"[[ data.nothing ]]/../x\"",
                )
            });
        });
        let mut answers = Map::new();
        answers.insert("app_name".into(), "app".into());
        answers.insert("module".into(), "example.com/app".into());
        let err = Packs.render(&bad, &base(), &answers).unwrap_err();
        assert!(format!("{err:#}").contains("Try:"), "{err:#}");
    }
}
