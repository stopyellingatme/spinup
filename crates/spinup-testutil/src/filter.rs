//! Apply the file selections from the shared golden contract.
//!
//! The R1 cases are in `testdata/golden/rust-filter.toml`. From R2, a unit that changes a
//! pack adds a case in `testdata/golden-rs/filter.d/<name>.toml` (ADR 0021 §10). A
//! `filter.d` case wins, its tree is `testdata/golden-rs/<name>`, and `skip_suffixes` does
//! not apply to it.

use anyhow::{Result, bail};
use serde::Deserialize;
use serde_json::{Map, Value};
use spinup_vfs::tree::Tree;
use std::collections::BTreeMap;

/// The shared golden selections and language exclusions.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    /// File suffixes excluded from R1 parity.
    pub skip_suffixes: Vec<String>,
    /// Each named golden case.
    pub golden: BTreeMap<String, Case>,
    /// The names that have a `rust-filter.toml` table and a `filter.d` case. The main loop
    /// removes the R1 table at the merge (ADR 0021 §10).
    #[serde(skip)]
    pub stale: Vec<String>,
}

/// Where a golden case comes from.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum Origin {
    /// `testdata/golden/rust-filter.toml`: the tree is `testdata/golden/<name>`.
    #[default]
    Go,
    /// `testdata/golden-rs/filter.d/*.toml`: the tree is `testdata/golden-rs/<name>`.
    Rs,
}

/// One file of `testdata/golden-rs/filter.d/`: golden cases only.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Fragment {
    #[serde(default)]
    golden: BTreeMap<String, Case>,
}

/// The render inputs and file selection for one golden tree.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Case {
    /// Packs in render order.
    pub packs: Vec<String>,
    /// Answers supplied to the renderer.
    pub answers: Map<String, Value>,
    /// Optional directory prefix removed from selected paths.
    pub sub: Option<String>,
    /// Included path prefixes.
    #[serde(default)]
    pub prefixes: Vec<String>,
    /// Included exact paths.
    #[serde(default)]
    pub paths: Vec<String>,
    /// Excluded exact paths.
    #[serde(default)]
    pub exclude: Vec<String>,
    /// Either `keep` or `0644`.
    pub mode: String,
    /// The file that declares the case.
    #[serde(skip)]
    pub origin: Origin,
}

/// Parses the shared contract without changing its file selections.
pub fn parse(text: &str) -> Result<Config> {
    let config: Config = toml::from_str(text).map_err(|error| {
        anyhow::anyhow!(
            "read golden filters: {error}\nTry: correct testdata/golden/rust-filter.toml"
        )
    })?;
    check_modes(&config.golden)?;
    Ok(config)
}

fn check_modes(cases: &BTreeMap<String, Case>) -> Result<()> {
    for (name, case) in cases {
        if !matches!(case.mode.as_str(), "keep" | "0644") {
            bail!(
                "read golden filter {name}: invalid mode {:?}\nTry: use keep or 0644",
                case.mode
            );
        }
    }
    Ok(())
}

/// Adds the cases of the `filter.d` files, given as (file name, text) in file-name order.
/// A `filter.d` case wins over a `rust-filter.toml` table, and the name goes to `stale`.
/// A name in two `filter.d` files is an error.
pub fn merge_fragments(config: &mut Config, fragments: &[(String, String)]) -> Result<()> {
    let mut origins: BTreeMap<String, String> = BTreeMap::new();
    for (file, text) in fragments {
        let fragment: Fragment = toml::from_str(text).map_err(|error| {
            anyhow::anyhow!(
                "read golden filter testdata/golden-rs/filter.d/{file}: {error}\nTry: correct the file"
            )
        })?;
        check_modes(&fragment.golden)?;
        for (name, mut case) in fragment.golden {
            if let Some(first) = origins.get(&name) {
                bail!(
                    "read golden filters: case {name:?} is in filter.d/{first} and filter.d/{file}\nTry: keep one case for each name"
                );
            }
            origins.insert(name.clone(), file.clone());
            case.origin = Origin::Rs;
            if config.golden.insert(name.clone(), case).is_some() {
                config.stale.push(name);
            }
        }
    }
    Ok(())
}

/// Loads the contract from the repository: `rust-filter.toml`, then the `filter.d` files.
pub fn load() -> Result<Config> {
    let root = crate::repo::repo_root()?;
    let path = root.join("testdata/golden/rust-filter.toml");
    let text = std::fs::read_to_string(path).map_err(|error| {
        anyhow::anyhow!(
            "read golden filters: {error}\nTry: restore testdata/golden/rust-filter.toml"
        )
    })?;
    let mut config = parse(&text)?;
    let dir = root.join(FILTER_D);
    let mut fragments = Vec::new();
    if dir.is_dir() {
        let mut names = Vec::new();
        for entry in std::fs::read_dir(&dir)? {
            let name = entry?.file_name().into_string().map_err(|_| {
                anyhow::anyhow!("read {FILTER_D}: a file name is not UTF-8\nTry: rename the file")
            })?;
            if name.ends_with(".toml") {
                names.push(name);
            }
        }
        names.sort();
        for name in names {
            let text = std::fs::read_to_string(dir.join(&name))?;
            fragments.push((name, text));
        }
    }
    merge_fragments(&mut config, &fragments)?;
    Ok(config)
}

/// The directory of the R2 golden cases, relative to the repository root.
pub const FILTER_D: &str = "testdata/golden-rs/filter.d";

impl Config {
    /// Returns the named case, or reports a missing contract entry.
    pub fn case(&self, name: &str) -> Result<&Case> {
        self.golden.get(name).ok_or_else(|| anyhow::anyhow!(
            "select golden {name}: no filter entry\nTry: use a name from testdata/golden/rust-filter.toml"
        ))
    }

