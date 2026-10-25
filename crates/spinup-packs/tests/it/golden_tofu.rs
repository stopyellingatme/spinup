//! OpenTofu output must match Go and remain optional.

#[test]
fn golden_tofu() -> anyhow::Result<()> {
    spinup_testutil::golden::run("tofu")?;
    Ok(())
}

#[test]
fn golden_tofu_opt_in() -> anyhow::Result<()> {
    if !spinup_testutil::golden::selected("tofu") {
        return Ok(());
    }
    let pack = spinup_packs::loader::load(&spinup_packs::embed::embedded()?, "tofu")?;
    assert_eq!(pack.requires, ["deploy"]);
    for file in pack.files {
        let class = if file.path.ends_with("terraform.tfvars.example") {
            "seed"
        } else {
            "managed"
        };
        assert_eq!(file.class, class, "{}", file.path);
    }
    let tree = spinup_testutil::golden::render_pack(
        &["base".into(), "api".into(), "deploy".into()],
        serde_json::json!({"app_name": "todo", "module": "example.com/todo", "modules": "deploy"})
            .as_object()
            .unwrap(),
    )?;
    assert!(
        tree.iter()
            .all(|(path, _)| !path.starts_with("deploy/infra/tofu"))
    );
    Ok(())
}
