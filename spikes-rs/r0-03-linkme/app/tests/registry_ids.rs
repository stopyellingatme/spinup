#[test]
fn registered_ids_match_expected() {
    let output = std::process::Command::new(env!("CARGO_BIN_EXE_app"))
        .output()
        .expect("run the registry binary");
    assert!(output.status.success());
    assert_eq!(
        String::from_utf8(output.stdout).expect("UTF-8 registry IDs"),
        include_str!("expected_registry.txt"),
        "registry IDs differ from expected_registry.txt"
    );
}