    /// Reports whether R1 excludes a file suffix. It applies only to `Origin::Go` cases.
    pub fn skips(&self, path: &str) -> bool {
        self.skip_suffixes
            .iter()
            .any(|suffix| path.ends_with(suffix))
    }

    /// Selects rendered files and applies the Go test's permission policy.
    pub fn select(&self, name: &str, tree: &Tree) -> Result<Tree> {
        let case = self.case(name)?;
        let mut result = Tree::new();
        for (path, file) in tree.iter() {
            if (case.origin == Origin::Go && self.skips(path))
                || case.exclude.iter().any(|p| p == path)
            {
                continue;
            }
            let selected = if let Some(sub) = &case.sub {
                let prefix = format!("{sub}/");
                let Some(relative) = path.strip_prefix(&prefix) else {
                    continue;
                };
                relative
            } else {
                if !(case.prefixes.is_empty() && case.paths.is_empty())
                    && !case.prefixes.iter().any(|p| path.starts_with(p))
                    && !case.paths.iter().any(|p| p == path)
                {
                    continue;
                }
                path
            };
            result.insert(
                selected,
                file.data.clone(),
                if case.mode == "0644" {
                    0o644
                } else {
                    file.mode
                },
            )?;
        }
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_vfs::tree::Tree;

    #[test]
    fn selections_apply_paths_prefixes_exclusions_suffixes_and_modes() {
        let config = parse(
            r#"
            skip_suffixes = [".go"]
            [golden.example]
            packs = ["base"]
            answers = {}
            prefixes = ["web/"]
            paths = ["extra"]
            exclude = ["web/omit"]
            mode = "0644"
            [golden.subtree]
            packs = []
            answers = {}
            sub = "web"
            mode = "keep"
        "#,
        )
        .unwrap();
        let mut tree = Tree::new();
        for path in [
            "web/a",
            "web/a.go",
            "web/omit",
            "extra",
            "other",
            "webbing/a",
        ] {
            tree.insert(path, Vec::new(), 0o755).unwrap();
        }
        let selected = config.select("example", &tree).unwrap();
        assert_eq!(
            selected.iter().map(|(p, _)| p).collect::<Vec<_>>(),
            ["extra", "web/a"]
        );
        assert!(selected.iter().all(|(_, f)| f.mode == 0o644));
        let selected = config.select("subtree", &tree).unwrap();
        assert_eq!(
            selected.iter().map(|(p, _)| p).collect::<Vec<_>>(),
            ["a", "omit"]
        );
        assert!(selected.iter().all(|(_, f)| f.mode == 0o755));
        assert!(config.case("absent").is_err());
    }

    #[test]
    fn filter_d_cases_win_and_skip_no_suffix() {
        let mut config = parse(
            r#"
            skip_suffixes = [".go"]
            [golden.api]
            packs = ["base"]
            answers = {}
            mode = "keep"
        "#,
        )
        .unwrap();
        merge_fragments(
            &mut config,
            &[(
                "api.toml".into(),
                "[golden.api]\npacks = ['base', 'api']\nanswers = {}\nmode = '0644'\n[golden.new]\npacks = []\nanswers = {}\nmode = 'keep'\n".into(),
            )],
        )
        .unwrap();
        assert_eq!(config.stale, ["api"]);
        let case = config.case("api").unwrap();
        assert_eq!(case.origin, Origin::Rs);
        assert_eq!(case.packs, ["base", "api"]);
        assert_eq!(config.case("new").unwrap().origin, Origin::Rs);
        let mut tree = Tree::new();
        tree.insert("main.go", Vec::new(), 0o644).unwrap();
        let selected = config.select("api", &tree).unwrap();
        assert!(selected.contains("main.go"));
        let error = merge_fragments(
            &mut config,
            &[
                (
                    "a.toml".into(),
                    "[golden.x]\npacks = []\nanswers = {}\nmode = 'keep'\n".into(),
                ),
                (
                    "b.toml".into(),
                    "[golden.x]\npacks = []\nanswers = {}\nmode = 'keep'\n".into(),
                ),
            ],
        )
        .unwrap_err()
        .to_string();
        assert!(
            error.contains("filter.d/a.toml and filter.d/b.toml"),
            "{error}"
        );
        assert!(error.contains("\nTry:"), "{error}");
        assert!(
            merge_fragments(
                &mut config,
                &[("c.toml".into(), "skip_suffixes = []".into())]
            )
            .is_err()
        );
    }

    /// The contract files of the repository agree (ADR 0021 §10).
    #[test]
    fn repository_contract() {
        let config = load().unwrap();
        let root = crate::repo::repo_root().unwrap();
        for (name, case) in &config.golden {
            if case.origin == Origin::Rs {
                assert!(
                    root.join("testdata/golden-rs").join(name).is_dir(),
                    "filter.d case {name} has no tree in testdata/golden-rs/{name}"
                );
            }
        }
        let parity = std::fs::read_to_string(root.join("testdata/golden/rust-parity.txt")).unwrap();
        let text = std::fs::read_to_string(root.join("testdata/golden/rust-filter.toml")).unwrap();
        let r1 = parse(&text).unwrap();
        for line in parity.lines() {
            let name = line.split('#').next().unwrap_or("").trim();
            if name.is_empty() {
                continue;
            }
            assert!(
                r1.golden
                    .keys()
                    .any(|key| key == name || key.starts_with(&format!("{name}/"))),
                "rust-parity.txt names {name}, which has no rust-filter.toml table"
            );
        }
        for name in &config.stale {
            println!("stale: {name} has a rust-filter.toml table and a filter.d case");
        }
    }
}
