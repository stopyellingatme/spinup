//! The kit pins agree (ADR 0021 §2, §4). Main-loop test: it reads files only, offline.
//!
//! The kit manifest does not inherit from the workspace, so the `=` pins of the root
//! `[workspace.dependencies]` do not bind it. These checks are the only guard.

use std::collections::BTreeMap;
use std::path::PathBuf;

use toml_edit::{DocumentMut, Item};

const LOCK_ALIGN_TRY: &str = "Try: scripts/rust/lock-align.sh";
const PACK_LOCK_TRY: &str = "Try: make rs-pack-lock FETCH=1";

fn repo() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn read(path: &str) -> String {
    std::fs::read_to_string(repo().join(path)).unwrap_or_else(|e| panic!("read {path}: {e}"))
}

fn document(path: &str) -> DocumentMut {
    read(path)
        .parse::<DocumentMut>()
        .unwrap_or_else(|e| panic!("parse {path}: {e}"))
}

/// The version of a dependency item: `"x"` or `{ version = "x" }`. `None` for a path entry.
fn version(item: &Item) -> Option<String> {
    if let Some(text) = item.as_str() {
        return Some(text.to_string());
    }
    item.as_table_like()?
        .get("version")?
        .as_str()
        .map(str::to_string)
}

/// The root pins without `=`, by crate name.
fn root_pins() -> BTreeMap<String, String> {
    let root = document("Cargo.toml");
    let deps = root["workspace"]["dependencies"]
        .as_table_like()
        .expect("root [workspace.dependencies]");
    deps.iter()
        .filter_map(|(name, item)| {
            version(item).map(|v| (name.to_string(), v.trim_start_matches('=').to_string()))
        })
        .collect()
}

/// The direct dependencies of the kit (normal and dev), without the self dependency.
fn kit_requirements() -> BTreeMap<String, String> {
    let kit = document("crates/spinup-kit/Cargo.toml");
    let mut out = BTreeMap::new();
    for section in ["dependencies", "dev-dependencies"] {
        let Some(table) = kit.get(section).and_then(Item::as_table_like) else {
            continue;
        };
        for (name, item) in table.iter() {
            if name == "spinup-kit" {
                continue;
            }
            let v = version(item).unwrap_or_else(|| {
                panic!("the kit dependency {name} has no version\nTry: give it a caret version")
            });
            out.insert(name.to_string(), v);
        }
    }
    out
}

/// The versions of each package in a `Cargo.lock`.
fn lock_versions(path: &str) -> BTreeMap<String, Vec<String>> {
    let lock = document(path);
    let mut out: BTreeMap<String, Vec<String>> = BTreeMap::new();
    if let Some(packages) = lock.get("package").and_then(Item::as_array_of_tables) {
        for package in packages {
            let name = package["name"].as_str().expect("package name");
            let version = package["version"].as_str().expect("package version");
            out.entry(name.to_string())
                .or_default()
                .push(version.to_string());
        }
    }
    out
}

fn stack_crates() -> BTreeMap<String, String> {
    let stack =
        spinup_stack::load::parse(read("crates/spinup-stack/stackversions.yaml").as_bytes())
            .expect("parse crates/spinup-stack/stackversions.yaml");
    stack.crates.into_iter().collect()
}

fn fail(problems: &[String]) {
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

#[test]
fn kit_version_equals_the_stack_pin() {
    let kit = document("crates/spinup-kit/Cargo.toml");
    let version = kit["package"]["version"].as_str().expect("kit version");
    let stack = stack_crates();
    assert_eq!(
        stack.get("spinup-kit").map(String::as_str),
        Some(version),
        "crates.spinup-kit of crates/spinup-stack/stackversions.yaml must equal the kit version {version}\nTry: set crates.spinup-kit to {version}"
    );
}

#[test]
fn kit_requirements_equal_the_root_pins() {
    let pins = root_pins();
    let mut problems = Vec::new();
    for (name, requirement) in kit_requirements() {
        match pins.get(&name) {
            Some(pin) if *pin == requirement => {}
            Some(pin) => problems.push(format!(
                "kit dependency {name} requires {requirement}, the root pins {pin}\nTry: write {pin} in crates/spinup-kit/Cargo.toml"
            )),
            None => problems.push(format!(
                "kit dependency {name} has no pin in the root [workspace.dependencies]\nTry: add the pin to the root Cargo.toml"
            )),
        }
    }
    fail(&problems);
}

#[test]
fn root_lock_holds_the_kit_pins() {
    let pins = root_pins();
    let lock = lock_versions("Cargo.lock");
    let mut problems = Vec::new();
    for name in kit_requirements().keys() {
        let pin = &pins[name];
        let versions = lock.get(name).cloned().unwrap_or_default();
        if !versions.contains(pin) {
            problems.push(format!(
                "kit dependency {name}: the root Cargo.lock has {versions:?}, the pin is {pin}\n{LOCK_ALIGN_TRY}"
            ));
        }
    }
    fail(&problems);
}

#[test]
fn stack_crates_equal_the_root_pins() {
    let pins = root_pins();
    let kit = document("crates/spinup-kit/Cargo.toml");
    let kit_version = kit["package"]["version"].as_str().expect("kit version");
    let mut problems = Vec::new();
    for (name, version) in stack_crates() {
        let want = if name == "spinup-kit" {
            Some(kit_version)
        } else {
            pins.get(&name).map(String::as_str)
        };
        if want != Some(version.as_str()) {
            problems.push(format!(
                "crates.{name} of crates/spinup-stack/stackversions.yaml is {version}, the root pin is {want:?}\nTry: set crates.{name} to the root pin"
            ));
        }
    }
    fail(&problems);
}

#[test]
fn pack_lock_is_not_stale() {
    let pack = lock_versions("crates/spinup-packs/overlay/api/files/api/Cargo.lock");
    let root = lock_versions("Cargo.lock");
    let stack = stack_crates();
    let mut problems = Vec::new();
    match (pack.get("spinup-kit"), stack.get("spinup-kit")) {
        (Some(versions), Some(pin)) if versions == &vec![pin.clone()] => {}
        (versions, pin) => problems.push(format!(
            "the pack lock has spinup-kit {versions:?}, crates.spinup-kit is {pin:?}\n{PACK_LOCK_TRY}"
        )),
    }
    for (name, versions) in &pack {
        let (Some(other), [version]) = (root.get(name), versions.as_slice()) else {
            continue;
        };
        if let [other] = other.as_slice()
            && other != version
        {
            problems.push(format!(
                "{name}: the pack lock has {version}, the root Cargo.lock has {other}\n{PACK_LOCK_TRY}"
            ));
        }
    }
    fail(&problems);
}
