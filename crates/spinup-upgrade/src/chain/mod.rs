//! The upgrade chain of template versions (Go `internal/upgrade/chain.go`).
//!
//! An upgrade moves through the latest patch of each minor between the locked version and
//! the target. The target itself is always the last step.

use anyhow::{Result, bail};
use spinup_compat::version::Version as Semver;
use spinup_tplstore::source::Source;

/// Returns the versions of an upgrade from `from` to `to`, in upgrade order.
///
/// The result holds the latest patch of each minor between the two versions, followed by
/// `to`. `to = None` selects the newest loadable version. `versions = None` lists the
/// versions of `source`. An empty result means that the project is up to date. A
/// downgrade is an error. Each version of the result must load from `source`: a
/// [`spinup_tplstore::source::SourceError`] stays in the error chain, so a caller can
/// downcast it.
pub fn chain(
    source: Option<&dyn Source>,
    from: &str,
    to: Option<&str>,
    versions: Option<&[String]>,
) -> Result<Vec<String>> {
    let old = parse(from)?;
    if let Some(to) = to {
        let target = parse(to)?;
        if target < old {
            bail!(downgrade(from, to));
        }
        if target == old {
            return Ok(Vec::new());
        }
    }
    let Some(source) = source else {
        bail!("select upgrade versions: template source is nil\nTry: configure a template source");
    };
    let listed;
    let versions = match versions {
        Some(versions) => versions,
        None => {
            listed = source.versions()?;
            &listed
        }
    };
    let mut candidates = versions
        .iter()
        .map(|raw| Ok((parse(raw)?, raw.as_str())))
        .collect::<Result<Vec<_>>>()?;
    candidates.sort();
    let to = match to {
        Some(to) => to.to_string(),
        None => candidates
            .iter()
            .rev()
            .find(|(_, raw)| source.load(raw).is_ok())
            .map(|(_, raw)| raw.to_string())
            .ok_or_else(|| {
                anyhow::anyhow!(
                    "select upgrade target: no template version is loadable\nTry: spinup cache warm"
                )
            })?,
    };
    let target = parse(&to)?;
    if target < old {
        bail!(downgrade(from, &to));
    }
    if target == old {
        return Ok(Vec::new());
    }
    source.load(&to)?;
    let mut result: Vec<(Semver, String)> = Vec::new();
    for (version, raw) in &candidates {
        if *version <= old || *version >= target {
            continue;
        }
        // The exact requested target represents the target minor.
        if same_minor(version, &old) || same_minor(version, &target) {
            continue;
        }
        match result.last_mut() {
            Some(last) if same_minor(&last.0, version) => {
                *last = (version.clone(), raw.to_string())
            }
            _ => result.push((version.clone(), raw.to_string())),
        }
    }
    for (_, raw) in &result {
        source.load(raw)?;
    }
    let mut steps: Vec<String> = result.into_iter().map(|(_, raw)| raw).collect();
    steps.push(to);
    Ok(steps)
}

fn same_minor(a: &Semver, b: &Semver) -> bool {
    a.major == b.major && a.minor == b.minor
}

fn downgrade(from: &str, to: &str) -> String {
    format!(
        "upgrade {from} to {to}: downgrades are not supported\nTry: select a version at least {from}"
    )
}

