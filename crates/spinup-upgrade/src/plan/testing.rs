//! Test fixtures of the planner: a fake pack renderer, a fake merger, template versions,
//! and locks (Go `planVersion`, `planMerger`, and `planLock`).

use std::path::Path;
use std::sync::{Arc, Mutex};

use anyhow::{Result, bail};
use serde_json::{Map, Value};
use spinup_exec::runner::Cancellation;
use spinup_manifest::hash::{hash, normalize};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_merge::contract::{Input, Merged, Merger};
use spinup_plugin_api::null_source::NullSource;
use spinup_tplstore::source::Version;
use spinup_vfs::plan::File;
use spinup_vfs::tree::Tree;

use super::PlanInput;
use crate::source::{PackRenderer, PackSet, Rendered, Templates};

/// One file of a fake template version.
#[derive(Debug, Clone)]
pub(crate) struct Spec {
    path: String,
    data: Vec<u8>,
    class: &'static str,
    from: String,
    mode: u32,
}

/// Returns a scaffold file with mode 0644.
pub(crate) fn spec(path: &str, data: impl AsRef<[u8]>) -> Spec {
    Spec {
        path: path.into(),
        data: data.as_ref().to_vec(),
        class: "scaffold",
        from: String::new(),
        mode: 0o644,
    }
}

impl Spec {
    pub(crate) fn class(mut self, class: &'static str) -> Self {
        self.class = class;
        self
    }

    pub(crate) fn from(mut self, from: &str) -> Self {
        self.from = from.into();
        self
    }

    pub(crate) fn mode(mut self, mode: u32) -> Self {
        self.mode = mode;
        self
    }
}

/// Builds a template version with the one pack `base`. The fake renderer reads the files
/// from `base/files/<class>/<path>` and the renames from `base/renames/<path>`.
pub(crate) fn version(name: &str, specs: &[Spec]) -> Version {
    version_with_stack(name, specs, Value::Object(Map::new()))
}

/// Builds a template version with stack versions decoded from `stack`.
pub(crate) fn version_with_stack(name: &str, specs: &[Spec], stack: Value) -> Version {
    let mut tree = Tree::new();
    tree.insert("base/pack.toml", b"name = 'base'\n".to_vec(), 0o644)
        .unwrap();
    for s in specs {
        tree.insert(
            format!("base/files/{}/{}", s.class, s.path),
            s.data.clone(),
            s.mode,
        )
        .unwrap();
        if !s.from.is_empty() {
            tree.insert(
                format!("base/renames/{}", s.path),
                s.from.as_bytes().to_vec(),
                0o644,
            )
            .unwrap();
        }
    }
    Version {
        version: name.into(),
        packs: Arc::new(tree),
        stack: Arc::new(serde_json::from_value(stack).unwrap()),
        kit_migration: None,
    }
}

/// Renders the fake packs of [`version`].
#[derive(Debug, Default)]
pub(crate) struct FakeRenderer;

impl PackRenderer for FakeRenderer {
    fn render(
        &self,
        version: &Version,
        packs: &[String],
        answers: &Map<String, Value>,
    ) -> Result<Rendered> {
        let mut rendered = Rendered {
            answers: answers.clone(),
            ..Rendered::default()
        };
        for pack in packs {
            if !version.packs.contains(&format!("{pack}/pack.toml")) {
                bail!(
                    "load pack {pack:?} from version {:?}: pack does not exist\nTry: select a version containing a valid pack {pack:?}",
                    version.version
                );
            }
            let tree = version.packs.sub(pack);
            for (path, file) in tree.sub("files").iter() {
                let (class, path) = path.split_once('/').unwrap();
                rendered.plan.add(File {
                    path: path.into(),
                    data: normalize(&file.data),
                    mode: file.mode,
                    class: class.into(),
                    template: format!("{pack}/{path}.tmpl"),
                    ..File::default()
                })?;
            }
            for (path, file) in tree.sub("renames").iter() {
                let from = String::from_utf8(file.data.to_vec()).unwrap();
                rendered.renames.insert(path.into(), from);
            }
        }
        Ok(rendered)
    }
}

