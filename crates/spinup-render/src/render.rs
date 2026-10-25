//! Render pack sources into a deterministic plan without writing files.

use anyhow::{Result, bail};
use minijinja::{Environment, value::Serde};
use serde_json::{Map, Value};
use spinup_packs::loader::{FileSpec, Pack};
use spinup_stack::stack::Stack;
use spinup_vfs::{
    path::clean_path,
    plan::{File, Plan},
};
use std::{collections::BTreeMap, sync::LazyLock};

/// Every input needed for deterministic rendering.
#[derive(Debug, Clone, Copy)]
pub struct Input<'a> {
    /// Packs in dependency order.
    pub packs: &'a [Pack],
    /// Explicit answers. Rendering does not change them.
    pub answers: &'a Map<String, Value>,
    /// Stack pins of this template version.
    pub stack: Option<&'a Stack>,
    /// The selected template version.
    pub template_version: &'a str,
}

impl Default for Input<'_> {
    fn default() -> Self {
        static EMPTY: LazyLock<Map<String, Value>> = LazyLock::new(Map::new);
        Self {
            packs: &[],
            answers: &EMPTY,
            stack: None,
            template_version: "",
        }
    }
}

/// Renders paths and `.tmpl` files in declaration order. R1 does not format sources.
pub fn render(input: Input<'_>) -> Result<Plan> {
    let mut answers = crate::answers::validate_answers(input.packs, input.answers)?;
    answers.insert(
        "Stack".into(),
        serde_json::to_value(input.stack).map_err(|e| {
            anyhow::anyhow!("render stack versions: {e}\nTry: supply valid stack versions")
        })?,
    );
    answers.insert("TemplateVersion".into(), input.template_version.into());
    let context: minijinja::Value = Serde(serde_json::json!({"data": answers})).into();
    let env = crate::env::environment(input.stack);
    let mut plan = Plan::new();
    let mut origins = BTreeMap::new();
    for pack in input.packs {
        for spec in &pack.files {
            let origin = format!("{}/{}", pack.name, spec.source);
            let enabled = spec
                .matches(&answers)
                .map_err(|e| anyhow::anyhow!("render {origin}: {e}"))?;
            if !enabled {
                continue;
            }
            let file = render_file(pack, spec, &env, &context)?;
            if let Some(first) = origins.insert(file.path.clone(), origin.clone()) {
                bail!(
                    "render duplicate output {:?}: templates {first} and {origin}\nTry: give each template a unique output path",
                    file.path
                );
            }
            plan.add(file).map_err(|e| {
                let previous = plan
                    .files
                    .iter()
                    .map(|f| f.template.as_str())
                    .collect::<Vec<_>>()
                    .join(", ");
                anyhow::anyhow!("stage output from {origin} after {previous}: {e}")
            })?;
        }
    }
    Ok(plan)
}

fn render_file(
    pack: &Pack,
    spec: &FileSpec,
    env: &Environment<'_>,
    context: &minijinja::Value,
) -> Result<File> {
    let origin = format!("{}/{}", pack.name, spec.source);
    let path = execute(env, &format!("{origin} output path"), &spec.path, context)?;
    let clean = clean_path(&path).map_err(|e| anyhow::anyhow!("render path from {origin}: {e}"))?;
    if clean != path || path.contains('\0') {
        bail!(
            "render path {path:?} from {origin}: path is not clean\nTry: use a clean relative output path"
        );
    }
    if !matches!(spec.class.as_str(), "managed" | "scaffold" | "seed") {
        bail!(
            "render {path:?} from {origin}: invalid class {:?}\nTry: use managed, scaffold, or seed",
            spec.class
        );
    }
    if clean_path(&spec.source).ok().as_deref() != Some(spec.source.as_str())
        || spec.source.contains('\0')
    {
        bail!(
            "read template {origin}: source path is not clean\nTry: use a clean relative source path"
        );
    }
    let mode = spec.permissions()?;
    let source = pack.fs.get(&spec.source).ok_or_else(|| anyhow::anyhow!("read template {origin} for {path:?}: file does not exist\nTry: provide a readable source file"))?;
    let content = if spec.source.ends_with(".tmpl") {
        let text = std::str::from_utf8(&source.data).map_err(|e| {
            anyhow::anyhow!("read template {origin}: {e}\nTry: use UTF-8 template source")
        })?;
        execute(env, &origin, text, context)?.into_bytes()
    } else {
        source.data.to_vec()
    };
    Ok(File {
        path,
        data: spinup_manifest::hash::normalize(&content),
        mode,
        class: spec.class.clone(),
        template: origin,
        ..File::default()
    })
}