/// Parses a template version: a semantic version without a `v` prefix, with optional build
/// metadata (Go `chainVersion`). Build metadata does not change the order.
pub fn parse(raw: &str) -> Result<Semver> {
    let (core, metadata) = match raw.split_once('+') {
        Some((core, metadata)) => (core, Some(metadata)),
        None => (raw, None),
    };
    let valid_metadata = metadata.is_none_or(|metadata| {
        metadata.split('.').all(|part| {
            !part.is_empty() && part.chars().all(|c| c.is_ascii_alphanumeric() || c == '-')
        })
    });
    match Semver::parse(core) {
        Ok(version)
            if valid_metadata && !raw.starts_with('v') && !raw.contains(['/', '\\', '\0']) =>
        {
            Ok(version)
        }
        _ => bail!(
            "select template version {raw:?}: expected a semantic version without v\nTry: use a version such as 0.2.0"
        ),
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::sync::Arc;

    use spinup_tplstore::source::{SourceError, Version};

    use super::*;

    struct Versions(BTreeMap<String, Arc<Version>>);

    impl Versions {
        fn new(list: &[&str]) -> Self {
            Self(
                list.iter()
                    .map(|v| {
                        let version = Version {
                            version: (*v).to_string(),
                            packs: Arc::default(),
                            stack: Arc::default(),
                            kit_migration: None,
                        };
                        ((*v).to_string(), Arc::new(version))
                    })
                    .collect(),
            )
        }
    }

    impl Source for Versions {
        fn versions(&self) -> Result<Vec<String>, SourceError> {
            Ok(self.0.keys().cloned().collect())
        }

        fn load(&self, version: &str) -> Result<Arc<Version>, SourceError> {
            self.0
                .get(version)
                .cloned()
                .ok_or_else(|| SourceError::Unavailable {
                    version: version.into(),
                    reason: "not cached".into(),
                    hint: format!("spinup cache warm {version}"),
                })
        }
    }

    /// The name, from, to, the listed versions, and the result (`None` is an error).
    type ChainCase = (
        &'static str,
        &'static str,
        Option<&'static str>,
        Option<&'static [&'static str]>,
        Option<&'static [&'static str]>,
    );

    #[test]
    fn chain_selects_minor_patches() {
        let source = Versions::new(&["0.1.0", "0.2.0", "0.2.7", "0.3.2", "0.4.0", "1.0.0"]);
        let strings = |list: &[&str]| list.iter().map(|s| (*s).to_string()).collect::<Vec<_>>();
        let cases: &[ChainCase] = &[
            (
                "minor patches",
                "0.1.0",
                Some("0.4.0"),
                None,
                Some(&["0.2.7", "0.3.2", "0.4.0"]),
            ),
            (
                "target patch",
                "0.1.0",
                Some("0.2.0"),
                None,
                Some(&["0.2.0"]),
            ),
            ("same", "0.1.0", Some("0.1.0"), None, Some(&[])),
            ("downgrade", "0.2.0", Some("0.1.0"), None, None),
            ("default", "0.4.0", None, None, Some(&["1.0.0"])),
            ("missing target", "0.1.0", Some("0.5.0"), None, None),
            ("bad from", "bad", Some("0.2.0"), None, None),
            (
                "default loadable",
                "0.1.0",
                None,
                Some(&["0.9.0", "0.2.0"]),
                Some(&["0.2.0"]),
            ),
            ("invalid metadata", "0.1.0", Some("0.2.0+bad$"), None, None),
            (
                "provided versions",
                "0.1.0",
                Some("0.4.0"),
                Some(&["0.3.2", "0.2.0"]),
                Some(&["0.2.0", "0.3.2", "0.4.0"]),
            ),
        ];
        for (name, from, to, versions, want) in cases {
            let versions = versions.map(strings);
            let got = chain(Some(&source), from, *to, versions.as_deref());
            match want {
                Some(want) => assert_eq!(got.unwrap(), strings(want), "{name}"),
                None => {
                    let err = got.expect_err(name);
                    assert!(format!("{err:#}").contains("Try:"), "{name}: {err:#}");
                }
            }
        }
    }

    #[test]
    fn chain_reports_unavailable_versions() {
        let err = chain(Some(&Versions::new(&[])), "0.1.0", Some("0.2.0"), None).unwrap_err();
        let source = err.downcast_ref::<SourceError>().expect("source error");
        assert!(source.is_unavailable(), "{err:#}");
    }

    #[test]
    fn chain_up_to_date_without_source() {
        assert!(
            chain(None, "0.2.0", Some("0.2.0"), None)
                .unwrap()
                .is_empty()
        );
        let err = chain(None, "0.1.0", Some("0.2.0"), None).unwrap_err();
        assert!(format!("{err:#}").contains("Try:"));
    }

    #[test]
    fn chain_version_parse_rules() {
        for good in [
            "0.2.0",
            "1.0.0-rc.1",
            "0.0.0-dev",
            "0.2.0+build.7",
            "0.2.0+a-b",
        ] {
            assert!(parse(good).is_ok(), "{good}");
        }
        for bad in [
            "v0.2.0",
            "0.2",
            "0.2.0+",
            "0.2.0+a..b",
            "0.2.0/x",
            "0.2.0+a\\b",
            "x",
        ] {
            let err = parse(bad).unwrap_err();
            assert!(format!("{err:#}").contains("Try: use a version"), "{bad}");
        }
        assert_eq!(parse("0.2.0+build").unwrap(), parse("0.2.0").unwrap());
    }
}
