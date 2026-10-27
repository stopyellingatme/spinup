//! Job scaffolds and sorted registration from Rust file names.

#[cfg(test)]
mod tests;

use std::{collections::BTreeSet, path::Path};

use anyhow::{Context, Result, bail};
use serde::Serialize;
use spinup_gen::contract::{Generator, Input, Output};
use spinup_vfs::plan::{File, Plan};

/// The directory of application jobs.
pub const DIR: &str = "api/src/jobs";
/// The generator-owned job module list.
pub const REGISTER_FILE: &str = "api/src/jobs/mod.rs";

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static JOB: Generator = Generator {
    name: "job",
    aliases: &[],
    short: "Generate a background job and register it",
    usage: "<name>",
    args: |cmd| cmd,
    run,
};

/// A normalized item name without resource inflection.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Name {
    /// The file name and job kind.
    pub snake: String,
    /// The Rust type name.
    pub pascal: String,
}

/// Parses snake, kebab, or camel case with the Go job naming rules.
/// Rust keywords and generated file names are reserved. The Go file-name rule for a
/// GOOS or GOARCH suffix does not apply: Rust has no build constraint in a file name.
pub fn parse_name(value: &str) -> Result<Name> {
    item_name(value, "job")
}

pub(crate) fn item_name(value: &str, kind: &str) -> Result<Name> {
    let mut words = Vec::new();
    let mut word = String::new();
    let mut previous = b'_';
    for byte in value.bytes() {
        if byte == b'_' || byte == b'-' {
            if !word.is_empty() {
                words.push(std::mem::take(&mut word));
            }
        } else if byte.is_ascii_alphanumeric() {
            if byte.is_ascii_uppercase()
                && (previous.is_ascii_lowercase() || previous.is_ascii_digit())
                && !word.is_empty()
            {
                words.push(std::mem::take(&mut word));
            }
            word.push(byte.to_ascii_lowercase() as char);
        } else {
            return Err(name_error(
                kind,
                value,
                "use letters, digits, and underscores",
            ));
        }
        previous = byte;
    }
    if !word.is_empty() {
        words.push(word);
    }
    let snake = words.join("_");
    let reason = if snake.is_empty() {
        Some("use letters, digits, and underscores")
    } else if snake.starts_with(|c: char| c.is_ascii_digit()) {
        Some("it starts with a digit")
    } else if snake.len() > 50 {
        Some("it is longer than 50 characters")
    } else if "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self static struct super trait true type unsafe use where while abstract become box do final gen macro override priv try typeof unsized virtual yield register register_gen deps generated init main"
        .split_whitespace().any(|s| s == snake)
    {
        Some("the name is reserved")
    } else if snake.ends_with("_test") || snake.ends_with("_tests") || snake.ends_with("_gen") {
        Some("a name that ends in _test, _tests, or _gen is reserved")
    } else {
        None
    };
    if let Some(reason) = reason {
        return Err(name_error(kind, value, reason));
    }
    let pascal = words
        .iter()
        .map(|word| word[..1].to_ascii_uppercase() + &word[1..])
        .collect();
    Ok(Name { snake, pascal })
}

fn name_error(kind: &str, value: &str, why: &str) -> anyhow::Error {
    let example = match kind {
        "mailer" => "welcome name:string",
        "handler" => "ping",
        _ => "send_digest",
    };
    anyhow::anyhow!("invalid {kind} name {value:?}: {why}\nTry: spinup g {kind} {example}")
}

fn run(input: &Input<'_>) -> Result<Output> {
    require_module(input, "jobs", "job")?;
    let [value] = input.args else {
        bail!("cannot generate a job: expected one job name\nTry: spinup g job send_digest");
    };
    let name = parse_name(value)?;
    let mut names = scan(input.root, DIR)?;
    check_collision(&names, &name, "job")?;
    names.insert(name.snake.clone());
    let mut plan = Plan::new();
    plan.add(File {
        path: format!("{DIR}/{}.rs", name.snake),
        data: render("job", include_str!("../../templates/job/job.rs.j2"), &name)?,
        class: "scaffold".into(),
        ..File::default()
    })?;
    plan.add(File {
        path: REGISTER_FILE.into(),
        data: module_source(&names)?.into_bytes(),
        class: "managed".into(),
        ..File::default()
    })?;
    Ok(Output {
        plan: plan.sorted(),
        next: vec![
            "cd api && cargo test --locked jobs".into(),
            "cd api && cargo run --locked -- worker".into(),
        ],
        ..Output::default()
    })
}

/// Returns the stable module list that the jobs pack also ships when empty.
/// Each job file exports `register(worker, pool)`.
pub fn module_source(names: &BTreeSet<String>) -> Result<String> {
    render(
        "job modules",
        include_str!("../../templates/job/mod.rs.j2"),
        &serde_json::json!({"names": names}),
    )
    .and_then(|data| String::from_utf8(data).map_err(Into::into))
}

pub(crate) fn require_module(input: &Input<'_>, module: &str, kind: &str) -> Result<()> {
    let config = spinup_project::config::Config {
        modules: input.lock.packs.clone(),
        ..Default::default()
    };
    if !spinup_render::modules::enabled(Some(&config), module) {
        bail!(
            "cannot generate a {kind}: the app has no {module} module\nTry: spinup module add {module}"
        );
    }
    Ok(())
}

pub(crate) fn scan(root: &Path, dir: &str) -> Result<BTreeSet<String>> {
    let entries = match std::fs::read_dir(root.join(dir)) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(BTreeSet::new()),
        Err(error) => {
            return Err(error).with_context(|| {
                format!("read {dir} failed\nTry: check the directory permissions")
            });
        }
    };
    let mut names = BTreeSet::new();
    for entry in entries {
        let entry = entry
            .with_context(|| format!("read {dir} failed\nTry: check the directory permissions"))?;
        if !entry
            .file_type()
            .context("inspect module file failed\nTry: check the directory permissions")?
            .is_file()
        {
            continue;
        }
        let file = entry.file_name();
        let Some(stem) = file.to_str().and_then(|s| s.strip_suffix(".rs")) else {
            continue;
        };
        if stem == "mod"
            || stem.ends_with("_tests")
            || stem.ends_with("_test")
            || (dir == crate::mailer::DIR && matches!(stem, "mailers" | "worker"))
        {
            continue;
        }
        let name = item_name(stem, "job")?;
        if name.snake != stem {
            bail!(
                "cannot list Rust module {stem:?}: its file name is not snake case\nTry: rename the file with a Rust module name"
            );
        }
        names.insert(stem.to_owned());
    }
    Ok(names)
}

pub(crate) fn check_collision(names: &BTreeSet<String>, name: &Name, kind: &str) -> Result<()> {
    for existing in names {
        if existing != &name.snake && item_name(existing, kind)?.pascal == name.pascal {
            bail!(
                "cannot generate {kind} {:?}: {} is already declared in {existing}.rs\nTry: use a different {kind} name",
                name.snake,
                name.pascal
            );
        }
    }
    Ok(())
}

pub(crate) fn render(name: &str, source: &str, context: &impl Serialize) -> Result<Vec<u8>> {
    let mut env = spinup_render::env::environment(None);
    env.set_syntax(
        env.syntax()
            .to_builder()
            .trim_blocks(true)
            .lstrip_blocks(true)
            .build()
            .expect("valid template syntax"),
    );
    env.render_str(source, minijinja::value::Serde(context))
        .map(String::into_bytes)
        .with_context(|| format!("render {name} failed\nTry: report the generator template error"))
}
