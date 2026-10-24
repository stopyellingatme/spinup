//! `compare [RUNS]`: runs the R0.10 comparison of the three formatting plans
//! and prints Markdown tables for the result file.
//!
//! For each plan it generates the reference resources (todo, then note) into
//! a copy of the base app inside a temporary Cargo workspace, then checks:
//! the tree hash over two fresh runs, a second run on the same app, `cargo
//! fmt --all --check`, the files that `cargo fmt` changes, and the lock
//! records that no longer match the files after `cargo fmt`. It also times
//! the plan step and sweeps longer names through all plans.

use std::fs;
use std::path::Path;
use std::process::Command;
use std::time::Instant;

use anyhow::{Context, Result, bail};
use spinup_gen::compare::{
    REFERENCE, TempDir, copy_base, generate, not_rustfmt_clean, plan_one, workspace,
};
use spinup_gen::{LOCK, Lock, Plan, hash, tree_hash};

fn main() -> Result<()> {
    let runs: usize = std::env::args()
        .nth(1)
        .map(|s| s.parse())
        .transpose()?
        .unwrap_or(5);
    println!("load before: {}", load()?);
    println!();
    reference_table()?;
    println!();
    timing_table(runs)?;
    println!();
    sweep_table()?;
    println!();
    println!("load after: {}", load()?);
    Ok(())
}

/// The 1-minute load average from `uptime`.
fn load() -> Result<String> {
    let out = Command::new("uptime").output()?;
    let text = String::from_utf8(out.stdout)?;
    Ok(text
        .rsplit_once("load averages:")
        .or_else(|| text.rsplit_once("load average:"))
        .map(|(_, l)| l.trim().to_owned())
        .unwrap_or(text))
}

/// A temporary Cargo workspace with the kit and an app at `todo-api/`.
fn workspace_copy(label: &str) -> Result<TempDir> {
    let dir = TempDir::new(label)?;
    let ws = workspace();
    let manifest = fs::read_to_string(ws.join("Cargo.toml"))?.replace(
        r#"members = ["gen", "kit", "todo-api"]"#,
        r#"members = ["kit", "todo-api"]"#,
    );
    fs::write(dir.path().join("Cargo.toml"), manifest)?;
    fs::copy(ws.join("Cargo.lock"), dir.path().join("Cargo.lock"))?;
    copy_tree(&ws.join("kit"), &dir.path().join("kit"))?;
    copy_base(&dir.path().join("todo-api"))?;
    Ok(dir)
}

fn copy_tree(src: &Path, dst: &Path) -> Result<()> {
    fs::create_dir_all(dst)?;
    for entry in fs::read_dir(src)? {
        let entry = entry?;
        let to = dst.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_tree(&entry.path(), &to)?;
        } else {
            fs::copy(entry.path(), to)?;
        }
    }
    Ok(())
}

/// Runs cargo fmt in the workspace. It returns success and the time in ms.
fn cargo_fmt(ws: &Path, check: bool) -> Result<(bool, f64)> {
    let mut cmd = Command::new("cargo");
    cmd.args(["fmt", "--all"]).current_dir(ws);
    if check {
        cmd.arg("--check");
    }
    let start = Instant::now();
    let out = cmd.output().context("run cargo fmt")?;
    let ms = start.elapsed().as_secs_f64() * 1000.0;
    if !check && !out.status.success() {
        bail!("cargo fmt failed: {}", String::from_utf8_lossy(&out.stderr));
    }
    Ok((out.status.success(), ms))
}

fn reference_table() -> Result<()> {
    println!("Reference resources (todo, then note), per plan:");
    println!();
    println!(
        "| Plan | Tree hash, run 1 | Run 2 equal | Second run writes | `cargo fmt --check` | Files `cargo fmt` changes | Lock records stale after `cargo fmt` | `cargo fmt` time |"
    );
    println!("|---|---|---|---|---|---|---|---|");
    for plan in Plan::ALL {
        let one = workspace_copy(plan.name())?;
        let two = workspace_copy(plan.name())?;
        let app = one.path().join("todo-api");
        generate(&app, REFERENCE, plan)?;
        generate(&two.path().join("todo-api"), REFERENCE, plan)?;
        let h1 = tree_hash(&app)?;
        let h2 = tree_hash(&two.path().join("todo-api"))?;
        let again = generate(&app, REFERENCE, plan)?;
        let writes: usize = again.iter().map(|r| r.written.len()).sum();
        let (check, _) = cargo_fmt(one.path(), true)?;
        let before = snapshot(&app)?;
        let (_, fmt_ms) = cargo_fmt(one.path(), false)?;
        let after = snapshot(&app)?;
        let changed = before
            .iter()
            .zip(&after)
            .filter(|(a, b)| a.1 != b.1)
            .map(|(a, _)| a.0.clone())
            .collect::<Vec<_>>();
        let lock: Lock = serde_json::from_slice(&fs::read(app.join(LOCK))?)?;
        let stale = lock
            .files
            .iter()
            .filter(|(path, e)| {
                fs::read(app.join(path)).map(|b| hash(&b)).ok().as_ref() != Some(&e.hash)
            })
            .count();
        println!(
            "| {} | `{}` | {} | {} | {} | {} ({}) | {} | {:.0} ms |",
            plan.name(),
            &h1[7..19],
            if h1 == h2 { "yes" } else { "no" },
            writes,
            if check { "pass" } else { "fail" },
            changed.len(),
            if changed.is_empty() {
                "none".to_owned()
            } else {
                changed.join(", ")
            },
            stale,
            fmt_ms,
        );
    }
    Ok(())
}

