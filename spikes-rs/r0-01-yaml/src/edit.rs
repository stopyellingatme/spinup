//! Native yaml-edit CST mutations. Fragments preserve the requested node styles.
use crate::Action;
use anyhow::{Context, Result, bail};
use std::str::FromStr;
use yaml_edit::{Document, YamlNode};

fn root(doc: &Document) -> Result<YamlNode> {
    doc.as_mapping()
        .map(YamlNode::Mapping)
        .context("root is not a mapping")
}

fn at(mut node: YamlNode, path: &[String]) -> Result<YamlNode> {
    for key in path {
        node = if let Some(seq) = node.as_sequence() {
            seq.get(key.parse()?)
        } else if let Some(map) = node.as_mapping() {
            map.get(key.as_str())
        } else {
            None
        }
        .with_context(|| format!("missing path component {key}"))?;
    }
    Ok(node)
}

pub fn apply(input: &str, actions: &[Action]) -> Result<String> {
    let doc = Document::from_str(input)?;
    for a in actions {
        if a.op == "noop" {
            continue;
        }
        if a.op == "delete" {
            let (key, path) = a.path.split_last().context("empty delete path")?;
            let parent = at(root(&doc)?, path)?;
            parent
                .as_mapping()
                .context("delete parent is not a mapping")?
                .remove(key.as_str());
            continue;
        }
        // A wrapper allows all fragment kinds through one native CST API.
        let wrapped = format!(
            "value:\n{}",
            a.value
                .lines()
                .map(|s| format!("  {s}\n"))
                .collect::<String>()
        );
        let fragment = Document::from_str(&wrapped)?;
        let value = fragment
            .as_mapping()
            .and_then(|m| m.get("value"))
            .context("invalid edit fragment")?;
        match a.op.as_str() {
            "append" => {
                let seq = at(root(&doc)?, &a.path)?;
                seq.as_sequence()
                    .context("append target is not a sequence")?
                    .push(value);
            }
            "set" => {
                let (key, path) = a.path.split_last().context("empty set path")?;
                let parent = at(root(&doc)?, path)?;
                if let Some(seq) = parent.as_sequence() {
                    seq.set(key.parse()?, value);
                } else {
                    parent
                        .as_mapping()
                        .context("set parent is not a mapping")?
                        .set(key.as_str(), value);
                }
            }
            op => bail!("unsupported operation {op}"),
        }
    }
    Ok(doc.to_string())
}
