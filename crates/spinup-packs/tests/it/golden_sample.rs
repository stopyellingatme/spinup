//! The sample pack must match the Go golden tree.

#[test]
fn golden_sample() -> anyhow::Result<()> {
    spinup_testutil::golden::run("_sample")?;
    Ok(())
}
