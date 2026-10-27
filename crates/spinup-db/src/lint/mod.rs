//! SQL migration checks and the database lint command.

mod sql;
use serde::Serialize;
use sql::{Statement, create_table, rule_for, tokenize};
use std::collections::BTreeSet;
use std::io::Write;
use std::path::{Path, PathBuf};

/// One unsafe migration statement.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Finding {
    /// The migration filename.
    pub file: String,
    /// The first line of the statement, starting at one.
    pub line: usize,
    /// The stable lint rule.
    pub rule: String,
    /// The reason the statement is unsafe.
    pub message: String,
    /// The suggested correction.
    #[serde(rename = "try")]
    pub try_hint: String,
}

/// Check one SQLx up migration without earlier migration history.
/// Down files have no findings.
pub fn lint(name: &str, sql: &[u8]) -> anyhow::Result<Vec<Finding>> {
    Ok(check(name, sql, None))
}

/// Check up migrations in filename order, with earlier table definitions.
pub fn lint_dir(dir: &Path) -> anyhow::Result<Vec<Finding>> {
    // Unlike naming, linting a missing directory is an error.
    std::fs::read_dir(dir).map_err(|e| {
        crate::ops::op_error(
            "Cannot lint migrations",
            anyhow::anyhow!("read migrations directory {dir:?}: {e}"),
        )
    })?;
    let files = crate::names::sql_files(dir)
        .map_err(|e| crate::ops::op_error("Cannot lint migrations", e))?;
    let mut earlier = BTreeSet::new();
    let mut findings = Vec::new();
    for name in files.into_iter().filter(|name| name.ends_with(".up.sql")) {
        let bytes = read(&dir.join(&name))?;
        findings.extend(check(&name, &bytes, Some(&earlier)));
        for statement in tokenize(&String::from_utf8_lossy(&bytes)) {
            if let Some(table) = create_table(&statement.tokens) {
                earlier.insert(table);
            }
        }
    }
    Ok(findings)
}

/// Check selected files, or the app migration directory when `files` is empty.
/// Resolve relative paths from the project root and sort them before reading.
pub fn lint_files(root: &Path, files: &[PathBuf]) -> anyhow::Result<Vec<Finding>> {
    if files.is_empty() {
        return lint_dir(&root.join(crate::names::MIGRATIONS_DIR));
    }
    let mut paths: Vec<_> = files
        .iter()
        .map(|p| {
            if p.is_absolute() {
                p.clone()
            } else {
                root.join(p)
            }
        })
        .collect();
    paths.sort();
    let mut findings = Vec::new();
    for path in paths
        .into_iter()
        .filter(|p| p.to_string_lossy().ends_with(".up.sql"))
    {
        let bytes = read(&path)?;
        findings.extend(lint(
            &path.file_name().unwrap_or_default().to_string_lossy(),
            &bytes,
        )?);
    }
    Ok(findings)
}

/// Find the project, print text or JSON findings, and fail when changes are unsafe.
pub fn run(dir: &Path, files: &[PathBuf], json: bool, out: &mut dyn Write) -> anyhow::Result<()> {
    let root = spinup_project::root::find_root(dir)
        .map_err(|e| crate::ops::op_error("Cannot lint migrations", e))?;
    let findings = lint_files(&root, files)?;
    let write_result = if json {
        serde_json::to_writer_pretty(&mut *out, &findings)
            .map_err(std::io::Error::other)
            .and_then(|()| writeln!(out))
    } else {
        findings.iter().try_for_each(|f| {
            writeln!(
                out,
                "{}:{}: {}: {}\n  Try: {}",
                f.file, f.line, f.rule, f.message, f.try_hint
            )
        })
    };
    write_result.map_err(|e| crate::ops::op_error("Cannot write lint findings", e))?;
    if !findings.is_empty() {
        return Err(spinup_ui::error::Error::new("Database lint failed")
            .why(format!("found {} unsafe migration changes", findings.len()))
            .try_hint("split the contract step into a later release")
            .into());
    }
    Ok(())
}

fn read(path: &Path) -> anyhow::Result<Vec<u8>> {
    std::fs::read(path).map_err(|e| {
        crate::ops::op_error(
            "Cannot lint migrations",
            anyhow::anyhow!("read migration {path:?}: {e}"),
        )
    })
}

fn check(name: &str, bytes: &[u8], earlier: Option<&BTreeSet<String>>) -> Vec<Finding> {
    if name.ends_with(".down.sql") {
        return Vec::new();
    }
    let sql = String::from_utf8_lossy(bytes);
    let no_transaction = sql.starts_with("-- no-transaction");
    let mut allowed = BTreeSet::new();
    let mut findings = Vec::new();
    for (index, line) in sql.lines().enumerate() {
        if let Some(reason) = line.trim().strip_prefix("-- spinup:allow-destructive") {
            if reason.trim().is_empty() {
                findings.push(Finding {
                    file: name.into(),
                    line: index + 1,
                    rule: "bad-override".into(),
                    message: "destructive override needs a reason".into(),
                    try_hint: "add a reason after -- spinup:allow-destructive".into(),
                });
            } else {
                allowed.insert(index + 2);
            }
        }
    }
    let statements = tokenize(&sql);
    let created: BTreeSet<_> = statements
        .iter()
        .filter_map(|s| create_table(&s.tokens))
        .collect();
    for Statement { tokens, line } in statements {
        if allowed.contains(&line) {
            continue;
        }
        if let Some((rule, message, hint)) = rule_for(&tokens, earlier, &created, no_transaction) {
            findings.push(Finding {
                file: name.into(),
                line,
                rule: rule.into(),
                message: message.into(),
                try_hint: hint.into(),
            });
        }
    }
    findings.sort_by_key(|f| f.line);
    findings
}

#[cfg(test)]
mod tests;
