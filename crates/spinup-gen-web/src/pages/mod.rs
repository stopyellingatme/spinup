//! Resource pages rendered from the neutral Go templates.
mod fields;
#[cfg(test)]
mod tests;

use anyhow::{Context, Result, bail};
use fields::PageField;
use serde::{Deserialize, Serialize};
use spinup_gen::{
    contract::PostStep,
    fields::Name,
    part::{Part, PartPlan, Spec},
};
use spinup_vfs::plan::{File, Plan};
use std::collections::BTreeMap;
use std::path::Path;

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::part::RESOURCE_PARTS)]
static PAGES: Part = Part {
    name: "pages",
    plan: plan_pages,
    paths: Some(paths),
};

const FILES: &[(&str, &str)] = &[
    (
        "+page.server.ts",
        include_str!("../../templates/resource/listServerTS.tmpl"),
    ),
    (
        "+page.svelte",
        include_str!("../../templates/resource/listSvelte.tmpl"),
    ),
    (
        "[id]/+page.server.ts",
        include_str!("../../templates/resource/itemServerTS.tmpl"),
    ),
    (
        "[id]/+page.svelte",
        include_str!("../../templates/resource/itemSvelte.tmpl"),
    ),
    (
        "[id]/edit/+page.server.ts",
        include_str!("../../templates/resource/editServerTS.tmpl"),
    ),
    (
        "[id]/edit/+page.svelte",
        include_str!("../../templates/resource/editSvelte.tmpl"),
    ),
    (
        "fields.svelte",
        include_str!("../../templates/resource/fieldsSvelte.tmpl"),
    ),
    (
        "new/+page.server.ts",
        include_str!("../../templates/resource/newServerTS.tmpl"),
    ),
    (
        "new/+page.svelte",
        include_str!("../../templates/resource/newSvelte.tmpl"),
    ),
    (
        "schema.ts",
        include_str!("../../templates/resource/schemaTS.tmpl"),
    ),
];

/// Returns current page paths and the legacy form path for destruction.
pub fn paths(name: &Name) -> Vec<String> {
    FILES
        .iter()
        .map(|(p, _)| format!("web/src/routes/{}/{p}", name.plural_kebab()))
        .chain(std::iter::once(format!(
            "web/src/routes/{}/form.ts",
            name.plural_kebab()
        )))
        .collect()
}

/// Plans the resource pages and requests client generation.
///
/// # Errors
/// Returns an error if embed mode or missing web inputs prevent page generation.
pub fn plan_pages(spec: &Spec<'_>) -> Result<PartPlan> {
    if !spec.lock.packs.iter().any(|p| p == "web") {
        return Ok(PartPlan::default());
    }
    if answer(spec.lock, "frontend_mode").as_deref() == Some("embed") {
        bail!(
            "cannot generate resource pages in embed mode: the pages require SvelteKit form actions\nTry: pass --api-only, or use an app with --frontend-mode node"
        );
    }
    check_inputs(spec.root)?;
    let data = PageData::new(spec);
    let env = spinup_render::env::environment(None);
    let mut plan = Plan::new();
    for (path, source) in FILES {
        let bytes = env
            .template_from_named_str(path, source)
            .and_then(|t| t.render(minijinja::context!(data => minijinja::value::Serde(&data))))
            .with_context(|| {
                format!("render the page template {path} failed\nTry: report this generator bug")
            })?;
        plan.add(File {
            path: format!("web/src/routes/{}/{path}", spec.name.plural_kebab()),
            data: bytes.into_bytes(),
            class: "scaffold".into(),
            ..File::default()
        })?;
    }
    Ok(PartPlan {
        plan,
        post_steps: vec![PostStep::Client],
    })
}

pub(crate) fn answer(lock: &spinup_manifest::lock::Lock, key: &str) -> Option<String> {
    lock.answers
        .get(key)
        .and_then(|v| serde_json::from_str::<String>(v.get()).ok())
}

