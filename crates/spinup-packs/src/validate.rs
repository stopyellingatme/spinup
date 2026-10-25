//! Validate pack questions, sources, output paths, and conditions.

use std::collections::BTreeSet;

use anyhow::{Result, bail};
use regex::Regex;

use crate::{
    loader::{FileSpec, Pack, Question},
    when::{Condition, quote},
};

impl Pack {
    /// Checks metadata and compiles every file condition for later evaluation.
    pub fn validate(&mut self) -> Result<()> {
        let mut questions = BTreeSet::new();
        for q in &self.questions {
            if !questions.insert(&q.key) {
                bail!(
                    "duplicate question {} in pack {}\nTry: give each question a unique key",
                    quote(&q.key),
                    quote(&self.name)
                );
            }
            q.validate()?;
        }
        let mut paths = BTreeSet::new();
        for f in &mut self.files {
            if !paths.insert(f.path.clone()) {
                bail!(
                    "duplicate output path {} in pack {}\nTry: give each file a unique output path",
                    quote(&f.path),
                    quote(&self.name)
                );
            }
            if !matches!(f.class.as_str(), "managed" | "scaffold" | "seed") {
                bail!(
                    "validate file {}: invalid class {}\nTry: use managed, scaffold, or seed",
                    quote(&f.path),
                    quote(&f.class)
                );
            }
            clean_path(&f.path)?;
            if !f.renamed_from.is_empty() {
                clean_path(&f.renamed_from)?;
            }
            clean_path(&f.source)?;
            if !self.fs.contains(&f.source) {
                if self.fs.is_dir(&f.source) {
                    bail!(
                        "validate source {}: source is not a regular file\nTry: select a source file",
                        quote(&f.source)
                    );
                }
                bail!(
                    "validate source {}: file does not exist\nTry: include the source in the pack",
                    quote(&f.source)
                );
            }
            f.permissions()?;
            f.condition = Some(Condition::parse(&f.when)?);
        }
        Ok(())
    }
}

pub(crate) fn clean_path(path: &str) -> Result<()> {
    let clean = spinup_vfs::path::clean_path(path).map_err(|error| {
        anyhow::anyhow!(
            "validate path {}: {error}\nTry: use a clean relative file path",
            quote(path)
        )
    })?;
    if clean != path {
        bail!(
            "validate path {}: path is not clean\nTry: use a clean relative file path",
            quote(path)
        );
    }
    Ok(())
}

impl Question {
    /// Checks the answer key, type, choices, bool default, and pattern syntax.
    pub fn validate(&self) -> Result<()> {
        let key = quote(&self.key);
        let mut bytes = self.key.bytes();
        if !bytes
            .next()
            .is_some_and(|b| b == b'_' || b.is_ascii_alphabetic())
            || !bytes.all(|b| b == b'_' || b.is_ascii_alphanumeric())
            || matches!(self.key.as_str(), "Stack" | "TemplateVersion")
        {
            bail!(
                "validate question {key}: invalid or reserved key\nTry: use an answer key other than Stack or TemplateVersion"
            );
        }
        match self.r#type.as_str() {
            "string" => {}
            "bool" => {
                if self.has_default() && !matches!(self.default.as_str(), "true" | "false") {
                    bail!("validate question {key}: invalid bool default\nTry: use true or false");
                }
            }
            "enum" => {
                if self.choices.is_empty() {
                    bail!(
                        "validate question {key}: enum has no choices\nTry: declare enum choices"
                    );
                }
            }
            _ => bail!(
                "validate question {key}: invalid type {}\nTry: use string, bool, or enum",
                quote(&self.r#type)
            ),
        }
        if !self.choices.is_empty() && self.r#type != "enum" {
            bail!(
                "validate question {key}: choices require enum type\nTry: use enum or remove choices"
            );
        }
        if !self.pattern.is_empty() {
            if self.r#type != "string" {
                bail!(
                    "validate question {key}: pattern requires string type\nTry: remove the pattern or use string"
                );
            }
            Regex::new(&self.pattern).map_err(|error| anyhow::anyhow!(
                "validate question {key}: invalid pattern: {error}\nTry: correct the regular expression"
            ))?;
        }
        Ok(())
    }
}

impl FileSpec {
    /// Returns octal permission bits, with 0644 as the default.
    pub fn permissions(&self) -> Result<u32> {
        if self.mode.is_empty() {
            return Ok(0o644);
        }
        if self.mode.len() != 4 || !self.mode.starts_with('0') {
            bail!(
                "validate mode {} for {}: expected four octal digits\nTry: use a mode such as 0644 or 0755",
                quote(&self.mode),
                quote(&self.path)
            );
        }
        u32::from_str_radix(&self.mode, 8).map_err(|error| {
            anyhow::anyhow!(
                "validate mode {} for {}: {error}\nTry: use a mode such as 0644 or 0755",
                quote(&self.mode),
                quote(&self.path)
            )
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_vfs::tree::Tree;

    fn pack() -> Pack {
        let mut fs = Tree::new();
        fs.insert("files/a", b"a".to_vec(), 0o644).unwrap();
        Pack {
            name: "example".into(),
            fs,
            files: vec![FileSpec {
                path: "out.txt".into(),
                source: "files/a".into(),
                class: "managed".into(),
                ..FileSpec::default()
            }],
            ..Pack::default()
        }
    }

    #[test]
    fn invalid_files() {
        for (field, value, failure) in [
            ("class", "other", "class"),
            ("path", "", "path"),
            ("path", "/escape", "path"),
            ("path", "a/../escape", "path"),
            ("path", "a//b", "path"),
            ("path", "a\\b", "path"),
            ("path", "a\0b", "path"),
            ("renamed_from", "../old", "path"),
            ("source", "../source", "path"),
            ("source", "missing", "source"),
            ("source", "files", "regular file"),
            ("when", "(auth)", "invalid when"),
            ("mode", "755", "mode"),
            ("mode", "0999", "mode"),
            ("mode", "1777", "mode"),
        ] {
            let mut pack = pack();
            let f = &mut pack.files[0];
            *match field {
                "class" => &mut f.class,
                "path" => &mut f.path,
                "source" => &mut f.source,
                "renamed_from" => &mut f.renamed_from,
                "when" => &mut f.when,
                _ => &mut f.mode,
            } = value.into();
            let error = pack.validate().unwrap_err().to_string();
            assert!(error.contains(failure), "{field}={value:?}: {error}");
            assert!(error.contains("\nTry:"));
        }
    }

    #[test]
    fn duplicate_direct_constructions() {
        let mut pack = pack();
        pack.files.push(pack.files[0].clone());
        assert!(
            pack.validate()
                .unwrap_err()
                .to_string()
                .contains("duplicate output path")
        );
        pack.files.pop();
        let q = Question {
            key: "valid".into(),
            r#type: "string".into(),
            ..Question::default()
        };
        pack.questions = vec![q.clone(), q];
        assert!(
            pack.validate()
                .unwrap_err()
                .to_string()
                .contains("duplicate question")
        );
    }

    #[test]
    fn permissions() {
        let mut file = FileSpec::default();
        assert_eq!(file.permissions().unwrap(), 0o644);
        for (mode, expected) in [
            ("0000", 0),
            ("0755", 0o755),
            ("0777", 0o777),
            ("0644", 0o644),
        ] {
            file.mode = mode.into();
            assert_eq!(file.permissions().unwrap(), expected);
        }
    }
}
