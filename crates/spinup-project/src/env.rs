//! The database URL of a project and the sqlx cache hint (ADR 0021 §6). One resolver for
//! `spinup db`, the post-step runner, and the no-database rule. Main loop only.

use std::path::Path;

/// The cargo output text when a query macro has no `.sqlx/` entry.
pub const SQLX_NO_CACHE: &str = "there is no cached data for this query";
/// The hint line that follows a build failure with [`SQLX_NO_CACHE`].
pub const SQLX_PREPARE_TRY: &str = "Try: spinup db prepare";

const KEY: &str = "DATABASE_URL";

/// Returns the database URL of the project at `root`, or `None`.
///
/// The order is the order of the Go app (`kit/config/config.go:40`, `kit/config/dotenv.go:14`):
/// `DATABASE_URL`, the file that `DATABASE_URL_FILE` names (trimmed), the `DATABASE_URL`
/// line of `<root>/api/.env`, then of `<root>/.env`. An empty value counts as unset.
/// `lookup` reads one environment variable, so tests never change the process environment.
///
/// # Errors
/// Returns an error with a `Try:` line when `DATABASE_URL_FILE` names a file that cannot be read.
pub fn database_url(
    root: &Path,
    lookup: &dyn Fn(&str) -> Option<String>,
) -> anyhow::Result<Option<String>> {
    if let Some(value) = lookup(KEY).filter(|v| !v.is_empty()) {
        return Ok(Some(value));
    }
    if let Some(file) = lookup("DATABASE_URL_FILE").filter(|v| !v.is_empty()) {
        let text = std::fs::read_to_string(&file).map_err(|error| {
            anyhow::anyhow!(
                "read DATABASE_URL_FILE {file:?}: {error}\nTry: point DATABASE_URL_FILE at a readable file, or unset it"
            )
        })?;
        let value = text.trim();
        if !value.is_empty() {
            return Ok(Some(value.to_string()));
        }
    }
    for path in [root.join("api").join(".env"), root.join(".env")] {
        if let Some(value) = dotenv_value(&path, KEY).filter(|v| !v.is_empty()) {
            return Ok(Some(value));
        }
    }
    Ok(None)
}

/// [`database_url`] with the process environment.
///
/// # Errors
/// As [`database_url`].
pub fn database_url_from_process(root: &Path) -> anyhow::Result<Option<String>> {
    database_url(root, &|key| std::env::var(key).ok())
}

/// Returns the value of the last `key=` line of the `.env` file at `path`. A missing or
/// unreadable file gives `None`. The rules are the Go `dotenv.go` rules: blank lines and `#`
/// comments are skipped, an `export ` prefix is allowed, an unquoted value ends at ` #`, and
/// one pair of matching `"` or `'` quotes is removed (`"` values keep Go escapes). A line
/// that Go rejects is skipped.
fn dotenv_value(path: &Path, key: &str) -> Option<String> {
    let text = std::fs::read_to_string(path).ok()?;
    let mut found = None;
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let line = line
            .strip_prefix("export ")
            .or_else(|| line.strip_prefix("export\t"))
            .map_or(line, str::trim_start);
        let Some((name, value)) = line.split_once('=') else {
            continue;
        };
        if name.trim() != key {
            continue;
        }
        if let Some(value) = parse_value(value.trim()) {
            found = Some(value);
        }
    }
    found
}

