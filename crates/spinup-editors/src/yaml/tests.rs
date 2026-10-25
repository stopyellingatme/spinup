//! Tests of the YAML editor: the R0.1 Go fixtures and the ports of the Go codemod tests.

use std::fs;
use std::path::{Path, PathBuf};

use serde_json::Value;

use super::*;

struct Action {
    op: String,
    path: Vec<String>,
    value: String,
    block: bool,
}

struct Fixture {
    id: String,
    mode: String,
    actions: Vec<Action>,
    comments: Vec<String>,
    input: String,
    expected: String,
}

fn fixtures() -> Vec<Fixture> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("src/testdata/yaml");
    let mut dirs: Vec<PathBuf> = fs::read_dir(&root)
        .expect("fixture directory")
        .map(|e| e.expect("entry").path())
        .filter(|p| p.is_dir())
        .collect();
    dirs.sort();
    dirs.into_iter()
        .map(|dir| {
            let edit: Value =
                serde_json::from_str(&fs::read_to_string(dir.join("edit.json")).unwrap()).unwrap();
            let strings = |v: &Value| -> Vec<String> {
                v.as_array()
                    .unwrap()
                    .iter()
                    .map(|s| s.as_str().unwrap().to_string())
                    .collect()
            };
            Fixture {
                id: edit["id"].as_str().unwrap().to_string(),
                mode: edit["mode"].as_str().unwrap().to_string(),
                actions: edit["actions"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|a| Action {
                        op: a["op"].as_str().unwrap().to_string(),
                        path: strings(&a["path"]),
                        value: a["value"].as_str().unwrap().to_string(),
                        block: a["block"].as_bool().unwrap_or(false),
                    })
                    .collect(),
                comments: strings(&edit["comments"]),
                input: fs::read_to_string(dir.join("input.yaml")).unwrap(),
                expected: fs::read_to_string(dir.join("expected.yaml")).unwrap(),
            }
        })
        .collect()
}

/// Runs the fixture actions with the splice editor.
fn apply_node(input: &str, actions: &[Action]) -> Result<String, Error> {
    let (out, _) = edit(input, |doc| {
        for action in actions {
            let path: Vec<&str> = action.path.iter().map(String::as_str).collect();
            match action.op.as_str() {
                "noop" => {}
                "set" => doc.set(&path, &Node::parse(&action.value)?)?,
                "append" => doc.append(&path, &Node::parse(&action.value)?, action.block)?,
                "delete" => {
                    doc.delete(&path)?;
                }
                other => return Err(Error::new(format!("unknown operation {other}"))),
            }
        }
        Ok(true)
    })?;
    Ok(out)
}

/// Runs the fixture actions like the Go Compose writer: decode, change, and marshal.
fn apply_map(input: &str, actions: &[Action]) -> Result<String, Error> {
    let mut doc = Node::parse(input)?;
    for action in actions {
        let path: Vec<&str> = action.path.iter().map(String::as_str).collect();
        let (key, parent) = path.split_last().unwrap();
        let value = Node::parse(&action.value)?;
        let parent = doc
            .at_mut(parent)
            .ok_or_else(|| Error::new("missing parent"))?;
        if !parent.insert(key, value) {
            return Err(Error::new("parent is not a mapping"));
        }
    }
    marshal_value(&doc, 4)
}

/// The fixtures where the splice editor differs from go-yaml on purpose. Each one keeps
/// every comment of the input. Go re-encodes the whole document, so it moves or drops
/// comments in these cases.
const DEVIATIONS: &[(&str, &str)] = &[
    (
        "alert-01",
        "go-yaml drops the line comment of the flow sequence that becomes a block sequence; the editor keeps it on the key line",
    ),
    (
        "codemod-delete",
        "go-yaml moves the comments of the removed entry to the foot of the mapping; the editor keeps them in place",
    ),
    (
        "codemod-insert",
        "go-yaml writes the new key after the foot comment of the document and a blank line; the editor writes it after the last entry",
    ),
];