/// One recorded merge: base, ours, and theirs.
pub(crate) type MergeCall = (Vec<u8>, Vec<u8>, Vec<u8>);

/// A merger that takes the one changed side, and marks a conflict when both sides changed.
#[derive(Debug, Default)]
pub(crate) struct FakeMerger {
    pub(crate) calls: Mutex<Vec<MergeCall>>,
    pub(crate) fail: bool,
}

impl FakeMerger {
    pub(crate) fn calls(&self) -> Vec<MergeCall> {
        self.calls.lock().unwrap().clone()
    }
}

impl Merger for FakeMerger {
    fn merge(&self, input: &Input<'_>, _cancel: &Cancellation) -> Result<Merged> {
        self.calls.lock().unwrap().push((
            input.base.to_vec(),
            input.ours.to_vec(),
            input.theirs.to_vec(),
        ));
        if self.fail {
            bail!("failed");
        }
        Ok(if input.ours == input.base {
            Merged {
                data: input.theirs.to_vec(),
                conflicted: false,
            }
        } else if input.theirs == input.base {
            Merged {
                data: input.ours.to_vec(),
                conflicted: false,
            }
        } else {
            let mut data = b"<<<<<<< ours\n".to_vec();
            data.extend_from_slice(input.ours);
            data.extend_from_slice(b"=======\n");
            data.extend_from_slice(input.theirs);
            data.extend_from_slice(b">>>>>>> theirs\n");
            Merged {
                data,
                conflicted: true,
            }
        })
    }
}

/// Returns a lock at 0.1.0 with the pack `base` and, with a class, the tracked file
/// `file.txt` whose hash is the hash of `data`.
pub(crate) fn lock(data: &str, class: Option<Class>) -> Lock {
    let mut lock = Lock::new();
    lock.template_version = "0.1.0".into();
    lock.packs = vec!["base".into()];
    if let Some(class) = class {
        lock.files.insert(
            "file.txt".into(),
            FileEntry {
                class,
                template: "base/file.txt.tmpl".into(),
                hash: hash(data.as_bytes()),
                mode: "0644".into(),
            },
        );
    }
    lock
}

/// Writes a project file with `mode`.
pub(crate) fn write(root: &Path, name: &str, data: impl AsRef<[u8]>, mode: u32) {
    let path = root.join(name);
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(&path, data).unwrap();
    set_mode(&path, mode);
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode)).unwrap();
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) {}

/// The shared parts of a plan input.
#[derive(Debug, Default)]
pub(crate) struct Fixture {
    pub(crate) renderer: FakeRenderer,
    pub(crate) plugins: NullSource,
    pub(crate) merger: FakeMerger,
    pub(crate) cancel: Cancellation,
    pub(crate) answers: Map<String, Value>,
    pub(crate) packs: PackSet,
    pub(crate) force_managed: bool,
}

impl Fixture {
    pub(crate) fn templates(&self) -> Templates<'_> {
        Templates {
            packs: &self.renderer,
            plugins: &self.plugins,
        }
    }

    pub(crate) fn input<'a>(
        &'a self,
        root: &'a Path,
        lock: &'a Lock,
        old: Option<&'a Version>,
        new: &'a Version,
    ) -> PlanInput<'a> {
        PlanInput {
            root,
            lock,
            old,
            new,
            old_answers: &self.answers,
            new_answers: &self.answers,
            old_packs: &self.packs,
            new_packs: &self.packs,
            templates: self.templates(),
            merger: Some(&self.merger),
            force_managed: self.force_managed,
            cancel: &self.cancel,
        }
    }

    /// Builds the next lock with the fixture answers and packs.
    pub(crate) fn next_lock(&self, old: &Lock, plan: &crate::contract::Plan, to: &Version) -> Lock {
        crate::lock::next_lock(&self.templates(), old, plan, to, &self.answers, &self.packs)
            .unwrap()
    }
}
