//! The secrets pack must match the Go golden selection.

#[test]
fn golden_secrets() -> anyhow::Result<()> {
    spinup_testutil::golden::run("secrets")?;
    Ok(())
}