#[test]
fn go_fixtures_match_except_the_recorded_deviations() {
    let fixtures = fixtures();
    assert_eq!(fixtures.len(), 32, "the R0.1 fixture set changed");
    let mut failures = Vec::new();
    for f in &fixtures {
        let out = match f.mode.as_str() {
            "map" => apply_map(&f.input, &f.actions),
            _ => apply_node(&f.input, &f.actions),
        };
        let out = match out {
            Ok(out) => out,
            Err(err) => {
                failures.push(format!("{}: error: {err}", f.id));
                continue;
            }
        };
        let deviation = DEVIATIONS.iter().any(|(id, _)| *id == f.id);
        if f.mode == "map" {
            // The Go Compose writer removes comments. The editor keeps that behavior.
            for comment in &f.comments {
                assert!(
                    !out.contains(comment.as_str()),
                    "{}: the map writer kept {comment}",
                    f.id
                );
            }
        } else {
            for comment in &f.comments {
                if !out.contains(comment.as_str()) {
                    failures.push(format!("{}: lost comment {comment}\n{out}", f.id));
                }
            }
        }
        if out == f.expected {
            if deviation {
                failures.push(format!("{}: now equals Go; remove the deviation", f.id));
            }
        } else if !deviation {
            failures.push(format!(
                "{}: output differs from Go\n--- got\n{out}--- want\n{}",
                f.id, f.expected
            ));
        }
        // The edit result is valid YAML with the same data as the Go output.
        assert_eq!(
            yaml_serde::from_str::<yaml_serde::Value>(&out).ok(),
            yaml_serde::from_str::<yaml_serde::Value>(&f.expected).ok(),
            "{}: the data differs from Go",
            f.id
        );
    }
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}

const EXAMPLE: &str = include_str!("../testdata/example.yaml");
const EXAMPLE_COMMENTS: [&str; 7] = [
    "File comment.",
    "Mapping comment.",
    "Name comment.",
    "List comment.",
    "Item comment.",
    "Other comment.",
    "End comment.",
];

type Change = fn(&mut Document) -> Result<bool, Error>;

#[test]
fn edit_keeps_comments_and_is_idempotent() {
    let (same, changed) = edit(EXAMPLE, |_| Ok(false)).unwrap();
    assert!(!changed && same == EXAMPLE);
    let cases: [(&str, Change); 3] = [
        ("set", |doc| {
            doc.set(&["app", "name"], &Node::str("new")).map(|()| true)
        }),
        ("insert", |doc| {
            doc.set(&["new", "child"], &Node::str("value"))
                .map(|()| true)
        }),
        ("delete", |doc| doc.delete(&["app", "items"])),
    ];
    for (name, change) in cases {
        let (out, changed) = edit(EXAMPLE, change).unwrap();
        assert!(changed, "{name}");
        for comment in EXAMPLE_COMMENTS {
            assert!(out.contains(comment), "{name}: lost {comment}:\n{out}");
        }
        let (again, changed) = edit(&out, change).unwrap();
        assert!(
            !changed && again == out,
            "{name}: not idempotent:\n{out}\n{again}"
        );
    }
}

#[test]
fn edit_writes_in_place() {
    let (out, _) = edit(EXAMPLE, |doc| {
        doc.set(&["app", "name"], &Node::str("new")).map(|()| true)
    })
    .unwrap();
    assert_eq!(out, EXAMPLE.replace("'old'", "'new'"));
    let (out, _) = edit(EXAMPLE, |doc| doc.delete(&["app", "items"])).unwrap();
    assert_eq!(
        out,
        "# File comment.\napp: # Mapping comment.\n  name: 'old' # Name comment.\n  # List comment.\n  # Item comment.\nother: true # Other comment.\n# End comment.\n"
    );
    let (out, _) = edit(EXAMPLE, |doc| {
        doc.set(&["new", "child"], &Node::str("value"))
            .map(|()| true)
    })
    .unwrap();
    assert_eq!(
        out,
        format!(
            "{}new:\n  child: value\n# End comment.\n",
            EXAMPLE.trim_end_matches("# End comment.\n")
        )
    );
}

#[test]
fn edit_edges() {
    let (out, changed) = edit("", |doc| {
        doc.set(&["a", "b"], &Node::str("v")).map(|()| true)
    })
    .unwrap();
    assert!(changed);
    assert_eq!(out, "a:\n  b: v\n");
    let err = edit("a: 1\n---\nb: 2", |_| Ok(false)).unwrap_err();
    assert!(err.to_string().contains("one document"), "{err}");
    assert!(edit("a: [", |_| Ok(false)).is_err());
    let mut doc = Document::parse("a: 1").unwrap();
    let err = doc.set(&["a", "b"], &Node::null()).unwrap_err().to_string();
    assert!(
        err.contains("path [\"a\"] crosses a non-mapping value"),
        "{err}"
    );
    assert!(
        err.ends_with("\nTry: check the document and the migration input"),
        "{err}"
    );
    assert!(doc.get(&["missing"]).is_none());
    assert!(!doc.delete(&["missing"]).unwrap());
    assert!(!doc.delete(&[]).unwrap());
    assert!(doc.set(&[], &Node::null()).is_err());
    assert!(doc.append(&["a"], &Node::null(), false).is_err());
    assert!(doc.append(&["missing"], &Node::null(), false).is_err());
}

