//! Structured manifest edits and precise dependency updates.

use std::collections::BTreeMap;

use anyhow::{Result, anyhow, bail};
use spinup_compat::version::Version;
use spinup_exec::runner::Cmd;
use spinup_project::webtool::Tool;
use toml_edit::{Array, DocumentMut, Item, Table, Value};

use super::{command, conflict, exists, parse, print, read, write};
use crate::contract::Step;
use crate::source::{PackRenderer, Packs};

pub(super) fn run(s: &mut Step<'_>) -> Result<()> {
    if exists(&s.worktree, "api/Cargo.toml")? {
        cargo(s)?;
    }
    web(s)
}

fn cargo(s: &mut Step<'_>) -> Result<()> {
    let path = s.worktree.join("api/Cargo.toml");
    let mut document = read(&path)?;
    let original = document.clone();
    let new = s
        .plan
        .as_ref()
        .and_then(|plan| plan.files.iter().find(|f| f.path == "api/Cargo.toml"))
        .map(|f| {
            std::str::from_utf8(&f.data)
                .map_err(|e| {
                    anyhow!("read the new api/Cargo.toml: {e}\nTry: repair the template manifest")
                })
                .and_then(|text| parse(text, "the new api/Cargo.toml"))
        })
        .transpose()?;
    let old = old_render(s)?;
    let mut raised = BTreeMap::new();
    edit(
        &mut document,
        old.as_ref(),
        new.as_ref(),
        &s.to.stack,
        &mut raised,
    )?;
    write(&path, &document)?;
    let before = direct_versions(original.as_item());
    let after = direct_versions(document.as_item());
    // Feature or dependency additions can need resolution without a version increase.
    // Resolve once before pinning new dependencies. Do not update the whole workspace.
    if raised.is_empty() && graph(&original) != graph(&document) {
        command(
            s,
            offline(
                s,
                Cmd::new("cargo").args(["metadata", "--format-version", "1"]),
            ),
        )?;
    }
    for (name, (from, pin)) in raised {
        // A previous update may already have resolved another raised dependency.
        let lock = if exists(&s.worktree, "api/Cargo.lock")? {
            Some(read(&s.worktree.join("api/Cargo.lock"))?)
        } else {
            None
        };
        let from = locked_version(lock.as_ref(), &name, &pin)
            .or_else(|| locked_version(lock.as_ref(), &name, &from))
            .unwrap_or(from);
        print(s, &format!("Dependency {name}: {from} -> {pin}\n"))?;
        command(
            s,
            offline(
                s,
                Cmd::new("cargo").args([
                    "update",
                    "-p",
                    &format!("{name}@{from}"),
                    "--precise",
                    &pin,
                ]),
            ),
        )?;
    }
    for (name, pin) in &after {
        if !before.contains_key(name) && s.to.stack.crates.contains_key(name) {
            command(
                s,
                offline(
                    s,
                    Cmd::new("cargo").args(["update", "-p", name, "--precise", bare(pin)]),
                ),
            )?;
        }
    }
    if let Some(kit) = after.get("spinup-kit") {
        s.lock.kit.crate_name = "spinup-kit".into();
        s.lock.kit.version = bare(kit).into();
    }
    Ok(())
}

fn offline(s: &Step<'_>, mut cmd: Cmd) -> Cmd {
    cmd.dir = Some(s.worktree.join("api"));
    if s.options.offline {
        cmd.args.push("--offline".into());
    }
    cmd
}

fn graph(doc: &DocumentMut) -> String {
    [
        "dependencies",
        "dev-dependencies",
        "build-dependencies",
        "features",
        "target",
    ]
    .iter()
    .filter_map(|key| doc.get(key))
    .map(ToString::to_string)
    .collect()
}

fn direct_versions(item: &Item) -> BTreeMap<String, String> {
    let mut versions = BTreeMap::new();
    for section in SECTIONS {
        if let Some(deps) = item.get(section).and_then(Item::as_table_like) {
            for (alias, dep) in deps.iter() {
                let name = dep.get("package").and_then(Item::as_str).unwrap_or(alias);
                if let Some(version) = dep
                    .as_str()
                    .or_else(|| dep.get("version").and_then(Item::as_str))
                {
                    versions.insert(name.into(), version.into());
                }
            }
        }
    }
    if let Some(targets) = item.get("target").and_then(Item::as_table_like) {
        for (_, target) in targets.iter() {
            versions.extend(direct_versions(target));
        }
    }
    versions
}

fn locked_version(lock: Option<&DocumentMut>, name: &str, requirement: &str) -> Option<String> {
    let packages = lock?.get("package")?.as_array_of_tables()?;
    let candidates: Vec<&str> = packages
        .iter()
        .filter(|p| p.get("name").and_then(Item::as_str) == Some(name))
        .filter_map(|p| p.get("version").and_then(Item::as_str))
        .collect();
    if candidates.len() == 1 {
        return Some(candidates[0].into());
    }
    candidates
        .into_iter()
        .find(|v| *v == bare(requirement))
        .map(str::to_owned)
}

