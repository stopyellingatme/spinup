//! The code-first OpenAPI files and the zero-resource module lists.

#[test]
fn golden_api_openapi() -> anyhow::Result<()> {
    let Some(tree) = spinup_testutil::golden::run("api-openapi")? else {
        return Ok(());
    };
    assert_eq!(
        crate::golden_deploy::read(&tree, "api/src/handlers/mod.rs")?,
        "//! The resource handlers. spinup owns this file.\n"
    );
    assert_eq!(
        crate::golden_deploy::read(&tree, "api/src/models/mod.rs")?,
        "//! The resource models. spinup owns this file.\n"
    );
    assert_eq!(
        crate::golden_deploy::read(&tree, "api/src/routes_gen.rs")?,
        "//! The routes of generated resources. spinup owns this file.\n\nuse kit::db::PgPool;\nuse utoipa_axum::router::OpenApiRouter;\n\n/// Merges the routes of each resource in name order.\npub fn routes() -> OpenApiRouter<PgPool> {\n    OpenApiRouter::new()\n}\n"
    );
    assert!(tree.contains("api/openapi/bundle.gen.yaml"));
    assert!(!tree.contains("api/openapi/openapi.yaml"));
    Ok(())
}
