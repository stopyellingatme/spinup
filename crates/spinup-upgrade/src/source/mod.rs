//! Pack and plugin selection for the OLD and NEW renders (Go `internal/upgrade/source.go`
//! and the overlay part of `internal/plugin/render`).
//!
//! A [`PackRenderer`] loads and renders the core packs of a template version. [`Packs`] is
//! the real renderer (`spinup-packs` and `spinup-render`). Tests bind a fake. A
//! [`PluginSource`] renders the plugin packs. [`Templates`] holds the two seams. [`PackSet`]
//! selects the packs and the plugins of one side of a render.

use std::collections::BTreeMap;

use anyhow::{Result, anyhow, bail};
use serde_json::{Map, Value};
use spinup_manifest::lock::{Lock, Plugin};
use spinup_plugin_api::{Loaded, PluginSource};
use spinup_tplstore::source::Version;
use spinup_vfs::plan::Plan;
use spinup_vfs::tree::Tree;

mod packs;
#[cfg(test)]
mod tests;

pub use packs::Packs;

/// Renders the core packs of a template version (Go `upgrade.Render` and
/// `upgrade.RenamedFrom`).
///
/// An implementation loads each named pack from [`Version::packs`], completes the answers
/// with the pack defaults (Go `render.ValidateAnswers`), renders the packs in the given order
/// with the stack versions and the template version of `version`, and evaluates the
/// `renamed_from` paths of the enabled files. Each error says what failed and ends with a
/// `Try:` line. A missing pack names the pack and the version.
pub trait PackRenderer: Send + Sync {
    /// Renders `packs` of `version` with `answers`. It never writes files.
    fn render(
        &self,
        version: &Version,
        packs: &[String],
        answers: &Map<String, Value>,
    ) -> Result<Rendered>;
}

/// The result of a render of one side.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Rendered {
    /// The rendered files in render order. The template of a plugin file starts with
    /// `plugin/<name>/`.
    pub plan: Plan,
    /// The answers after the pack defaults were added. The lock records them.
    pub answers: Map<String, Value>,
    /// The renamed files: the new path mapped to the old path (`renamed_from`).
    pub renames: BTreeMap<String, String>,
}

/// The two render seams of an upgrade step.
#[derive(Clone, Copy)]
pub struct Templates<'a> {
    /// Renders the core packs.
    pub packs: &'a dyn PackRenderer,
    /// Renders the packs of trusted plugins. R1 binds the null source.
    pub plugins: &'a dyn PluginSource,
}

impl std::fmt::Debug for Templates<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Templates").finish_non_exhaustive()
    }
}

/// Selects the packs of one side of a render (Go `upgrade.PackSet`).
#[derive(Debug, Clone, Default)]
pub struct PackSet {
    /// The pack names in render order, plugin names included. `None` uses the locked
    /// selection (`Lock::packs`).
    pub names: Option<Vec<String>>,
    /// The trusted plugins whose packs render after the core packs.
    pub plugins: Vec<Loaded>,
}

impl PackSet {
    /// Returns the selected names, or `fallback` when the set selects no names.
    pub fn names_or<'s>(&'s self, fallback: &'s [String]) -> &'s [String] {
        self.names.as_deref().unwrap_or(fallback)
    }

    /// Returns the lock entries of the selected plugins (Go `pluginrender.Plugins`).
    pub fn lock_plugins(&self) -> Vec<Plugin> {
        self.plugins
            .iter()
            .map(|p| Plugin {
                name: p.meta.name.clone(),
                version: p.meta.version.clone(),
                sha256: p.sha256.clone(),
                api: i64::from(p.meta.api),
            })
            .collect()
    }

    fn is_plugin(&self, name: &str) -> bool {
        self.plugins.iter().any(|p| p.meta.name == name)
    }
}

/// Renders one side of a step: the core packs of `set` (or `fallback`) through
/// [`Templates::packs`], then each plugin through [`Templates::plugins`].
///
/// A plugin file gets the template prefix `plugin/` (Go `pluginrender.Prefix`). Two files
/// with the same path (also under case folding) are an error.
pub fn render(
    templates: &Templates<'_>,
    version: &Version,
    set: &PackSet,
    fallback: &[String],
    answers: &Map<String, Value>,
) -> Result<Rendered> {
    let core: Vec<String> = set
        .names_or(fallback)
        .iter()
        .filter(|name| !set.is_plugin(name))
        .cloned()
        .collect();
    let mut rendered = templates.packs.render(version, &core, answers)?;
    for plugin in &set.plugins {
        let plan = templates.plugins.render(plugin, &rendered.answers)?;
        let prefix = format!("{}/", plugin.meta.name);
        for mut file in plan.files {
            if file.template.starts_with(&prefix) {
                file.template = format!("plugin/{}", file.template);
            }
            rendered.plan.add(file).map_err(|e| {
                anyhow!(
                    "render plugin {:?} for version {}: {e:#}",
                    plugin.meta.name,
                    version.version
                )
            })?;
        }
    }
    Ok(rendered)
}