fn check_inputs(root: &Path) -> Result<()> {
    let mut missing = Vec::new();
    let components = [
        "alert-dialog",
        "badge",
        "button",
        "card",
        "checkbox",
        "dropdown-menu",
        "input",
        "label",
        "pagination",
        "table",
    ];
    let paths = components
        .iter()
        .map(|n| format!("web/src/lib/components/ui/{n}/"))
        .chain([
            "web/src/lib/components/app/field.svelte".into(),
            "web/src/lib/components/app/page-header.svelte".into(),
            "web/src/lib/components/app/row-menu.svelte".into(),
        ]);
    for path in paths {
        if !exists(root, &path)? {
            missing.push(path);
        }
    }
    let path = root.join("web/package.json");
    let source = match std::fs::read(&path) {
        Ok(bytes) => bytes,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => b"{}".to_vec(),
        Err(e) => {
            return Err(anyhow::anyhow!(
                "read web/package.json: {e}\nTry: check the file permissions"
            ));
        }
    };
    #[derive(Default, Deserialize)]
    struct Package {
        dependencies: Option<BTreeMap<String, Option<String>>>,
        #[serde(rename = "devDependencies")]
        dev_dependencies: Option<BTreeMap<String, Option<String>>>,
    }
    let package: Option<Package> = serde_json::from_slice(&source)
        .context("read web/package.json failed\nTry: fix the JSON syntax of web/package.json")?;
    let package = package.unwrap_or_default();
    for name in ["sveltekit-superforms", "zod", "svelte-sonner"] {
        if ![&package.dependencies, &package.dev_dependencies]
            .into_iter()
            .flatten()
            .any(|deps| deps.contains_key(name))
        {
            missing.push(format!("the npm package {name}"));
        }
    }
    if !missing.is_empty() {
        bail!(
            "cannot generate resource pages: the app is missing {}\nTry: spinup upgrade (it adds the new web pack files), then run the generator again",
            missing.join(", ")
        );
    }
    Ok(())
}

pub(crate) fn exists(root: &Path, path: &str) -> Result<bool> {
    match std::fs::metadata(root.join(path)) {
        Ok(_) => Ok(true),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(e) => Err(anyhow::anyhow!(
            "check {path}: {e}\nTry: check the file permissions"
        )),
    }
}

#[derive(Default, Serialize)]
#[serde(rename_all = "PascalCase")]
struct PageData {
    pascal: String,
    words: String,
    plural_words: String,
    name: String,
    title: String,
    route: String,
    #[serde(rename = "APIPath")]
    api_path: String,
    auth: bool,
    fields: Vec<PageField>,
    title_col: String,
    title_label: String,
    toggle: Option<PageField>,
    columns: Vec<PageField>,
    enums: Vec<PageField>,
    list_badge: bool,
    date: bool,
    time: bool,
    #[serde(rename = "JSON")]
    json: bool,
    control: bool,
    input: bool,
    checkbox: bool,
}

pub(crate) fn capitalize(text: &str) -> String {
    let mut chars = text.chars();
    chars.next().map_or_else(String::new, |c| {
        c.to_uppercase().to_string() + chars.as_str()
    })
}

impl PageData {
    fn new(spec: &Spec<'_>) -> Self {
        let words = spec.name.snake().replace('_', " ");
        let plural_words = spec.name.plural_snake().replace('_', " ");
        let mut data = Self {
            pascal: spec.name.pascal(),
            name: capitalize(&words),
            title: capitalize(&plural_words),
            words,
            plural_words,
            route: format!("/{}", spec.name.plural_kebab()),
            api_path: format!("/api/{}", spec.name.plural_snake()),
            auth: spec.lock.packs.iter().any(|p| p == "auth"),
            ..Self::default()
        };
        data.title_label = data.name.clone();
        for field in &spec.fields {
            let p = data.field(field);
            data.fields.push(p.clone());
            if !p.labels.is_empty() {
                data.enums.push(p.clone());
            }
            if field.r#type.name == "string" && data.title_col.is_empty() {
                data.title_col.clone_from(&p.col);
                data.title_label.clone_from(&p.label);
            } else if field.r#type.name == "bool" && !field.nullable && data.toggle.is_none() {
                data.toggle = Some(p);
            } else if !p.cell.is_empty() {
                data.list_badge |= p.cell.contains("<Badge");
                data.columns.push(p);
            }
        }
        data
    }
}