/// The relative path and bytes of each file under `dir`, sorted.
fn snapshot(dir: &Path) -> Result<Vec<(String, Vec<u8>)>> {
    let mut out = Vec::new();
    walk(dir, dir, &mut out)?;
    out.sort();
    Ok(out)
}

fn walk(root: &Path, dir: &Path, out: &mut Vec<(String, Vec<u8>)>) -> Result<()> {
    for entry in fs::read_dir(dir)? {
        let p = entry?.path();
        if p.is_dir() {
            walk(root, &p, out)?;
        } else {
            let rel = p.strip_prefix(root)?.to_string_lossy().into_owned();
            out.push((rel, fs::read(&p)?));
        }
    }
    Ok(())
}

fn median(mut v: Vec<f64>) -> f64 {
    v.sort_by(f64::total_cmp);
    v[v.len() / 2]
}

/// Times the plan step (render plus format, no writes) of both reference
/// resources. The app holds the todo files when note is planned.
fn timing_table(runs: usize) -> Result<()> {
    println!(
        "Plan step time for todo plus note (render and format, no writes), median of {runs} runs:"
    );
    println!();
    println!("| Plan | Median | Min | Max | Added over plan A |");
    println!("|---|---|---|---|---|");
    let mut base = 0.0;
    for plan in Plan::ALL {
        let mut times = Vec::new();
        for _ in 0..runs {
            let dir = TempDir::new("time")?;
            copy_base(dir.path())?;
            let mut total = 0.0;
            for args in REFERENCE {
                let start = Instant::now();
                let files = plan_one(dir.path(), args, plan)?;
                total += start.elapsed().as_secs_f64() * 1000.0;
                spinup_gen::apply(dir.path(), &files, false)?;
            }
            times.push(total);
        }
        let m = median(times.clone());
        if plan == Plan::Stable {
            base = m;
        }
        println!(
            "| {} | {:.1} ms | {:.1} ms | {:.1} ms | {:+.1} ms |",
            plan.name(),
            m,
            times.iter().copied().fold(f64::INFINITY, f64::min),
            times.iter().copied().fold(0.0, f64::max),
            m - base
        );
    }
    Ok(())
}

/// Resources with longer names. The fields are those of todo, so the only
/// change is the name length. The last rows make one field name longer.
const SWEEP: &[&str] = &[
    "note",
    "event",
    "ticket",
    "invoice",
    "customer",
    "ship_note",
    "order_line",
    "price_quote",
    "product_note",
    "shipping_label",
    "customer_review",
    "subscription_plan",
    "warehouse_location",
    "customer_support_ticket",
];

fn sweep_table() -> Result<()> {
    println!(
        "Name sweep: files that rustfmt would change after each plan (fields of todo, nullable enum):"
    );
    println!();
    println!("| Resource | Name length | Plan A | Plan B | Plan C |");
    println!("|---|---|---|---|---|");
    let mut cases: Vec<(String, Vec<String>)> = SWEEP
        .iter()
        .map(|n| {
            (
                (*n).to_owned(),
                vec![
                    (*n).to_owned(),
                    "title:string".into(),
                    "done:bool".into(),
                    "due:date?".into(),
                    "priority:enum:low|normal|high?".into(),
                ],
            )
        })
        .collect();
    for field in ["delivery_priority", "customer_delivery_priority_level"] {
        cases.push((
            format!("todo, field `{field}`"),
            vec![
                "todo".into(),
                "title:string".into(),
                format!("{field}:enum:low|normal|high?"),
            ],
        ));
    }
    for (label, args) in cases {
        let mut cells = Vec::new();
        for plan in Plan::ALL {
            let dir = TempDir::new("sweep")?;
            copy_base(dir.path())?;
            let args: Vec<&str> = args.iter().map(String::as_str).collect();
            let files = plan_one(dir.path(), &args, plan)?;
            let bad = not_rustfmt_clean(dir.path(), &files)?;
            cells.push(if bad.is_empty() {
                "0".to_owned()
            } else {
                format!(
                    "{} ({})",
                    bad.len(),
                    bad.iter()
                        .map(|p| p.rsplit('/').next().unwrap_or(p))
                        .collect::<Vec<_>>()
                        .join(", ")
                )
            });
        }
        let len = label.split(',').next().map_or(0, str::len);
        println!(
            "| {label} | {len} | {} | {} | {} |",
            cells[0], cells[1], cells[2]
        );
    }
    Ok(())
}
