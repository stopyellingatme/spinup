//! `check_kit`, `check_min_cli`, and the supported template versions.

use crate::version::{Version, parse_version};

/// A compatibility failure. `Display` is the Go text, with its `Try:` line.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct CompatError(String);

/// Verify that the CLI and kit major and minor versions match.
///
/// # Errors
/// Returns [`CompatError`] when a version does not parse or the numbers differ.
pub fn check_kit(cli_version: &str, kit_version: &str) -> Result<(), CompatError> {
    if cli_version == "dev" {
        return Ok(());
    }
    let cli = parse_version(cli_version).map_err(|e| {
        CompatError(format!(
            "cannot check CLI compatibility: {e}\nTry: spinup upgrade"
        ))
    })?;
    let kit = parse_version(kit_version).map_err(|e| {
        CompatError(format!(
            "cannot check kit compatibility: {e}\nTry: go get github.com/stopyellingatme/spinup/kit@v{}.{}.x",
            cli.major, cli.minor
        ))
    })?;
    if cli.major == kit.major && cli.minor == kit.minor {
        return Ok(());
    }
    Err(CompatError(format!(
        "CLI {cli} is incompatible with kit {kit}: major and minor versions must match\nTry: spinup upgrade or go get github.com/stopyellingatme/spinup/kit@v{}.{}.x",
        cli.major, cli.minor
    )))
}

/// Return up to three released templates that the CLI supports, newest first.
///
/// A released text that does not parse is skipped. A `dev` CLI supports every parsed release.
/// Equal versions sort by their raw text, ascending.
#[must_use]
pub fn supported_template_versions<S: AsRef<str>>(
    cli_version: &str,
    released: &[S],
) -> Vec<String> {
    let cli_is_dev = cli_version == "dev";
    let cli = parse_version(cli_version);
    if cli.is_err() && !cli_is_dev {
        return Vec::new();
    }
    let mut candidates: Vec<(String, Version)> = released
        .iter()
        .filter_map(|raw| {
            let v = parse_version(raw.as_ref()).ok()?;
            if let (false, Ok(c)) = (cli_is_dev, &cli)
                && v > *c
            {
                return None;
            }
            Some((raw.as_ref().to_string(), v))
        })
        .collect();
    candidates.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    candidates.into_iter().take(3).map(|(raw, _)| raw).collect()
}

/// Verify that the CLI satisfies a template minimum version.
///
/// # Errors
/// Returns [`CompatError`] when a version does not parse or the CLI is older.
pub fn check_min_cli(cli_version: &str, min_cli: &str) -> Result<(), CompatError> {
    if cli_version == "dev" {
        return Ok(());
    }
    let cli = parse_version(cli_version).map_err(|e| {
        CompatError(format!(
            "cannot check CLI version: {e}\nTry: spinup self-update"
        ))
    })?;
    let minimum = parse_version(min_cli).map_err(|e| {
        CompatError(format!(
            "cannot check minimum CLI version: {e}\nTry: spinup self-update"
        ))
    })?;
    if cli < minimum {
        return Err(CompatError(format!(
            "CLI {cli} is older than required version {minimum}\nTry: spinup self-update"
        )));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Pair {
        cli: String,
        other: String,
        error: String,
    }

    #[derive(Deserialize)]
    struct Supported {
        cli: String,
        released: Option<Vec<String>>,
        expected: Option<Vec<String>>,
    }

    #[derive(Deserialize)]
    struct Oracle {
        kit: Vec<Pair>,
        min_cli: Vec<Pair>,
        supported: Vec<Supported>,
    }

    fn oracle() -> Oracle {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../testdata/oracle/compat.json"
        );
        serde_json::from_str(&std::fs::read_to_string(path).expect("read oracle"))
            .expect("parse oracle")
    }

    fn text(r: Result<(), CompatError>) -> String {
        r.err().map(|e| e.to_string()).unwrap_or_default()
    }

    #[test]
    fn oracle_check_kit() {
        for c in &oracle().kit {
            assert_eq!(
                text(check_kit(&c.cli, &c.other)),
                c.error,
                "{} {}",
                c.cli,
                c.other
            );
        }
    }

    #[test]
    fn oracle_check_min_cli() {
        for c in &oracle().min_cli {
            assert_eq!(
                text(check_min_cli(&c.cli, &c.other)),
                c.error,
                "{} {}",
                c.cli,
                c.other
            );
        }
    }

    #[test]
    fn oracle_supported() {
        for c in &oracle().supported {
            let released = c.released.clone().unwrap_or_default();
            assert_eq!(
                supported_template_versions(&c.cli, &released),
                c.expected.clone().unwrap_or_default(),
                "{} {:?}",
                c.cli,
                released
            );
        }
    }

    #[test]
    fn go_unit_cases() {
        assert!(check_kit("v1.2.3", "v1.2.99").is_ok());
        assert!(check_kit("dev", "bad").is_ok());
        let e = check_kit("v1.2.3", "v1.3.0").unwrap_err().to_string();
        assert!(e.contains("Try:") && e.contains("v1.2.x"));
        let got = supported_template_versions(
            "v1.4.0",
            &["v1.5.0", "v1.0.0", "v1.4.0", "v1.3.0", "v1.2.0", "bad"],
        );
        assert_eq!(got, ["v1.4.0", "v1.3.0", "v1.2.0"]);
        assert!(check_min_cli("dev", "v99.0.0").is_ok());
        assert!(check_min_cli("v1.2.3", "v1.2.3").is_ok());
        assert!(
            check_min_cli("v1.2.3", "v1.2.4")
                .unwrap_err()
                .to_string()
                .contains("Try: spinup self-update")
        );
    }
}
