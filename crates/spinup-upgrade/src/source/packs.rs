//! The real [`PackRenderer`]: the packs of a template version through `spinup-packs` and
//! `spinup-render` (Go `upgrade.Render`, `loadPlanPacks`, and `RenamedFrom`).

use std::collections::BTreeMap;
use std::sync::Arc;

use anyhow::{Result, anyhow, bail};
use serde_json::{Map, Value};
use spinup_packs::loader::{FileSpec, Pack, load};
use spinup_render::answers::validate_answers;
use spinup_render::render::{Input, render};
use spinup_tplstore::source::Version;
use spinup_vfs::tree::Tree;

use super::{PackRenderer, Rendered};

/// Renders the core packs of a template version with the minijinja packs of the version.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct Packs;

impl PackRenderer for Packs {
    fn render(
        &self,
        version: &Version,
        packs: &[String],
        answers: &Map<String, Value>,
    ) -> Result<Rendered> {
        let loaded = load_packs(&version.packs, &version.version, packs)?;
        let answers = validate_answers(&loaded, answers)?;
        let plan = render(input(&loaded, &answers, version))?;
        let mut renames = BTreeMap::new();
        for pack in &loaded {
            for spec in pack.files.iter().filter(|s| !s.renamed_from.is_empty()) {
                let Some((target, source)) = rename(pack, spec, &answers, version)? else {
                    continue;
                };
                if renames.insert(target.clone(), source).is_some() {
                    bail!(
                        "render rename {target:?}: duplicate destination\nTry: give each rename a unique destination"
                    );
                }
            }
        }
        Ok(Rendered {
            plan,
            answers,
            renames,
        })
    }
}

/// Loads the named packs of a pack tree (Go `loadPlanPacks`). The error names the pack and
/// the version.
pub(crate) fn load_packs(tree: &Tree, version: &str, names: &[String]) -> Result<Vec<Pack>> {
    names
        .iter()
        .map(|name| {
            load(tree, name).map_err(|e| {
                anyhow!(
                    "load pack {name:?} from version {version:?}: {e:#}\nTry: select a version containing a valid pack {name:?}"
                )
            })
        })
        .collect()
}

fn input<'a>(
    packs: &'a [Pack],
    answers: &'a Map<String, Value>,
    version: &'a Version,
) -> Input<'a> {
    Input {
        packs,
        answers,
        stack: Some(&version.stack),
        template_version: &version.version,
    }
}

/// Renders the output path and the `renamed_from` path of one file spec with the render
/// environment. It returns `None` when the `when` condition disables the file. The render
/// reads an empty source, so the template content is not rendered again.
fn rename(
    pack: &Pack,
    spec: &FileSpec,
    answers: &Map<String, Value>,
    version: &Version,
) -> Result<Option<(String, String)>> {
    const SOURCE: &str = "renamed-from";
    let mut fs = Tree::new();
    fs.insert(SOURCE, Arc::<[u8]>::from(&b""[..]), 0o644)?;
    let path = |path: &str| FileSpec {
        path: path.to_string(),
        source: SOURCE.into(),
        renamed_from: String::new(),
        ..spec.clone()
    };
    let single = Pack {
        name: pack.name.clone(),
        files: vec![path(&spec.path), path(&spec.renamed_from)],
        fs,
        ..Pack::default()
    };
    let plan = render(input(std::slice::from_ref(&single), answers, version)).map_err(|e| {
        anyhow!(
            "render rename of {}/{}: {e:#}\nTry: correct the renamed_from template",
            pack.name,
            spec.source
        )
    })?;
    Ok(match plan.files.as_slice() {
        [target, source] => Some((target.path.clone(), source.path.clone())),
        _ => None,
    })
}
