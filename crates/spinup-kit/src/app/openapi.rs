//! The `openapi` command writer and the spec helpers that bring the utoipa output closer to
//! the Go bundle.

use std::io::Write;

use utoipa::openapi::path::{Operation, Parameter, ParameterIn, PathItem};
use utoipa::openapi::{OpenApi, RefOr};

fn operations(item: &mut PathItem) -> Vec<&mut Operation> {
    [
        &mut item.get,
        &mut item.put,
        &mut item.post,
        &mut item.delete,
        &mut item.options,
        &mut item.head,
        &mut item.patch,
        &mut item.trace,
    ]
    .into_iter()
    .flatten()
    .collect()
}

fn is_path_param(p: &RefOr<Parameter>) -> bool {
    matches!(p, RefOr::T(p) if p.parameter_in == ParameterIn::Path)
}

/// Moves a path parameter that every operation of a path declares to the path item, as the
/// Go spec files do (`parameters:` next to `get:`). utoipa declares path parameters for
/// each operation only. A second call changes nothing.
pub fn hoist_path_params(doc: &mut OpenApi) {
    for item in doc.paths.paths.values_mut() {
        let mut ops = operations(item);
        let Some(first) = ops.first() else { continue };
        let shared: Vec<RefOr<Parameter>> = first
            .parameters
            .iter()
            .flatten()
            .filter(|p| is_path_param(p))
            .filter(|p| {
                ops.iter()
                    .all(|op| op.parameters.iter().flatten().any(|q| q == *p))
            })
            .cloned()
            .collect();
        if shared.is_empty() {
            continue;
        }
        for op in &mut ops {
            if let Some(params) = &mut op.parameters {
                params.retain(|p| !shared.contains(p));
                if params.is_empty() {
                    op.parameters = None;
                }
            }
        }
        item.parameters.get_or_insert_with(Vec::new).extend(shared);
    }
}

/// Returns the YAML text of `doc` after [`hoist_path_params`].
pub fn to_yaml(mut doc: OpenApi) -> anyhow::Result<String> {
    hoist_path_params(&mut doc);
    doc.to_yaml().map_err(|e| {
        anyhow::anyhow!(
            "cannot write the OpenAPI document as YAML: {e}\nTry: check the utoipa attributes of the handlers and schemas"
        )
    })
}

/// Writes the YAML text of `doc` to `out` (the `openapi` command).
pub fn write_yaml(doc: OpenApi, out: &mut dyn Write) -> anyhow::Result<()> {
    let text = to_yaml(doc)?;
    out.write_all(text.as_bytes())
        .and_then(|()| out.flush())
        .map_err(|e| {
            anyhow::anyhow!("cannot write the OpenAPI document: {e}\nTry: check the output pipe")
        })
}
