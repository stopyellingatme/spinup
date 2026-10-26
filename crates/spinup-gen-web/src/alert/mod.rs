//! Comment-preserving edits to custom Prometheus rules.
#[cfg(test)]
mod tests;
mod validation;

use anyhow::{Context, Result, bail};
use spinup_editors::yaml::{self, Node};
use spinup_gen::contract::{Command, Generator, Input, Output};
use spinup_vfs::plan::File;

const CUSTOM: &str = "monitoring/alerts/custom.yml";

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static ALERT: Generator = Generator {
    name: "alert",
    aliases: &[],
    short: "Add a custom Prometheus alert rule",
    usage: "<name> --expr <PromQL>",
    args,
    run: plan,
};
fn args(cmd: clap::Command) -> clap::Command {
    cmd.arg(
        clap::Arg::new("expr")
            .long("expr")
            .default_value("")
            .help("PromQL expression for the alert"),
    )
    .arg(
        clap::Arg::new("for")
            .long("for")
            .default_value("5m")
            .help("time that the expression must stay true"),
    )
    .arg(
        clap::Arg::new("severity")
            .long("severity")
            .default_value("warning")
            .help("alert severity: warning or critical"),
    )
    .arg(
        clap::Arg::new("summary")
            .long("summary")
            .default_value("")
            .help("short alert summary"),
    )
}
fn plan(input: &Input<'_>) -> Result<Output> {
    let tool = spinup_exec::lookup::look_path("promtool").ok();
    plan_with_tool(input, tool.as_deref())
}
fn plan_with_tool(input: &Input<'_>, tool: Option<&std::path::Path>) -> Result<Output> {
    if input.args.len() != 1 {
        bail!(
            "expected one alert name\nTry: spinup g alert HighErrors --expr 'sum(rate(http_server_request_duration_seconds_count{{http_response_status_code=~\"5..\"}}[5m])) > 0'"
        );
    }
    if !input.lock.packs.iter().any(|p| p == "monitoring") {
        bail!("the app has no monitoring pack\nTry: create the app with --modules ...,monitoring");
    }
    let name = &input.args[0];
    if !name.starts_with(|c: char| c.is_ascii_alphabetic() || c == '_')
        || !name.bytes().all(|c| c.is_ascii_alphanumeric() || c == b'_')
    {
        bail!(
            "invalid alert name {name:?}: use letters, digits, and underscores\nTry: spinup g alert HighErrors --expr 'up == 0'"
        );
    }
    let value = |key| {
        input
            .matches
            .try_get_one::<String>(key)
            .ok()
            .flatten()
            .map(String::as_str)
    };
    let rule = rule(
        name,
        value("expr").unwrap_or(""),
        value("for").unwrap_or("5m"),
        value("severity").unwrap_or("warning"),
        value("summary").unwrap_or(""),
    )?;
    let source = std::fs::read_to_string(input.root.join(CUSTOM)).with_context(|| {
        format!("read {CUSTOM} failed\nTry: create the app with --modules ...,monitoring")
    })?;
    let data = edit(&source, name, rule)?;
    let mut output = Output {
        next: vec!["spinup monitor alerts".into()],
        edits: vec![CUSTOM.into()],
        ..Output::default()
    };
    if let Some(tool) = tool {
        validation::validate(input, tool, data.as_bytes())?;
        output.commands.push(Command {
            dir: ".".into(),
            argv: ["promtool", "check", "rules", CUSTOM]
                .map(String::from)
                .to_vec(),
        });
    } else {
        output
            .next
            .push("promtool is unavailable; skipped alert validation".into());
    }
    output.plan.add(File {
        path: CUSTOM.into(),
        data: data.into_bytes(),
        class: "seed".into(),
        ..File::default()
    })?;
    Ok(output)
}
fn rule(name: &str, expr: &str, duration: &str, severity: &str, summary: &str) -> Result<Node> {
    if expr.trim().is_empty() {
        bail!("--expr is required\nTry: spinup g alert HighErrors --expr 'up == 0'");
    }
    if !validation::valid_duration(duration) {
        bail!("invalid --for duration {duration:?}: use a Prometheus duration\nTry: pass --for 5m");
    }
    if !["warning", "critical"].contains(&severity) {
        bail!(
            "invalid severity {severity:?}: use warning or critical\nTry: pass --severity warning"
        );
    }
    let summary = if summary.trim().is_empty() {
        format!("Alert expression is true: {expr}")
    } else {
        summary.into()
    };
    Ok(Node::map([
        ("alert", Node::str(name)),
        ("expr", Node::str(expr)),
        ("for", Node::str(duration)),
        ("labels", Node::map([("severity", Node::str(severity))])),
        ("annotations", Node::map([("summary", Node::str(summary))])),
    ]))
}
fn edit(source: &str, name: &str, rule: Node) -> Result<String> {
    let mut doc = yaml::Document::parse(source)?;
    let groups = doc.get(&["groups"]).unwrap_or_else(|| Node::seq(vec![]));
    let Some(items) = groups.items() else {
        bail!("groups must be a YAML sequence\nTry: repair {CUSTOM}");
    };
    for group in items {
        if let Some(rules) = group.get("rules").and_then(Node::items)
            && rules
                .iter()
                .any(|r| r.get("alert").and_then(Node::as_str) == Some(name))
        {
            bail!("alert {name:?} already exists\nTry: choose another alert name");
        }
    }
    if let Some(index) = items
        .iter()
        .position(|g| g.get("name").and_then(Node::as_str) == Some("custom"))
    {
        if items[index].get("rules").and_then(Node::items).is_none() {
            bail!("custom group has invalid rules\nTry: repair {CUSTOM}");
        }
        doc.append(&["groups", &index.to_string(), "rules"], &rule, true)?;
    } else {
        let group = Node::map([
            ("name", Node::str("custom")),
            ("rules", Node::seq(vec![rule])),
        ]);
        if doc.get(&["groups"]).is_some() {
            doc.append(&["groups"], &group, true)?;
        } else {
            doc.set(&["groups"], &Node::seq(vec![group]))?;
        }
    }
    Ok(doc.into_string())
}
