use std::process::Command;

#[test]
fn version_uses_release_binary_name() {
    let output = Command::new(env!("CARGO_BIN_EXE_spinup"))
        .arg("--version")
        .output()
        .unwrap();
    assert!(output.status.success());
    let version = std::str::from_utf8(&output.stdout).unwrap();
    assert!(version.starts_with("spinup "), "{version}");
    assert!(!version.contains("cli-skel"));
}

#[test]
fn linked_tls_and_compression_work_offline() {
    let output = Command::new(env!("CARGO_BIN_EXE_spinup"))
        .arg("--self-check")
        .output()
        .unwrap();
    assert!(output.status.success(), "{output:?}");
    assert_eq!(
        std::str::from_utf8(&output.stdout).unwrap(),
        "TLS=rustls provider=ring HTTP=ureq compression=zstd self-check=ok\n"
    );
}
