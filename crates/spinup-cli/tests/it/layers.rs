//! The layer test (rust-plan.md §1, ADR 0020 §5). It reads `cargo metadata` and fails on a
//! workspace edge that `docs/design/rust-layers.toml` does not allow.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

fn workspace_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .expect("crates/spinup-cli is two levels below the workspace root")
        .to_path_buf()
}

type Edges = BTreeMap<String, BTreeSet<String>>;

fn read_layers(root: &Path) -> (Edges, Edges) {
    let path = root.join("docs/design/rust-layers.toml");
    let text = std::fs::read_to_string(&path).expect("read docs/design/rust-layers.toml");
    let value: toml::Table = text.parse().expect("parse docs/design/rust-layers.toml");
    let table = |name: &str| -> Edges {
        value
            .get(name)
            .and_then(|v| v.as_table())
            .unwrap_or_else(|| panic!("rust-layers.toml has no [{name}] table"))
            .iter()
            .map(|(krate, deps)| {
                let deps = deps
                    .as_array()
                    .unwrap_or_else(|| panic!("[{name}] {krate} is not an array"))
                    .iter()
                    .map(|d| d.as_str().expect("crate names are strings").to_string())
                    .collect();
                (krate.clone(), deps)
            })
            .collect()
    };
    (table("normal"), table("dev"))
}

/// Returns (crate, dependency, kind) for every dependency between workspace crates.
#[allow(
    clippy::disallowed_methods,
    clippy::disallowed_types,
    reason = "the test runs cargo itself, not a project command"
)]
fn workspace_edges(root: &Path) -> (BTreeSet<String>, Vec<(String, String, String)>) {
    let cargo = option_env!("CARGO").unwrap_or("cargo");
    let output = std::process::Command::new(cargo)
        .args([
            "metadata",
            "--format-version",
            "1",
            "--no-deps",
            "--offline",
        ])
        .current_dir(root)
        .output()
        .expect("run cargo metadata");
    assert!(
        output.status.success(),
        "cargo metadata failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    let metadata: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode cargo metadata");
    let packages = metadata["packages"].as_array().expect("packages");
    let members: BTreeSet<String> = packages
        .iter()
        .map(|p| p["name"].as_str().expect("package name").to_string())
        .collect();
    let mut edges = Vec::new();
    for package in packages {
        let name = package["name"].as_str().expect("package name");
        for dep in package["dependencies"].as_array().expect("dependencies") {
            let dep_name = dep["name"].as_str().expect("dependency name");
            if !members.contains(dep_name) {
                continue;
            }
            let kind = dep["kind"].as_str().unwrap_or("normal").to_string();
            edges.push((name.to_string(), dep_name.to_string(), kind));
        }
    }
    (members, edges)
}

#[test]
fn workspace_edges_follow_rust_layers() {
    let root = workspace_root();
    let (normal, dev) = read_layers(&root);
    let (members, edges) = workspace_edges(&root);

    let keys: BTreeSet<String> = normal.keys().cloned().collect();
    assert_eq!(
        keys, members,
        "the [normal] keys of rust-layers.toml differ from the workspace members"
    );

    let mut bad = Vec::new();
    for (krate, dep, kind) in &edges {
        let allowed = normal.get(krate).is_some_and(|deps| deps.contains(dep))
            || (kind == "dev" && dev.get(krate).is_some_and(|deps| deps.contains(dep)));
        if !allowed {
            bad.push(format!("{krate} -> {dep} ({kind})"));
        }
        if dep == "spinup-cli" && krate != "spinup" {
            bad.push(format!(
                "{krate} -> spinup-cli: no crate depends on spinup-cli"
            ));
        }
    }
    assert!(
        bad.is_empty(),
        "edges that docs/design/rust-layers.toml does not allow:\n{}\nTry: remove the dependency, or ask the main loop to allow the edge",
        bad.join("\n")
    );
}