/// Adds the plugins to a selection of core packs (Go `pluginrender.Overlay`).
///
/// A plugin name that equals a pack of `version`, or the name of another plugin, is an
/// error. The result keeps the order of `names` and appends the plugin names that `names`
/// does not hold. Each pack that a plugin pack requires must be in the result.
pub fn overlay(version: &Version, names: &[String], plugins: &[Loaded]) -> Result<PackSet> {
    let mut selected = names.to_vec();
    let mut seen: Vec<&str> = Vec::new();
    for plugin in plugins {
        let name = plugin.meta.name.as_str();
        if version.packs.is_dir(name) || seen.contains(&name) {
            bail!(
                "load plugin {name:?}: pack name collides with another pack\nTry: rename the plugin pack"
            );
        }
        seen.push(name);
        if !selected.iter().any(|n| n == name) {
            selected.push(name.to_string());
        }
    }
    for plugin in plugins {
        let name = plugin.meta.name.as_str();
        let mut tree = Tree::new();
        for (path, file) in plugin.tree.iter() {
            tree.insert(format!("{name}/{path}"), file.data.clone(), file.mode)?;
        }
        let pack = spinup_packs::loader::load(&tree, name)
            .map_err(|e| anyhow!("load plugin {name:?}: {e:#}\nTry: repair the plugin pack"))?;
        for required in &pack.requires {
            if !selected.contains(required) {
                bail!(
                    "load plugin {name:?}: required pack {required:?} is not selected\nTry: enable the required module before adding the plugin"
                );
            }
        }
    }
    Ok(PackSet {
        names: Some(selected),
        plugins: plugins.to_vec(),
    })
}

/// Selects the OLD side: the locked packs and the historical plugins of the lock (Go
/// `upgrade.LockedPacks`).
///
/// `historical` holds the plugins at their locked versions. Without the OLD template
/// version, a lock without plugins gives the default set, and a lock with plugins is an
/// error.
pub fn locked_packs(
    lock: &Lock,
    version: Option<&Version>,
    historical: &[Loaded],
) -> Result<PackSet> {
    let Some(version) = version else {
        if historical.is_empty() {
            return Ok(PackSet::default());
        }
        bail!(
            "load locked plugin packs: template version is unavailable\nTry: restore the old template version before upgrading"
        );
    };
    overlay(version, &lock.packs, historical)
}

/// Selects the NEW side: the target packs and the current plugins (Go
/// `Runner.pluginPacks`).
///
/// `target` holds the target names (`None` uses the lock packs). The selection drops the
/// names of locked and current plugins, keeps the order of the core packs, and appends the
/// current plugins.
pub fn target_packs(
    lock: &Lock,
    version: &Version,
    target: Option<&[String]>,
    current: &[Loaded],
) -> Result<PackSet> {
    let names: Vec<String> = target
        .unwrap_or(&lock.packs)
        .iter()
        .filter(|name| {
            !lock.plugins.iter().any(|p| &p.name == *name)
                && !current.iter().any(|p| &p.meta.name == *name)
        })
        .cloned()
        .collect();
    overlay(version, &names, current)
}

/// Returns the upgrade versions, with a same-version step when the plugins changed (Go
/// `Runner.pluginChain`).
///
/// A non-empty `versions` is returned as it is. Otherwise a plugin that was added, removed,
/// or changed (version, hash, or API) gives the one step `[lock.template_version]`.
pub fn plugin_chain(lock: &Lock, current: &[Loaded], versions: Vec<String>) -> Vec<String> {
    if !versions.is_empty() {
        return versions;
    }
    let same = lock.plugins.len() == current.len()
        && current.iter().all(|p| {
            lock.plugins.iter().any(|entry| {
                entry.name == p.meta.name
                    && without_v(&entry.version) == without_v(&p.meta.version)
                    && entry.sha256 == p.sha256
                    && entry.api == i64::from(p.meta.api)
            })
        });
    if same {
        versions
    } else {
        vec![lock.template_version.clone()]
    }
}

/// Removes one `v` prefix (Go `strings.TrimPrefix(version, "v")`).
fn without_v(version: &str) -> &str {
    version.strip_prefix('v').unwrap_or(version)
}