#[test]
fn replace_moves_inner_comments_above_the_key() {
    let src = "# Header.\napp:\n  # Name.\n  name: old # Inline.\nother: true\n";
    let change: Change = |doc| doc.set(&["app"], &Node::str("new")).map(|()| true);
    let (out, changed) = edit(src, change).unwrap();
    assert!(changed);
    assert_eq!(
        out,
        "# Header.\n# Name.\n# Inline.\napp: new\nother: true\n"
    );
    let (again, changed) = edit(&out, change).unwrap();
    assert!(!changed && again == out);
}

#[test]
fn set_replaces_a_null_intermediate_mapping() {
    let (out, changed) = edit("# about a\na: # remains\n", |doc| {
        doc.set(&["a", "b"], &Node::str("v")).map(|()| true)
    })
    .unwrap();
    assert!(changed);
    assert_eq!(out, "# about a\na: # remains\n  b: v\n");
    let (out, _) = edit("a: ~\nb: 1\n", |doc| {
        doc.set(&["a", "c", "d"], &Node::plain("1")).map(|()| true)
    })
    .unwrap();
    assert_eq!(out, "a:\n  c:\n    d: 1\nb: 1\n");
}

#[test]
fn values_keep_quotes_and_quote_new_strings() {
    let src = "a: \"x\" # q\nb: 'y'\nc: plain\nd: {e: f}\n";
    let (out, _) = edit(src, |doc| {
        doc.set(&["a"], &Node::str("z"))?;
        doc.set(&["b"], &Node::str("it's"))?;
        doc.set(&["c"], &Node::str("true"))?;
        doc.set(&["d", "e"], &Node::str("a: b"))?;
        doc.set(&["d", "g"], &Node::str("h"))?;
        Ok(true)
    })
    .unwrap();
    assert_eq!(
        out,
        "a: \"z\" # q\nb: 'it''s'\nc: \"true\"\nd: {e: 'a: b', g: h}\n"
    );
}

#[test]
fn delete_handles_sequence_items_flow_maps_and_last_entries() {
    let src = "list:\n  - k: v # c\n    j: w\nflow: {a: 1, b: 2}\nonly:\n  x: 1\n";
    let (out, _) = edit(src, |doc| {
        doc.delete(&["list", "0", "k"])?;
        doc.delete(&["flow", "b"])?;
        doc.delete(&["only", "x"])?;
        Ok(true)
    })
    .unwrap();
    assert_eq!(out, "list:\n  # c\n  - j: w\nflow: {a: 1}\nonly: {}\n");
    let (out, _) = edit("x: 1\n", |doc| doc.delete(&["x"])).unwrap();
    assert_eq!(out, "{}\n");
}

#[test]
fn append_converts_null_and_flow_sequences() {
    let src = "a:\nb: []\nc: [x]\nd:\n  - y\n";
    let item = Node::map([("k", Node::str("v"))]);
    let (out, _) = edit(src, |doc| {
        doc.append(&["a"], &item, false)?;
        doc.append(&["b"], &item, true)?;
        doc.append(&["c"], &item, false)?;
        doc.append(&["d"], &item, false)?;
        Ok(true)
    })
    .unwrap();
    assert_eq!(
        out,
        "a:\n  - k: v\nb:\n  - k: v\nc: [x, {k: v}]\nd:\n  - y\n  - k: v\n"
    );
}

#[test]
fn edits_reach_into_flow_collections() {
    let src = "a: [x: 1, y]\nb: {c: [d]}\n";
    let (out, _) = edit(src, |doc| {
        doc.set(&["a", "0", "x"], &Node::plain("2"))?;
        doc.set(&["a", "1"], &Node::str("z w"))?;
        doc.append(&["b", "c"], &Node::str("e"), true)?;
        Ok(true)
    })
    .unwrap();
    assert_eq!(out, "a: [x: 2, z w]\nb: {c: [d, e]}\n");
}

#[test]
fn get_reads_values() {
    let doc = Document::parse("a:\n  - b: 'x'\nc:\n").unwrap();
    assert_eq!(doc.get(&["a", "0", "b"]).unwrap().as_str(), Some("x"));
    assert_eq!(doc.get(&["a"]).unwrap().items().unwrap().len(), 1);
    assert!(doc.get(&["c"]).unwrap().is_null());
    assert!(doc.get(&["a", "1"]).is_none());
    assert!(doc.get(&["c", "d"]).is_none());
    assert!(Document::parse("").unwrap().get(&[]).is_none());
}

/// The Compose writer removes comments, as the Go writer does (ADR 0020 §7).
#[test]
fn compose_writer_removes_comments_like_go() {
    let src = "# Compose\nservices: # all\n    postgres:\n        image: x # pinned\n";
    let out = marshal_value(&Node::parse(src).unwrap(), 4).unwrap();
    assert_eq!(out, "services:\n    postgres:\n        image: x\n");
}
