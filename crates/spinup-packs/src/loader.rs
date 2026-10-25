//! Load pack metadata and ordered fragments from a read-only tree.

use std::collections::BTreeMap;

use anyhow::{Result, bail};
use serde::Deserialize;
use spinup_vfs::tree::Tree;

use crate::when::{Condition, quote};

/// One answer declaration and its command flag.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Question {
    /// The answer key and flag name.
    pub key: String,
    /// The text shown when asking for an answer.
    pub prompt: String,
    /// The answer type: string, bool, or enum.
    pub r#type: String,
    /// The textual default, including bool defaults.
    pub default: String,
    /// Whether the metadata explicitly declares a default, including an empty one.
    pub default_set: bool,
    /// The regular expression for a string answer.
    pub pattern: String,
    /// The choices for an enum answer.
    pub choices: Vec<String>,
}

impl Question {
    /// Reports whether the question declares a default.
    pub fn has_default(&self) -> bool {
        self.default_set || !self.default.is_empty()
    }
}

/// A source file, its output path, and its upgrade policy.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct FileSpec {
    /// The output path, which can contain template expressions.
    pub path: String,
    /// The source path relative to the pack directory.
    pub source: String,
    /// The upgrade policy: managed, scaffold, or seed.
    pub class: String,
    /// The optional condition for including the file.
    pub when: String,
    /// The version that introduced the file.
    pub since: String,
    /// The previous output path.
    pub renamed_from: String,
    /// Four octal permission digits, or an empty string for 0644.
    pub mode: String,
    /// The compiled condition. Direct constructions can leave this empty.
    #[serde(skip)]
    pub condition: Option<Condition>,
}

impl FileSpec {
    /// Evaluates the condition against an answer map.
    /// Accepts `&serde_json::Map<String, Value>` directly.
    pub fn matches<'de, D>(
        &self,
        answers: impl IntoIterator<Item = (&'de String, D)>,
    ) -> Result<bool>
    where
        D: serde::Deserializer<'de>,
    {
        if let Some(condition) = &self.condition
            && condition.source() == self.when
        {
            return Ok(condition.matches(answers)?);
        }
        Ok(Condition::parse(&self.when)?.matches(answers)?)
    }
}

/// Merged metadata and source files rooted at one pack directory.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Pack {
    /// The declared name, without the leading underscore of an internal pack.
    pub name: String,
    /// The pack description.
    pub description: String,
    /// The required packs.
    pub requires: Vec<String>,
    /// The questions, in declaration order.
    pub questions: Vec<Question>,
    /// The files, in declaration order.
    pub files: Vec<FileSpec>,
    /// The source tree rooted at the pack directory.
    pub fs: Tree,
}

#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct RawQuestion {
    key: String,
    prompt: String,
    r#type: String,
    default: Option<toml::Value>,
    pattern: String,
    choices: Vec<String>,
}

#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Metadata {
    name: Option<String>,
    description: Option<String>,
    requires: Option<Vec<String>>,
    questions: Vec<RawQuestion>,
    files: Vec<FileSpec>,
}

