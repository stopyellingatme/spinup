//! The registry ID test (R0.3, ADR 0020 §5). It runs the real binary and compares the
//! registered IDs with `tests/expected_registry.txt`.

#[test]
#[allow(
    clippy::disallowed_methods,
    clippy::disallowed_types,
    reason = "the test runs the built binary itself, not a project command"
)]
fn registered_ids_match_expected() {
    let output = std::process::Command::new(env!("CARGO_BIN_EXE_spinup"))
        .env("SPINUP_PRINT_REGISTRY", "1")
        .output()
        .expect("run the spinup binary");
    assert!(output.status.success(), "the binary failed: {output:?}");
    let got = String::from_utf8(output.stdout).expect("UTF-8 registry IDs");
    let want: String = include_str!("../expected_registry.txt")
        .lines()
        .filter(|line| !line.starts_with('#'))
        .map(|line| format!("{line}\n"))
        .collect();
    assert_eq!(
        got, want,
        "the registered IDs differ from tests/expected_registry.txt\nTry: add the missing `use <crate> as _;` line to src/wire_gen.rs, or update the expected list"
    );
}
