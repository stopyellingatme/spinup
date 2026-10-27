//! The base pack records the repository and registry owner.

#[test]
fn golden_base() -> anyhow::Result<()> {
    let Some(tree) = spinup_testutil::golden::run("base")? else {
        return Ok(());
    };
    let config: toml::Value =
        toml::from_str(std::str::from_utf8(&tree.get("spinup.toml").unwrap().data)?)?;
    assert_eq!(config["app"]["name"].as_str(), Some("todo"));
    assert_eq!(config["app"]["module"].as_str(), Some("example.com/todo"));
    assert_eq!(config["app"]["registry_owner"].as_str(), Some(""));
    assert_eq!(config["behavior_version"].as_integer(), Some(1));
    assert_eq!(config["monitoring"]["profile"].as_str(), Some("lite"));
    assert_eq!(
        config["modules"].as_array().unwrap(),
        &[toml::Value::from("ci"), toml::Value::from("deploy")]
    );
    Ok(())
}

#[test]
fn golden_base_registry_owner() -> anyhow::Result<()> {
    if !spinup_testutil::golden::selected("base") {
        return Ok(());
    }
    for (module, owner) in [
        ("github.com/Example-Owner/MyRepo", "example-owner"),
        ("example.com/Owner/Repo", ""),
        ("example.com/github.com/Owner", ""),
    ] {
        let tree = crate::golden_deploy::render(
            &["base", "api"],
            serde_json::json!({"app_name": "todo", "module": module}),
        )?;
        let config: toml::Value =
            toml::from_str(&crate::golden_deploy::read(&tree, "spinup.toml")?)?;
        assert_eq!(config["app"]["registry_owner"].as_str(), Some(owner));
        assert_eq!(config["app"]["module"].as_str(), Some(module));
        let cargo: toml::Value =
            toml::from_str(&crate::golden_deploy::read(&tree, "api/Cargo.toml")?)?;
        assert_eq!(cargo["package"]["name"].as_str(), Some("api"));
        let ignore = crate::golden_deploy::read(&tree, ".gitignore")?;
        assert!(ignore.contains("api/target/"));
        assert!(!ignore.contains("api/bin/") && !ignore.contains("api/tmp/"));
        assert!(crate::golden_deploy::read(&tree, "README.md")?.contains("a Rust API"));
        let renovate: serde_json::Value =
            serde_json::from_str(&crate::golden_deploy::read(&tree, "renovate.json")?)?;
        let rules = renovate["packageRules"].as_array().unwrap();
        assert!(
            rules
                .iter()
                .any(|rule| rule["matchManagers"] == serde_json::json!(["cargo"]))
        );
        assert!(rules.iter().any(|rule| rule["matchDepNames"]
            == serde_json::json!(["spinup-kit"])
            && rule["enabled"] == false));
    }
    let embedded = spinup_packs::embed::embedded()?;
    let base = spinup_packs::loader::load(&embedded, "base")?;
    let questions = base
        .questions
        .iter()
        .filter(|question| question.key == "module")
        .collect::<Vec<_>>();
    assert_eq!(questions.len(), 1);
    assert_eq!(
        questions[0].prompt,
        "Repository path (github.com/<owner>/<repo>)"
    );
    assert_eq!(
        questions[0].pattern,
        "^[a-z0-9][a-z0-9.-]*(/[A-Za-z0-9._~-]+)+$"
    );
    Ok(())
}