/// Go `dotenvValue`. `None` for a value that Go rejects.
fn parse_value(raw: &str) -> Option<String> {
    let Some(quote) = raw.chars().next().filter(|c| *c == '"' || *c == '\'') else {
        let bytes = raw.as_bytes();
        for (i, c) in bytes.iter().enumerate() {
            if *c == b'#' && (i == 0 || bytes[i - 1] == b' ' || bytes[i - 1] == b'\t') {
                return Some(raw[..i].trim().to_string());
            }
        }
        return Some(raw.to_string());
    };
    let mut out = String::new();
    let mut chars = raw[1..].char_indices();
    while let Some((i, c)) = chars.next() {
        if c == quote {
            let tail = raw[1 + i + 1..].trim();
            if !tail.is_empty() && !tail.starts_with('#') {
                return None;
            }
            return Some(out);
        }
        if quote == '"' && c == '\\' {
            let (_, escaped) = chars.next()?;
            out.push(match escaped {
                'n' => '\n',
                't' => '\t',
                'r' => '\r',
                other => other,
            });
            continue;
        }
        out.push(c);
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    fn resolve(root: &Path, vars: &[(&str, &str)]) -> anyhow::Result<Option<String>> {
        let vars: BTreeMap<String, String> = vars
            .iter()
            .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
            .collect();
        database_url(root, &|key| vars.get(key).cloned())
    }

    fn project(api_env: Option<&str>, root_env: Option<&str>) -> tempfile::TempDir {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join("api")).unwrap();
        if let Some(text) = api_env {
            std::fs::write(dir.path().join("api/.env"), text).unwrap();
        }
        if let Some(text) = root_env {
            std::fs::write(dir.path().join(".env"), text).unwrap();
        }
        dir
    }

    #[test]
    fn each_source_alone() {
        let empty = project(None, None);
        assert_eq!(resolve(empty.path(), &[]).unwrap(), None);
        assert_eq!(
            resolve(empty.path(), &[("DATABASE_URL", "postgres://env")]).unwrap(),
            Some("postgres://env".into())
        );
        let file = empty.path().join("secret");
        std::fs::write(&file, "  postgres://file\n").unwrap();
        assert_eq!(
            resolve(
                empty.path(),
                &[("DATABASE_URL_FILE", file.to_str().unwrap())]
            )
            .unwrap(),
            Some("postgres://file".into())
        );
        let api = project(Some("DATABASE_URL=postgres://api\n"), None);
        assert_eq!(
            resolve(api.path(), &[]).unwrap(),
            Some("postgres://api".into())
        );
        let root = project(None, Some("DATABASE_URL=postgres://root\n"));
        assert_eq!(
            resolve(root.path(), &[]).unwrap(),
            Some("postgres://root".into())
        );
    }

    #[test]
    fn order_and_empty_values() {
        let dir = project(
            Some("DATABASE_URL=postgres://api\n"),
            Some("DATABASE_URL=postgres://root\n"),
        );
        let file = dir.path().join("secret");
        std::fs::write(&file, "postgres://file").unwrap();
        let file = file.to_str().unwrap();
        assert_eq!(
            resolve(
                dir.path(),
                &[
                    ("DATABASE_URL", "postgres://env"),
                    ("DATABASE_URL_FILE", file)
                ]
            )
            .unwrap(),
            Some("postgres://env".into())
        );
        assert_eq!(
            resolve(
                dir.path(),
                &[("DATABASE_URL", ""), ("DATABASE_URL_FILE", file)]
            )
            .unwrap(),
            Some("postgres://file".into())
        );
        assert_eq!(
            resolve(dir.path(), &[("DATABASE_URL", "")]).unwrap(),
            Some("postgres://api".into())
        );
        let empty_api = project(
            Some("DATABASE_URL=\n"),
            Some("DATABASE_URL=postgres://root\n"),
        );
        assert_eq!(
            resolve(empty_api.path(), &[]).unwrap(),
            Some("postgres://root".into())
        );
    }

    #[test]
    fn dotenv_rules() {
        let dir = project(
            Some(
                "# comment\n\nOTHER=1\nDATABASE_URL=postgres://first\nexport DATABASE_URL=\"postgres://quoted\"\n",
            ),
            None,
        );
        assert_eq!(
            resolve(dir.path(), &[]).unwrap(),
            Some("postgres://quoted".into())
        );
        let single = project(Some("DATABASE_URL='postgres://single'\n"), None);
        assert_eq!(
            resolve(single.path(), &[]).unwrap(),
            Some("postgres://single".into())
        );
        let comment = project(Some("DATABASE_URL=postgres://c # note\n"), None);
        assert_eq!(
            resolve(comment.path(), &[]).unwrap(),
            Some("postgres://c".into())
        );
        let escaped = project(Some("DATABASE_URL=\"postgres://a\\\"b\" # note\n"), None);
        assert_eq!(
            resolve(escaped.path(), &[]).unwrap(),
            Some("postgres://a\"b".into())
        );
        let bad = project(
            Some("DATABASE_URL=postgres://good\nDATABASE_URL=\"postgres://unterminated\n"),
            None,
        );
        assert_eq!(
            resolve(bad.path(), &[]).unwrap(),
            Some("postgres://good".into())
        );
    }

    #[test]
    fn unreadable_database_url_file() {
        let dir = project(None, None);
        let missing = dir.path().join("missing");
        let error = resolve(
            dir.path(),
            &[("DATABASE_URL_FILE", missing.to_str().unwrap())],
        )
        .unwrap_err()
        .to_string();
        assert!(error.contains("read DATABASE_URL_FILE"), "{error}");
        assert!(error.contains("\nTry:"), "{error}");
    }
}