/// Reads `pack.toml`, then `pack.d/*.toml` in file-name order.
/// The tree is rooted at the directory that contains all packs.
pub fn load(tree: &Tree, name: &str) -> Result<Pack> {
    if !valid_name(name) {
        bail!(
            "load pack {}: invalid file system or name\nTry: use a pack directory name",
            quote(name)
        );
    }
    let mut pack = Pack {
        fs: tree.sub(name),
        ..Pack::default()
    };
    let mut sources = vec!["pack.toml".to_string()];
    sources.extend(
        pack.fs
            .children("pack.d")
            .into_iter()
            .filter(|path| path.ends_with(".toml"))
            .map(|path| format!("pack.d/{path}")),
    );
    let mut question_sources = BTreeMap::new();
    let mut file_sources = BTreeMap::new();
    for (i, source) in sources.iter().enumerate() {
        let origin = format!("{name}/{source}");
        let data = pack.fs.get(source).ok_or_else(|| {
            anyhow::anyhow!(
                "read pack {origin}: file does not exist\nTry: provide a readable pack file"
            )
        })?;
        let meta = std::str::from_utf8(&data.data)
            .map_err(|error| {
                anyhow::anyhow!("decode pack {origin}: {error}\nTry: correct the pack TOML")
            })
            .and_then(|text| {
                toml::from_str::<Metadata>(text).map_err(|error| {
                    anyhow::anyhow!("decode pack {origin}: {error}\nTry: correct the pack TOML")
                })
            })?;
        if i == 0 {
            // Go removes one leading underscore for internal fixture packs.
            let expected = name.strip_prefix('_').unwrap_or(name);
            if meta.name.as_deref() != Some(expected) {
                bail!(
                    "load pack {}: name must equal {}\nTry: correct name in pack.toml",
                    quote(name),
                    quote(expected)
                );
            }
            pack.name = expected.into();
            pack.description = meta.description.unwrap_or_default();
            pack.requires = meta.requires.unwrap_or_default();
        } else if meta.name.is_some() || meta.description.is_some() || meta.requires.is_some() {
            bail!(
                "load fragment {origin}: fragments may only add questions and files\nTry: keep pack metadata in pack.toml"
            );
        }
        for raw in meta.questions {
            check_duplicate(&mut question_sources, "question", &raw.key, &origin)?;
            let default_set = raw.default.is_some();
            let default = match raw.default {
                None => String::new(),
                Some(toml::Value::String(value)) => value,
                Some(toml::Value::Boolean(value)) => {
                    if raw.r#type != "bool" {
                        bail!(
                            "load question {} in {origin}: bool default needs type bool\nTry: correct the question type",
                            quote(&raw.key)
                        );
                    }
                    value.to_string()
                }
                _ => bail!(
                    "load question {} in {origin}: default must be a string or bool\nTry: correct the default type",
                    quote(&raw.key)
                ),
            };
            pack.questions.push(Question {
                key: raw.key,
                prompt: raw.prompt,
                r#type: raw.r#type,
                default,
                default_set,
                pattern: raw.pattern,
                choices: raw.choices,
            });
        }
        for file in meta.files {
            check_duplicate(&mut file_sources, "output path", &file.path, &origin)?;
            pack.files.push(file);
        }
    }
    pack.validate()
        .map_err(|error| anyhow::anyhow!("load pack {}: {error}", quote(name)))?;
    Ok(pack)
}

fn check_duplicate(
    sources: &mut BTreeMap<String, String>,
    kind: &str,
    key: &str,
    origin: &str,
) -> Result<()> {
    if let Some(first) = sources.get(key) {
        bail!(
            "duplicate {kind} {} in {first} and {origin}\nTry: give each {kind} a unique name",
            quote(key)
        );
    }
    sources.insert(key.into(), origin.into());
    Ok(())
}

/// Returns sorted directory names that contain `pack.toml`, including internal packs.
pub fn list(tree: &Tree) -> Result<Vec<String>> {
    Ok(tree
        .children("")
        .into_iter()
        .filter(|name| tree.contains(&format!("{name}/pack.toml")))
        .collect())
}

fn valid_name(name: &str) -> bool {
    !matches!(name, "" | "_" | "." | "..") && !name.contains(['/', '\\', '\0'])
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_vfs::tree::Tree;

    fn fixture(entries: &[(&str, &str)]) -> Tree {
        let mut tree = Tree::new();
        for (path, data) in entries {
            tree.insert(*path, data.as_bytes().to_vec(), 0o644).unwrap();
        }
        tree
    }

    #[test]
    fn load_fragments() {
        let tree = fixture(&[
            (
                "example/pack.toml",
                "name='example'\n[[questions]]\nkey='app_name'\ntype='string'\ndefault=''\n[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'",
            ),
            ("example/files/a", "a"),
            (
                "example/pack.d/b.toml",
                "[[questions]]\nkey='second'\ntype='bool'\ndefault=false",
            ),
            (
                "example/pack.d/a.toml",
                "[[questions]]\nkey='first'\ntype='enum'\nchoices=['yes','no']",
            ),
        ]);
        let p = load(&tree, "example").unwrap();
        assert_eq!(
            p.questions
                .iter()
                .map(|q| q.key.as_str())
                .collect::<Vec<_>>(),
            ["app_name", "first", "second"]
        );
        assert!(p.questions[0].has_default());
        assert!(!p.questions[1].has_default());
        assert_eq!(p.questions[2].default, "false");
        assert_eq!(p.fs.get("files/a").unwrap().data.as_ref(), b"a");
        assert_eq!(list(&tree).unwrap(), ["example"]);
        assert!(p.files[0].condition.is_some());
    }
}

#[cfg(test)]
mod rejection_tests {
    use super::*;

    fn tree(body: &str, fragment: &str) -> Tree {
        let mut tree = Tree::new();
        tree.insert("example/pack.toml", body.as_bytes().to_vec(), 0o644)
            .unwrap();
        tree.insert("example/files/a", b"a".to_vec(), 0o644)
            .unwrap();
        if !fragment.is_empty() {
            tree.insert(
                "example/pack.d/extra.toml",
                fragment.as_bytes().to_vec(),
                0o644,
            )
            .unwrap();
        }
        tree
    }

