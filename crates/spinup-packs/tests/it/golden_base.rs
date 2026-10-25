//! The base pack must preserve the Go project configuration.

#[test]
fn golden_base() -> anyhow::Result<()> {
    let Some(tree) = spinup_testutil::golden::run("base")? else {
        return Ok(());
    };
    let config: toml::Value =
        toml::from_str(std::str::from_utf8(&tree.get("spinup.toml").unwrap().data)?)?;
    assert_eq!(config["app"]["name"].as_str(), Some("todo"));
    assert_eq!(config["app"]["module"].as_str(), Some("example.com/todo"));
    assert_eq!(config["behavior_version"].as_integer(), Some(1));
    assert_eq!(config["monitoring"]["profile"].as_str(), Some("lite"));
    assert_eq!(
        config["modules"].as_array().unwrap(),
        &[toml::Value::from("ci"), toml::Value::from("deploy")]
    );
    Ok(())
}
