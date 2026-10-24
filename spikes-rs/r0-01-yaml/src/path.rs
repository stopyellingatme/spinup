//! Native yamlpath routes and yamlpatch operations. No Go output is consulted.
use crate::Action;
use anyhow::{Context, Result, bail};
use yamlpatch::{Op, Patch};
use yamlpath::{Component, Document, Route};

fn route(parts: &[String]) -> Route<'_> {
    Route::from(
        parts
            .iter()
            .map(|s| {
                s.parse::<usize>()
                    .map(Component::Index)
                    .unwrap_or_else(|_| Component::from(s.as_str()))
            })
            .collect::<Vec<_>>(),
    )
}

pub fn apply(input: &str, actions: &[Action]) -> Result<String> {
    let mut doc = Document::new(input)?;
    for a in actions {
        if a.op == "noop" {
            continue;
        }
        let r = route(&a.path);
        let value = || yaml_serde::from_str(&a.value).context("parse edit fragment");
        let (r, operation) = match a.op.as_str() {
            "append" => (r, Op::Append { value: value()? }),
            "delete" => (r, Op::Remove),
            "set" if doc.query_exists(&r) => (r, Op::Replace(value()?)),
            "set" => {
                let (key, parent) = a.path.split_last().context("empty set path")?;
                (
                    route(parent),
                    Op::Add {
                        key: key.clone(),
                        value: value()?,
                    },
                )
            }
            op => bail!("unsupported operation {op}"),
        };
        doc = yamlpatch::apply_yaml_patches(
            &doc,
            &[Patch {
                route: r,
                operation,
            }],
        )?;
    }
    Ok(doc.source().to_string())
}
