//! Source splice prototype. It preserves source outside edit spans.
//! This does not implement a general Go YAML serializer.
use crate::Action;
use anyhow::{Context, Result, bail, ensure};
use tree_sitter::{Node, Parser};

fn unwrap(mut n: Node<'_>) -> Node<'_> {
    while matches!(
        n.kind(),
        "stream" | "document" | "block_node" | "flow_node" | "block_sequence_item"
    ) {
        let mut cursor = n.walk();
        let Some(child) = n
            .named_children(&mut cursor)
            .find(|c| !matches!(c.kind(), "comment" | "anchor" | "tag" | "yaml_directive"))
        else {
            break;
        };
        n = child;
    }
    n
}

fn at<'a>(node: Node<'a>, path: &[String], input: &str) -> Result<Node<'a>> {
    let node = unwrap(node);
    let Some((key, rest)) = path.split_first() else {
        return Ok(node);
    };
    let mut cursor = node.walk();
    if matches!(node.kind(), "block_mapping" | "flow_mapping") {
        for pair in node.named_children(&mut cursor) {
            let Some(k) = pair.child_by_field_name("key") else {
                continue;
            };
            if input[k.byte_range()].trim_matches(['\'', '"']) == key {
                let v = pair
                    .child_by_field_name("value")
                    .context("null value has no syntax node")?;
                return at(v, rest, input);
            }
        }
    } else if matches!(node.kind(), "block_sequence" | "flow_sequence") {
        let value = node
            .named_children(&mut cursor)
            .filter(|n| n.kind() != "comment")
            .nth(key.parse()?)
            .context("sequence index absent")?;
        return at(value, rest, input);
    }
    bail!("missing path component {key}")
}

fn indent(raw: &str, spaces: usize) -> String {
    raw.lines()
        .map(|line| {
            if line.is_empty() {
                "\n".to_string()
            } else {
                format!("{}{line}\n", " ".repeat(spaces))
            }
        })
        .collect()
}

fn line_start(input: &str, pos: usize) -> usize {
    input[..pos].rfind('\n').map_or(0, |i| i + 1)
}

fn line_end(input: &str, pos: usize) -> usize {
    input[pos..].find('\n').map_or(input.len(), |i| pos + i + 1)
}

fn content_end(node: Node<'_>, input: &str) -> usize {
    let mut cursor = node.walk();
    let last = node
        .named_children(&mut cursor)
        .filter(|c| c.kind() != "comment")
        .last();
    last.map_or(node.end_byte(), |n| {
        let end = n.end_byte();
        if end > 0 && input.as_bytes()[end - 1] == b'\n' {
            end
        } else {
            line_end(input, end)
        }
    })
}

pub fn apply(input: &str, actions: &[Action]) -> Result<String> {
    let mut parser = Parser::new();
    parser.set_language(&tree_sitter_yaml::LANGUAGE.into())?;
    let mut output = input.to_string();
    for a in actions {
        if a.op == "noop" {
            continue;
        }
        let tree = parser.parse(&output, None).context("parse canceled")?;
        ensure!(!tree.root_node().has_error(), "invalid YAML");
        match a.op.as_str() {
            "append" => {
                let target = at(tree.root_node(), &a.path, &output)?;
                let spaces = if target.kind() == "flow_sequence" {
                    let start = line_start(&output, target.start_byte());
                    output[start..].chars().take_while(|c| *c == ' ').count() + 2
                } else {
                    target.start_position().column
                };
                let raw = a.value.trim_end_matches('\n');
                let mut lines = raw.lines();
                let first = lines.next().context("empty append fragment")?;
                let block = format!(
                    "{}- {first}\n{}",
                    " ".repeat(spaces),
                    indent(&lines.collect::<Vec<_>>().join("\n"), spaces + 2)
                );
                if target.kind() == "flow_sequence" {
                    ensure!(
                        a.block,
                        "flow append without block conversion is unsupported"
                    );
                    ensure!(
                        &output[target.byte_range()] == "[]",
                        "nonempty flow append is unsupported"
                    );
                    let start = target.start_byte();
                    let replace_start =
                        if output.as_bytes().get(start.wrapping_sub(1)) == Some(&b' ') {
                            start - 1
                        } else {
                            start
                        };
                    let end = line_end(&output, target.end_byte());
                    if output[target.end_byte()..end].trim().starts_with('#') {
                        bail!("flow-to-block comment relocation needs policy");
                    }
                    output.replace_range(replace_start..end, &format!("\n{block}"));
                } else {
                    ensure!(
                        target.kind() == "block_sequence",
                        "append target is not a sequence"
                    );
                    output.insert_str(content_end(target, &output), &block);
                }
            }
            "set" => {
                if let Ok(target) = at(tree.root_node(), &a.path, &output) {
                    let raw = a.value.trim_end_matches('\n');
                    let old = &output[target.byte_range()];
                    let replacement = if !raw.contains('\n') {
                        if old.starts_with('\'') {
                            format!("'{}'", raw.replace('\'', "''"))
                        } else if old.starts_with('"') {
                            serde_json::to_string(raw)?
                        } else {
                            raw.to_string()
                        }
                    } else if matches!(
                        target.kind(),
                        "block_mapping" | "block_sequence" | "block_scalar"
                    ) {
                        let spaces = if target.kind() == "block_scalar" {
                            output[line_start(&output, target.start_byte())..]
                                .chars()
                                .take_while(|c| *c == ' ')
                                .count()
                        } else {
                            target.start_position().column
                        };
                        let rendered = indent(raw, spaces);
                        rendered[spaces..].to_string()
                    } else {
                        bail!("scalar-to-block replacement needs policy");
                    };
                    output.replace_range(target.byte_range(), &replacement);
                } else {
                    let (key, path) = a.path.split_last().context("empty set path")?;
                    let parent = at(tree.root_node(), path, &output)?;
                    ensure!(
                        parent.kind() == "block_mapping",
                        "insert parent is not a block mapping"
                    );
                    let spaces = parent.start_position().column;
                    let raw = a.value.trim_end_matches('\n');
                    let pair = if raw.contains('\n') {
                        format!("{}{key}:\n{}", " ".repeat(spaces), indent(raw, spaces + 2))
                    } else {
                        format!("{}{key}: {raw}\n", " ".repeat(spaces))
                    };
                    output.insert_str(content_end(parent, &output), &pair);
                }
            }
            "delete" => {
                let target = at(tree.root_node(), &a.path, &output)?;
                let mut parent = target.parent();
                let pair = loop {
                    let n = parent.context("delete needs a mapping pair")?;
                    if n.kind() == "block_mapping_pair" {
                        break n;
                    }
                    parent = n.parent();
                };
                let start = line_start(&output, pair.start_byte());
                let end = content_end(pair, &output);
                output.replace_range(start..end, "");
            }
            op => bail!("unsupported operation {op}"),
        }
    }
    let tree = parser.parse(&output, None).context("parse canceled")?;
    ensure!(
        !tree.root_node().has_error(),
        "splice produced invalid YAML"
    );
    Ok(output)
}
