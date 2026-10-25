//! `gen resource <name> <field:type>... [--app DIR] [--format PLAN] [--force]
//! [--dry-run]`: the spike form of `spinup g resource`.

use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::{Result, bail};
use spinup_gen::Plan;

const USAGE: &str = "gen resource <name> <field:type>... [--app DIR] [--format stable|prettyplease|rustfmt] [--force] [--dry-run]";

fn main() -> ExitCode {
    match run(std::env::args().skip(1).collect()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("gen: {err:#}");
            ExitCode::FAILURE
        }
    }
}

fn run(args: Vec<String>) -> Result<()> {
    let mut app = PathBuf::from(".");
    // The plan that R0.10 chose. The other plans stay for the comparison.
    let mut plan = Plan::Rustfmt;
    let (mut force, mut dry_run) = (false, false);
    let mut rest = Vec::new();
    let mut it = args.into_iter();
    while let Some(arg) = it.next() {
        match arg.as_str() {
            "--app" => app = it.next().map(PathBuf::from).unwrap_or_default(),
            "--format" => plan = Plan::parse(&it.next().unwrap_or_default())?,
            "--force" => force = true,
            "--dry-run" => dry_run = true,
            _ => rest.push(arg),
        }
    }
    if rest.first().map(String::as_str) != Some("resource") {
        bail!("expected the resource command\nTry: {USAGE}");
    }
    let files = spinup_gen::plan_args(&app, &rest[1..], plan)?;
    if dry_run {
        for f in &files {
            println!("{}  {}", spinup_gen::hash(f.contents.as_bytes()), f.path);
        }
        return Ok(());
    }
    let report = spinup_gen::apply(&app, &files, force)?;
    for path in &report.written {
        println!("write      {path}");
    }
    for path in &report.unchanged {
        println!("unchanged  {path}");
    }
    Ok(())
}
