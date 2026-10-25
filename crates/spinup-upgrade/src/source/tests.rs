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
    Loaded {
        meta: Meta {
            name: name.into(),
            version: version.into(),
            api: 1,
            ..Meta::default()
        },
        source: format!("path:./plugins/{name}"),
        sha256: sha.into(),
        tree: Arc::new(Tree::new()),
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
