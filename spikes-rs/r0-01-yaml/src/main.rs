fn main() -> anyhow::Result<()> {
    println!("{}", serde_json::to_string_pretty(&r0_01_yaml::measure()?)?);
    Ok(())
}
