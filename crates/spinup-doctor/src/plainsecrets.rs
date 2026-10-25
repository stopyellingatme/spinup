//! The plain secrets check (Go `internal/doctor/plainsecrets`).
//!
//! The check lists the files that Git tracks. It fails when Git tracks a private file
//! (`.env`, `.env.*`, `*.agekey`) or a `secrets/*.yaml` file that is not encrypted with
//! sops, in the work tree or in the index. It also fails when `.gitignore` does not
//! exclude `.env` and `.env.local`.

use std::sync::Arc;

use linkme::distributed_slice;
use serde::Deserialize;
use spinup_exec::os_runner::OsRunner;
use spinup_exec::runner::{Cmd, Runner};

use crate::composelint::clean_path;
use crate::registry::{CHECKS, Check, Context, Outcome, Scope, Status};

const TRY: &str = "encrypt secrets with sops, remove private files from Git with git rm --cached, and add .env, .env.*, and *.agekey to .gitignore";

/// The `plaintext-secrets` check.
// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[distributed_slice(CHECKS)]
static PLAINTEXT_SECRETS: Check = Check {
    id: "plaintext-secrets",
    title: "Plaintext secrets",
    scope: Scope::Local,
    quick: false,
    run: run_check,
};

fn failure(message: impl Into<String>) -> Outcome {
    Outcome::fail(message, TRY)
}

fn run_check(ctx: &Context) -> Outcome {
    if ctx.no_root() || ctx.quick {
        return Outcome::new(Status::Skip, "requires a project and a full check");
    }
    let runner: Arc<dyn Runner> = ctx.runner.clone().unwrap_or_else(|| Arc::new(OsRunner));
    let listing = runner.output(
        &Cmd::new("git").args(["ls-files", "-z"]).dir(&ctx.root),
        &ctx.cancel,
    );
    let Ok(listing) = listing else {
        return failure("tracked files could not be inspected");
    };
    let stdout = String::from_utf8_lossy(&listing.stdout).into_owned();
    let mut problems: Vec<String> = Vec::new();
    for name in stdout.split('\0') {
        if name.is_empty() {
            continue;
        }
        if !fs_path(name) {
            return failure("Git returned an unsafe path");
        }
        if private_dotenv(name) || name.ends_with(".agekey") {
            problems.push(format!("tracked private file: {name}"));
        } else if name.starts_with("secrets/")
            && (name.ends_with(".yaml") || name.ends_with(".yml"))
        {
            let file = ctx.root.join(name);
            let regular = std::fs::symlink_metadata(&file).is_ok_and(|m| m.is_file());
            if !regular {
                problems.push(format!("unreadable secrets file: {name}"));
                continue;
            }
            match std::fs::read(&file) {
                Ok(data) if encrypted(&data) => {}
                _ => {
                    problems.push(format!("tracked plaintext secrets: {name}"));
                    continue;
                }
            }
            let staged = runner.output(
                &Cmd::new("git")
                    .args(["show", &format!(":./{name}")])
                    .dir(&ctx.root),
                &ctx.cancel,
            );
            if !staged.is_ok_and(|out| encrypted(&out.stdout)) {
                problems.push(format!("staged plaintext secrets: {name}"));
            }
        }
    }
    let ignores = std::fs::read_to_string(ctx.root.join(".gitignore"));
    if !ignores.is_ok_and(|data| ignores_dotenv(&data)) {
        problems.push(".gitignore does not exclude .env files".to_string());
    }
    if !problems.is_empty() {
        problems.sort();
        return failure(problems.join(", "));
    }
    Outcome::new(
        Status::Ok,
        "tracked secrets are encrypted and .env files are ignored",
    )
}

fn private_dotenv(name: &str) -> bool {
    let base = name.rsplit('/').next().unwrap_or(name);
    base == ".env" || (base != ".env.example" && base.starts_with(".env."))
}

fn fs_path(name: &str) -> bool {
    name != "." && !name.starts_with('/') && clean_path(name) == name && !name.starts_with("../")
}

/// Reports whether `data` is one YAML document with a non-empty `sops` mapping at the root.
fn encrypted(data: &[u8]) -> bool {
    let Ok(text) = std::str::from_utf8(data) else {
        return false;
    };
    let mut documents = Vec::new();
    for document in yaml_serde::Deserializer::from_str(text) {
        match yaml_serde::Value::deserialize(document) {
            Ok(value) => documents.push(value),
            Err(_) => return false,
        }
    }
    let [yaml_serde::Value::Mapping(root)] = documents.as_slice() else {
        return false;
    };
    root.iter().any(|(key, value)| {
        key.as_str() == Some("sops") && value.as_mapping().is_some_and(|m| !m.is_empty())
    })
}