    #[test]
    fn invalid_packs() {
        for (body, fragment, failure) in [
            ("name='other'", "", "name must equal"),
            ("description='test'", "", "name must equal"),
            ("name=", "", "decode"),
            ("name='example'\nunknown=true", "", "decode"),
            ("name='example'", "name='example'", "fragments"),
            ("name='example'", "description=''", "fragments"),
            ("name='example'", "requires=[]", "fragments"),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='number'",
                "",
                "invalid type",
            ),
            (
                "name='example'\n[[questions]]\nkey='bad-key'\ntype='string'",
                "",
                "invalid or reserved",
            ),
            (
                "name='example'\n[[questions]]\nkey='Stack'\ntype='string'",
                "",
                "reserved",
            ),
            (
                "name='example'\n[[questions]]\nkey='TemplateVersion'\ntype='string'",
                "",
                "reserved",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='enum'",
                "",
                "no choices",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\ndefault=2",
                "",
                "default",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\ndefault=[]",
                "",
                "default",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='bool'\ndefault='not-bool'",
                "",
                "bool default",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='bool'\ndefault=''",
                "",
                "bool default",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\ndefault=true",
                "",
                "bool default",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\npattern='['",
                "",
                "invalid pattern",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='bool'\npattern='.'",
                "",
                "pattern requires",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\nchoices=['a']",
                "",
                "choices require",
            ),
            (
                "name='example'\n[[questions]]\nkey='x'\ntype='string'\nunknown=true",
                "",
                "decode",
            ),
            (
                "name='example'\n[[files]]\npath='a'\nsource='files/a'\nclass='seed'\nunknown=true",
                "",
                "decode",
            ),
        ] {
            let error = load(&tree(body, fragment), "example")
                .unwrap_err()
                .to_string();
            assert!(error.contains(failure), "{body}, {fragment}: {error}");
            assert!(error.contains("\nTry:"), "{error}");
        }
    }

    #[test]
    fn duplicate_origins() {
        let body = "name='example'\n[[questions]]\nkey='app_name'\ntype='string'\n[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'";
        for fragment in [
            "[[questions]]\nkey='app_name'\ntype='string'",
            "[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'",
        ] {
            let error = load(&tree(body, fragment), "example")
                .unwrap_err()
                .to_string();
            assert!(error.contains("example/pack.toml"), "{error}");
            assert!(error.contains("example/pack.d/extra.toml"), "{error}");
            assert!(error.contains("duplicate"), "{error}");
        }
    }

    #[test]
    fn invalid_names_and_sorted_list() {
        let mut tree = tree("name='example'", "");
        for name in ["", ".", "_", "../escape", "a/b", "a\\b", "missing", "a\0b"] {
            assert!(load(&tree, name).is_err(), "{name:?}");
        }
        tree.insert("z/pack.toml", b"name='z'".to_vec(), 0o644)
            .unwrap();
        tree.insert("a/pack.toml", b"name='a'".to_vec(), 0o644)
            .unwrap();
        tree.insert("not-pack/file", b"ignored".to_vec(), 0o644)
            .unwrap();
        tree.insert("file", b"ignored".to_vec(), 0o644).unwrap();
        assert_eq!(list(&tree).unwrap(), ["a", "example", "z"]);
        assert!(list(&Tree::new()).unwrap().is_empty());
    }

    #[test]
    fn explicit_defaults_and_valid_types() {
        let tree = tree(
            "name='example'\n[[questions]]\nkey='value'\ntype='bool'\ndefault='true'",
            "",
        );
        let pack = load(&tree, "example").unwrap();
        assert!(pack.questions[0].has_default());
        assert_eq!(pack.questions[0].default, "true");
        let question = Question {
            default: "nonempty".into(),
            ..Question::default()
        };
        assert!(question.has_default());
    }

    #[test]
    fn matches_recompiles_changed_condition_and_lock_answers() {
        let mut pack = load(&tree("name='example'\n[[files]]\npath='out'\nsource='files/a'\nclass='seed'\nwhen=\"modules contains 'auth'\"", ""), "example").unwrap();
        for (input, expected) in [
            (r#"{"modules":["ci","auth"]}"#, true),
            (r#"{"modules":["ci"]}"#, false),
        ] {
            let answers: serde_json::Map<String, serde_json::Value> =
                serde_json::from_str(input).unwrap();
            assert_eq!(pack.files[0].matches(&answers).unwrap(), expected);
        }
        pack.files[0].when = "modules contains 'jobs'".into();
        let answers: serde_json::Map<String, serde_json::Value> =
            serde_json::from_str(r#"{"modules":["auth"]}"#).unwrap();
        assert!(!pack.files[0].matches(&answers).unwrap());
    }
}
