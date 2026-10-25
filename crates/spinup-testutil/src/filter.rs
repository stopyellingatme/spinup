//! Apply the file selections from the shared golden contract.

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
}

/// Parses the shared contract without changing its file selections.
pub fn parse(text: &str) -> Result<Config> {
    let config: Config = toml::from_str(text).map_err(|error| {
        anyhow::anyhow!(
            "read golden filters: {error}\nTry: correct testdata/golden/rust-filter.toml"
        )
    })?;
    for (name, case) in &config.golden {
        if !matches!(case.mode.as_str(), "keep" | "0644") {
            bail!(
                "read golden filter {name}: invalid mode {:?}\nTry: use keep or 0644",
                case.mode
            );
        }
    }
    Ok(config)
}

/// Loads the contract from the repository.
pub fn load() -> Result<Config> {
    let path = crate::repo::repo_root()?.join("testdata/golden/rust-filter.toml");
    let text = std::fs::read_to_string(path).map_err(|error| {
        anyhow::anyhow!(
            "read golden filters: {error}\nTry: restore testdata/golden/rust-filter.toml"
        )
    })?;
    parse(&text)
}

impl Config {
    /// Returns the named case, or reports a missing contract entry.
    pub fn case(&self, name: &str) -> Result<&Case> {
        self.golden.get(name).ok_or_else(|| anyhow::anyhow!(
            "select golden {name}: no filter entry\nTry: use a name from testdata/golden/rust-filter.toml"
        ))
    }

    /// Reports whether R1 excludes a file suffix.
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
            if self.skips(path) || case.exclude.iter().any(|p| p == path) {
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
}