fn ignores_dotenv(data: &str) -> bool {
    ignored(data, ".env") && ignored(data, ".env.local")
}

fn ignored(data: &str, target: &str) -> bool {
    let mut ignored = false;
    for line in data.split('\n') {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let negative = line.starts_with('!');
        let pattern = line.strip_prefix('!').unwrap_or(line);
        let pattern = pattern.strip_prefix('/').unwrap_or(pattern);
        let mut matched = glob_match(pattern, target);
        if let Some(rest) = pattern.strip_prefix("**/") {
            matched = glob_match(rest, target);
        }
        if pattern == "**/.env" && target == ".env" {
            matched = true;
        }
        if matched {
            ignored = !negative;
        }
    }
    ignored
}

/// Matches a name against a pattern with the rules of Go `path.Match`.
/// A bad pattern does not match.
fn glob_match(pattern: &str, name: &str) -> bool {
    let pattern: Vec<char> = pattern.chars().collect();
    let name: Vec<char> = name.chars().collect();
    match_here(&pattern, &name).unwrap_or(false)
}

/// Returns `None` for a bad pattern.
fn match_here(pattern: &[char], name: &[char]) -> Option<bool> {
    let Some((&first, rest)) = pattern.split_first() else {
        return Some(name.is_empty());
    };
    match first {
        '*' => {
            // Try every length of the star, never across a slash.
            for skip in 0..=name.len() {
                if let Some(true) = match_here(rest, &name[skip..]) {
                    return Some(true);
                }
                if name.get(skip) == Some(&'/') || skip == name.len() {
                    break;
                }
            }
            // A bad pattern in the rest still has to be reported.
            check_pattern(rest)?;
            Some(false)
        }
        '?' => match name.split_first() {
            Some((&c, tail)) if c != '/' => match_here(rest, tail),
            _ => {
                check_pattern(rest)?;
                Some(false)
            }
        },
        '[' => {
            let (class_end, negated) = class_bounds(pattern)?;
            let class = &pattern[if negated { 2 } else { 1 }..class_end];
            let tail_pattern = &pattern[class_end + 1..];
            let hit = match name.first() {
                Some(&c) => Some(class_matches(class, c)? != negated),
                None => None,
            };
            if hit == Some(true) {
                match_here(tail_pattern, &name[1..])
            } else {
                check_pattern(tail_pattern)?;
                Some(false)
            }
        }
        '\\' => {
            let (&literal, tail_pattern) = rest.split_first()?;
            match name.split_first() {
                Some((&c, tail)) if c == literal => match_here(tail_pattern, tail),
                _ => {
                    check_pattern(tail_pattern)?;
                    Some(false)
                }
            }
        }
        literal => match name.split_first() {
            Some((&c, tail)) if c == literal => match_here(rest, tail),
            _ => {
                check_pattern(rest)?;
                Some(false)
            }
        },
    }
}

/// Returns `None` when the pattern is bad.
fn check_pattern(pattern: &[char]) -> Option<()> {
    let mut i = 0;
    while i < pattern.len() {
        match pattern[i] {
            '\\' => {
                pattern.get(i + 1)?;
                i += 2;
            }
            '[' => {
                let (end, negated) = class_bounds(&pattern[i..])?;
                class_matches(&pattern[i + if negated { 2 } else { 1 }..i + end], 'a')?;
                i += end + 1;
            }
            _ => i += 1,
        }
    }
    Some(())
}

/// Returns the index of the closing `]` and whether the class is negated.
fn class_bounds(pattern: &[char]) -> Option<(usize, bool)> {
    let negated = pattern.get(1) == Some(&'^');
    let mut i = if negated { 2 } else { 1 };
    let mut first = true;
    while i < pattern.len() {
        match pattern[i] {
            ']' if !first => return Some((i, negated)),
            '\\' => i += 1,
            _ => {}
        }
        first = false;
        i += 1;
    }
    None
}

/// Reports whether the class (without brackets) contains `c`. Returns `None` for a bad range.
fn class_matches(class: &[char], c: char) -> Option<bool> {
    let mut matched = false;
    let mut i = 0;
    let next = |i: &mut usize| -> Option<char> {
        let mut ch = *class.get(*i)?;
        if ch == '\\' {
            *i += 1;
            ch = *class.get(*i)?;
        }
        *i += 1;
        Some(ch)
    };
    while i < class.len() {
        let low = next(&mut i)?;
        let mut high = low;
        if class.get(i) == Some(&'-') {
            i += 1;
            high = next(&mut i)?;
        }
        if low <= c && c <= high {
            matched = true;
        }
    }
    Some(matched)
}

#[cfg(test)]
mod tests;