fn old_render(s: &Step<'_>) -> Result<Option<DocumentMut>> {
    if !s.from.packs.contains("api/pack.toml") {
        return Ok(None);
    }
    // The state machine has already built the new in-memory lock. The disk lock
    // still contains the old answers until the lock phase.
    let locked = if s.worktree.join("spinup.lock").is_file() {
        spinup_manifest::lock::Lock::load(s.worktree.join("spinup.lock"))?
    } else {
        s.lock.clone()
    };
    let answers = crate::lock::answers(&locked)?;
    let rendered = Packs.render(&s.from, &["api".into()], &answers)?;
    rendered
        .plan
        .get("api/Cargo.toml")
        .map(|file| {
            let text = String::from_utf8(file.data).map_err(|e| {
                anyhow!("read the old api/Cargo.toml: {e}\nTry: restore the old template version")
            })?;
            parse(&text, "the old api/Cargo.toml")
        })
        .transpose()
}

type Raised = BTreeMap<String, (String, String)>;

pub(super) fn edit(
    disk: &mut DocumentMut,
    old: Option<&DocumentMut>,
    new: Option<&DocumentMut>,
    stack: &spinup_stack::stack::Stack,
    raised: &mut Raised,
) -> Result<()> {
    validate(disk.as_item())?;
    if let Some(new) = new {
        validate(new.as_item())?;
        merge_sections(disk.as_item_mut(), new.as_item())?;
        if let Some(features) = new.get("features").and_then(Item::as_table_like) {
            if !disk.contains_key("features") {
                disk["features"] = Item::Table(Table::new());
            }
            for (name, feature) in features.iter() {
                if name == "default" {
                    let mut defaults = feature.as_array().cloned().unwrap_or_default();
                    let baseline = old
                        .and_then(|doc| doc.get("features"))
                        .and_then(|f| f.get("default"))
                        .and_then(Item::as_array);
                    if let Some(current) = disk["features"].get("default").and_then(Item::as_array)
                    {
                        for value in current.iter().filter_map(Value::as_str) {
                            if !baseline.is_some_and(|list| has(list, value))
                                && !has(&defaults, value)
                            {
                                defaults.push(value);
                            }
                        }
                    }
                    disk["features"]["default"] = toml_edit::value(defaults);
                } else if disk["features"].get(name).is_none() {
                    disk["features"][name] = feature.clone();
                } else {
                    merge_array(&mut disk["features"][name], feature)?;
                }
            }
        }
    }
    raise_sections(disk.as_item_mut(), stack, raised)?;
    if !stack.rust.min.is_empty() {
        let current = disk
            .get("package")
            .and_then(|p| p.get("rust-version"))
            .and_then(Item::as_str);
        let target = version(&stack.rust.min)?;
        if current.map(version).transpose()?.is_none_or(|v| v < target) {
            disk["package"]["rust-version"] = toml_edit::value(&stack.rust.min);
        }
    }
    Ok(())
}

fn validate(item: &Item) -> Result<()> {
    if item.as_table_like().is_none() {
        bail!(
            "edit api/Cargo.toml: a manifest section must be a table\nTry: repair the manifest section"
        );
    }
    for key in [
        "package",
        "features",
        "dependencies",
        "dev-dependencies",
        "build-dependencies",
        "target",
    ] {
        if let Some(value) = item.get(key) {
            let table = value.as_table_like().ok_or_else(|| {
                anyhow!(
                    "edit api/Cargo.toml: {key} must be a table\nTry: repair the manifest table"
                )
            })?;
            if key == "features" {
                for (name, feature) in table.iter() {
                    if !feature
                        .as_array()
                        .is_some_and(|array| array.iter().all(|v| v.is_str()))
                    {
                        bail!(
                            "edit api/Cargo.toml: feature {name} must be an array of strings\nTry: repair the manifest features"
                        );
                    }
                }
            } else if key == "target" {
                for (_, target) in table.iter() {
                    validate(target)?;
                }
            } else if SECTIONS.contains(&key) {
                for (name, dep) in table.iter() {
                    if !dep.is_str() && dep.as_table_like().is_none() {
                        bail!(
                            "edit api/Cargo.toml: dependency {name} must be a string or table\nTry: repair the dependency declaration"
                        );
                    }
                }
            }
        }
    }
    Ok(())
}

const SECTIONS: [&str; 3] = ["dependencies", "dev-dependencies", "build-dependencies"];

