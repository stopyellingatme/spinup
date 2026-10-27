//! User-owned Grafana dashboard generation.
use anyhow::{Context, Result, bail};
use serde_json::{Value, json};
use spinup_gen::contract::{Generator, Input, Output};
use spinup_vfs::plan::File;

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static DASHBOARD: Generator = Generator {
    name: "dashboard",
    aliases: &[],
    short: "Add a custom Grafana dashboard",
    usage: "<name>",
    args: |cmd| {
        cmd.arg(
            clap::Arg::new("panel")
                .long("panel")
                .action(clap::ArgAction::Append)
                .help("time-series panel as title=PromQL expression"),
        )
    },
    run: plan,
};

fn parse_panel(value: &str) -> Result<(&str, &str)> {
    if let Some((title, expr)) = value.split_once('=') {
        let (title, expr) = (title.trim(), expr.trim());
        if !title.is_empty() && !expr.is_empty() {
            return Ok((title, expr));
        }
    }
    bail!(
        "invalid panel {value:?}: use title=expr (a PromQL expression)\nTry: spinup g dashboard service-health --panel 'Request rate=sum(rate(http_server_request_duration_seconds_count[5m]))'"
    )
}
fn plan(input: &Input<'_>) -> Result<Output> {
    if input.args.len() != 1 {
        bail!("expected one dashboard name\nTry: spinup g dashboard service-health");
    }
    if !input.lock.packs.iter().any(|p| p == "monitoring") {
        bail!("the app has no monitoring pack\nTry: create the app with --modules ...,monitoring");
    }
    let name = &input.args[0];
    if name.is_empty()
        || name.len() > 33
        || !name.starts_with(|c: char| c.is_ascii_lowercase())
        || !name
            .bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
    {
        bail!(
            "invalid dashboard name {name:?}: use lower-case letters, digits, and hyphens\nTry: spinup g dashboard service-health"
        );
    }
    let values: Vec<_> = input
        .matches
        .try_get_many::<String>("panel")
        .ok()
        .flatten()
        .into_iter()
        .flatten()
        .collect();
    let panels = if values.is_empty() {
        vec![(
            "Request rate",
            "sum(rate(http_server_request_duration_seconds_count[$__rate_interval]))",
        )]
    } else {
        values
            .iter()
            .map(|v| parse_panel(v))
            .collect::<Result<Vec<_>>>()?
    };
    let mut data = serde_json::to_string_pretty(&dashboard(name, &panels))
        .context("encode dashboard failed\nTry: use a valid dashboard name and panel")?;
    // Go encoding/json escapes HTML and JavaScript line separators.
    for (a, b) in [
        ("<", "\\u003c"),
        (">", "\\u003e"),
        ("&", "\\u0026"),
        ("\u{2028}", "\\u2028"),
        ("\u{2029}", "\\u2029"),
    ] {
        data = data.replace(a, b);
    }
    data.push('\n');
    let mut output = Output {
        next: vec!["spinup monitor dashboards sync".into()],
        ..Output::default()
    };
    output.plan.add(File {
        path: format!("monitoring/grafana/dashboards/custom/{name}.json"),
        data: data.into_bytes(),
        class: "seed".into(),
        ..File::default()
    })?;
    Ok(output)
}
fn dashboard(name: &str, panels: &[(&str, &str)]) -> Value {
    let datasource = json!({"type":"prometheus", "uid":"prometheus"});
    let items: Vec<_> = panels.iter().enumerate().map(|(i, (title, expr))| json!({
        "datasource":datasource, "fieldConfig":{"defaults":{"custom":{"drawStyle":"line", "fillOpacity":10, "lineWidth":1, "showPoints":"never"}, "unit":"short"}, "overrides":[]},
        "gridPos":{"h":8,"w":12,"x":(i % 2) * 12,"y":(i / 2) * 8}, "id": i+1,
        "targets":[{"datasource":datasource,"expr":expr,"legendFormat":"{{job}}","refId":"A"}], "title":title,"type":"timeseries"
    })).collect();
    json!({"editable":true,"panels":items,"schemaVersion":39,"tags":["spinup","custom"],"time":{"from":"now-6h","to":"now"},"timezone":"","title":name.replace('-', " "),"uid":format!("spinup-{name}"),"version":1})
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn panels_and_layout() {
        assert_eq!(
            parse_panel(" Errors = up == 0 ").unwrap(),
            ("Errors", "up == 0")
        );
        for v in ["bad", "=x", "x=", " = "] {
            assert!(parse_panel(v).unwrap_err().to_string().contains("Try:"));
        }
        let d = dashboard(
            "service-health",
            &[("one", "up"), ("two", "up"), ("three", "up")],
        );
        assert_eq!(d["panels"][0]["gridPos"], json!({"h":8,"w":12,"x":0,"y":0}));
        assert_eq!(d["panels"][1]["gridPos"]["x"], 12);
        assert_eq!(d["panels"][2]["gridPos"]["y"], 8);
        assert_eq!(d["uid"], "spinup-service-health");
    }
}
