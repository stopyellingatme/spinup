//! R0.1 compares YAML editors against committed Go outputs.
use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, fs, path::PathBuf};

#[cfg(feature = "edit")]
mod edit;
#[cfg(feature = "path")]
mod path;
#[cfg(feature = "splice")]
mod splice;

#[derive(Debug, Deserialize)]
pub struct Action {
    #[serde(default)]
    pub block: bool,
    pub op: String,
    pub path: Vec<String>,
    pub value: String,
}

#[derive(Debug, Deserialize)]
pub struct Fixture {
    pub id: String,
    pub family: String,
    pub mode: String,
    pub actions: Vec<Action>,
    pub comments: Vec<String>,
    #[serde(skip)]
    pub input: String,
    #[serde(skip)]
    pub expected: String,
}

#[derive(Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Observation {
    pub byte_equal: bool,
    pub comments_kept: bool,
    pub pass: bool,
    pub detail: String,
}

pub type Matrix = BTreeMap<String, BTreeMap<String, Observation>>;
type Editor = fn(&str, &[Action]) -> Result<String>;

pub fn fixtures() -> Result<Vec<Fixture>> {
    let mut dirs = fs::read_dir(PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("fixtures"))?
        .collect::<std::io::Result<Vec<_>>>()?;
    dirs.sort_by_key(|d| d.file_name());
    let mut out = Vec::new();
    for d in dirs {
        if !d.file_type()?.is_dir() {
            continue;
        }
        let mut f: Fixture =
            serde_json::from_str(&fs::read_to_string(d.path().join("edit.json"))?)?;
        f.input = fs::read_to_string(d.path().join("input.yaml"))?;
        f.expected = fs::read_to_string(d.path().join("expected.yaml"))?;
        out.push(f);
    }
    Ok(out)
}

pub fn measure() -> Result<Matrix> {
    let editors: &[(&str, Editor)] = &[
        #[cfg(feature = "path")]
        ("yamlpath+yamlpatch", path::apply),
        #[cfg(feature = "edit")]
        ("yaml-edit", edit::apply),
        #[cfg(feature = "splice")]
        ("tree-sitter splice", splice::apply),
    ];
    let mut matrix = Matrix::new();
    for f in fixtures()? {
        let mut row = BTreeMap::new();
        for &(name, apply) in editors {
            let result = apply(&f.input, &f.actions).with_context(|| format!("{}: {name}", f.id));
            let observation = match result {
                Ok(output) => {
                    let byte_equal = output == f.expected;
                    let comments_kept = f.comments.iter().all(|c| output.contains(c));
                    let detail = if byte_equal {
                        String::new()
                    } else {
                        let offset = output
                            .bytes()
                            .zip(f.expected.bytes())
                            .position(|(a, b)| a != b)
                            .unwrap_or(output.len().min(f.expected.len()));
                        format!(
                            "first unequal byte {offset}; actual {} bytes; expected {} bytes",
                            output.len(),
                            f.expected.len()
                        )
                    };
                    Observation {
                        byte_equal,
                        comments_kept,
                        pass: byte_equal && comments_kept,
                        detail,
                    }
                }
                Err(error) => Observation {
                    byte_equal: false,
                    comments_kept: false,
                    pass: false,
                    detail: format!("{error:#}"),
                },
            };
            row.insert(name.to_string(), observation);
        }
        matrix.insert(f.id, row);
    }
    Ok(matrix)
}

#[cfg(all(test, feature = "path", feature = "edit", feature = "splice"))]
mod tests {
    use super::*;

    #[test]
    fn every_candidate_runs_against_every_go_fixture() -> Result<()> {
        let fs = fixtures()?;
        assert!(fs.len() >= 25);
        for family in ["sops", "alert", "compose", "userdata", "rotation"] {
            assert!(fs.iter().filter(|f| f.family == family).count() >= 4);
        }
        let measured = measure()?;
        assert!(measured.values().all(|row| row.len() == 3));
        let baseline: Matrix = serde_json::from_str(include_str!("../observations.json"))?;
        assert_eq!(
            measured, baseline,
            "candidate results changed; inspect the outputs before updating the baseline"
        );
        Ok(())
    }

    #[test]
    fn comment_retention_is_independent_of_byte_equality() -> Result<()> {
        let matrix = measure()?;
        assert!(
            matrix
                .values()
                .flat_map(|r| r.values())
                .any(|o| o.comments_kept && !o.byte_equal)
        );
        let fixture = fixtures()?
            .into_iter()
            .find(|f| f.id == "compose-01")
            .unwrap();
        assert!(
            fixture
                .comments
                .iter()
                .any(|c| !fixture.expected.contains(c)),
            "the real Go map writer drops comments"
        );
        Ok(())
    }
}