fn merge_sections(disk: &mut Item, new: &Item) -> Result<()> {
    for section in SECTIONS {
        let Some(deps) = new.get(section).and_then(Item::as_table_like) else {
            continue;
        };
        if disk.get(section).is_none() {
            disk[section] = Item::Table(Table::new());
        }
        for (name, dep) in deps.iter() {
            if disk[section].get(name).is_none() {
                disk[section][name] = dep.clone();
            } else {
                merge_dependency(&mut disk[section][name], dep)?;
            }
        }
    }
    if let Some(targets) = new.get("target").and_then(Item::as_table_like) {
        for (name, target) in targets.iter() {
            if disk.get("target").is_none() {
                disk["target"] = Item::Table(Table::new());
            }
            if disk["target"].get(name).is_none() {
                disk["target"][name] = Item::Table(Table::new());
            }
            merge_sections(&mut disk["target"][name], target)?;
        }
    }
    Ok(())
}

fn merge_dependency(disk: &mut Item, new: &Item) -> Result<()> {
    let Some(table) = new.as_table_like() else {
        return Ok(());
    };
    if let Some(version) = disk.as_str() {
        let mut expanded = toml_edit::InlineTable::new();
        expanded.insert("version", Value::from(version));
        *disk = Item::Value(Value::InlineTable(expanded));
    }
    for (key, item) in table.iter() {
        if disk.get(key).is_none() {
            disk[key] = item.clone();
        } else if key == "features" {
            merge_array(&mut disk[key], item)?;
        }
    }
    Ok(())
}

fn merge_array(disk: &mut Item, new: &Item) -> Result<()> {
    let Some(additions) = new.as_array() else {
        return Ok(());
    };
    let list = disk.as_array_mut().ok_or_else(|| {
        anyhow!("edit api/Cargo.toml: features must be an array\nTry: repair the manifest features")
    })?;
    for feature in additions.iter().filter_map(Value::as_str) {
        if !has(list, feature) {
            list.push(feature);
        }
    }
    Ok(())
}

fn has(list: &Array, value: &str) -> bool {
    list.iter().any(|entry| entry.as_str() == Some(value))
}

fn raise_sections(
    disk: &mut Item,
    stack: &spinup_stack::stack::Stack,
    raised: &mut Raised,
) -> Result<()> {
    for section in SECTIONS {
        let Some(deps) = disk.get_mut(section).and_then(Item::as_table_like_mut) else {
            continue;
        };
        for (alias, dep) in deps.iter_mut() {
            let name = dep
                .get("package")
                .and_then(Item::as_str)
                .unwrap_or(&alias)
                .to_owned();
            let Some(pin) = stack.crates.get(&name) else {
                continue;
            };
            let Some(current) = dep
                .as_str()
                .or_else(|| dep.get("version").and_then(Item::as_str))
            else {
                continue;
            };
            let current = current.to_owned();
            if version(&current)? >= version(pin)? {
                continue;
            }
            let from = bare(&current).to_owned();
            let prefix = current
                .strip_prefix(['=', '~', '^'])
                .map_or("", |_| &current[..1]);
            if dep.is_str() {
                *dep = toml_edit::value(format!("{prefix}{pin}"));
            } else {
                dep["version"] = toml_edit::value(format!("{prefix}{pin}"));
            }
            raised.insert(name, (from, pin.clone()));
        }
    }
    if let Some(targets) = disk.get_mut("target").and_then(Item::as_table_like_mut) {
        for (_, target) in targets.iter_mut() {
            raise_sections(target, stack, raised)?;
        }
    }
    Ok(())
}

fn bare(text: &str) -> &str {
    text.trim().trim_start_matches(['=', '^', '~']).trim()
}

fn version(text: &str) -> Result<Version> {
    let core = bare(text).split('+').next().unwrap_or(text);
    let full = match core.matches('.').count() {
        0 => format!("{core}.0.0"),
        1 => format!("{core}.0"),
        _ => core.to_owned(),
    };
    Version::parse(&full).map_err(|e| {
        anyhow!("compare dependency version {text:?}: {e}\nTry: use a valid version in api/Cargo.toml and the target stack")
    })
}

pub(super) fn tool(s: &Step<'_>) -> Tool {
    if s.answers.is_empty() {
        return Tool::from_lock(Some(&s.lock));
    }
    Tool::new(
        s.answers
            .get("package_manager")
            .and_then(serde_json::Value::as_str)
            .unwrap_or(""),
        s.answers
            .get("api_client")
            .and_then(serde_json::Value::as_str)
            .unwrap_or(""),
    )
}

fn web(s: &mut Step<'_>) -> Result<()> {
    if !exists(&s.worktree, "web/package.json")? {
        return Ok(());
    }
    let tool = tool(s);
    if conflict(s, "web/package.json") {
        return print(
            s,
            &format!(
                "Skipped web/{}: web/package.json has conflicts. Resolve them, then run {} install in web/.\n",
                tool.lockfile(),
                tool.name()
            ),
        );
    }
    let result = command(s, tool.refresh_lock().dir(s.worktree.join("web")));
    if let Err(e) = result {
        bail!(
            "update web/{}: {e:#}\nTry: run {} install in web/ and then spinup upgrade --continue",
            tool.lockfile(),
            tool.name()
        );
    }
    Ok(())
}