/// Executes one template and reports its origin on syntax or answer errors.
pub fn execute(
    env: &Environment<'_>,
    name: &str,
    source: &str,
    context: &minijinja::Value,
) -> Result<String> {
    let template = env.template_from_named_str(name, source).map_err(|e| {
        anyhow::anyhow!("parse template {name}: {e}\nTry: correct the template syntax")
    })?;
    template.render(context.clone()).map_err(|e| {
        anyhow::anyhow!(
            "execute template {name}: {e}\nTry: correct the template answers or stack lookup"
        )
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use spinup_packs::{
        embed::embedded,
        loader::{FileSpec, load},
    };
    use spinup_vfs::tree::Tree;

    #[test]
    fn sample_determinism() {
        let pack = load(&embedded().unwrap(), "_sample").unwrap();
        let packs = [pack];
        let answers = Map::new();
        let input = Input {
            packs: &packs,
            answers: &answers,
            stack: Some(spinup_stack::load::embedded()),
            template_version: "0.1.0",
        };
        let first = render(input).unwrap();
        assert_eq!(first, render(input).unwrap());
        assert_eq!(
            first
                .files
                .iter()
                .map(|f| f.path.as_str())
                .collect::<Vec<_>>(),
            [
                "api/cmd/demo/main.txt",
                "versions.txt",
                "README.md",
                "database.txt",
                "scripts/start.sh",
                "greeting.txt"
            ]
        );
        assert_eq!(first.get("scripts/start.sh").unwrap().mode, 0o755);
        assert_eq!(first.get("README.md").unwrap().class, "seed");
        assert!(
            first
                .files
                .iter()
                .all(|f| f.template.starts_with("sample/files/")
                    && f.data.ends_with(b"\n")
                    && !f.data.contains(&b'\r'))
        );
        let answers = json!({"auth":true,"modules":["jobs"]})
            .as_object()
            .unwrap()
            .clone();
        assert_eq!(
            render(Input {
                answers: &answers,
                ..input
            })
            .unwrap()
            .files
            .len(),
            8
        );
    }

    fn pack(output: &str, source: &str, data: &[u8]) -> Pack {
        let mut fs = Tree::new();
        fs.insert(source, data.to_vec(), 0o644).unwrap();
        Pack {
            name: "example".into(),
            fs,
            files: vec![FileSpec {
                path: output.into(),
                source: source.into(),
                class: "managed".into(),
                ..FileSpec::default()
            }],
            ..Pack::default()
        }
    }

    #[test]
    fn copying_and_normalization() {
        for (source, data, want) in [
            ("plain", b"a\r\nb\r\n\r\n".as_slice(), b"a\nb\n".as_slice()),
            ("plain", b"[[ data.missing ]]", b"[[ data.missing ]]\n"),
            ("plain", b"\0a\r\n", b"\0a\r\n"),
            ("plain", b"", b""),
            ("plain.tmpl", b"a\r\nb\r\n\r\n", b"a\nb\n"),
        ] {
            let packs = [pack("out.txt", source, data)];
            assert_eq!(
                render(Input {
                    packs: &packs,
                    ..Input::default()
                })
                .unwrap()
                .files[0]
                    .data,
                want
            );
        }
        let packs = [pack("out.go", "plain.tmpl", b"package broken func")];
        assert_eq!(
            render(Input {
                packs: &packs,
                ..Input::default()
            })
            .unwrap()
            .files[0]
                .data,
            b"package broken func\n"
        );
    }

    #[test]
    fn errors_and_collisions() {
        for (body, path) in [
            ("[[ data.missing ]]", "out.txt"),
            ("[[", "out.txt"),
            ("[[ image('missing') ]]", "out.txt"),
            ("[[ indent(-1, 'x') ]]", "out.txt"),
            ("[[ join(',', 'x') ]]", "out.txt"),
            ("[[ has('x', 'x') ]]", "out.txt"),
            ("x", "../escape"),
            ("x", "a//b"),
            ("x", "[[ data.missing ]]"),
        ] {
            let packs = [pack(path, "file.tmpl", body.as_bytes())];
            let error = render(Input {
                packs: &packs,
                ..Input::default()
            })
            .unwrap_err()
            .to_string();
            assert!(
                error.contains("example/file.tmpl") && error.contains("\nTry:"),
                "{error}"
            );
        }
        for path in ["same", "SAME"] {
            let packs = [pack("same", "first", b"a"), pack(path, "second", b"b")];
            let error = render(Input {
                packs: &packs,
                ..Input::default()
            })
            .unwrap_err()
            .to_string();
            assert!(
                error.contains("first") && error.contains("second"),
                "{error}"
            );
        }
    }
}
