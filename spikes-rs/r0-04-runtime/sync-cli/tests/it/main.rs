#![cfg(unix)]
include!("../../../common/runner_contract.rs");

#[test]
fn lock_refresh_and_normal_completion() {
    cli_contract(env!("CARGO_BIN_EXE_sync-cli"), false);
}
#[test]
fn ctrl_c_kills_group_and_stops_refresh() {
    cli_contract(env!("CARGO_BIN_EXE_sync-cli"), true);
}

fn run(cmd: Cmd, after: Option<Duration>) -> Output {
    use sync_cli::{Cancellation, OSRunner, Runner};
    let cancel = Cancellation::default();
    let timer = after.map(|delay| {
        let token = cancel.clone();
        thread::spawn(move || {
            thread::sleep(delay);
            token.cancel();
        })
    });
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let result = OSRunner
        .run(&cmd, &cancel, &mut stdout, &mut stderr)
        .unwrap();
    assert_eq!(stdout, result.stdout);
    assert_eq!(stderr, result.stderr);
    if let Some(timer) = timer {
        timer.join().unwrap();
    }
    result
}
#[test]
fn cancel_kills_child_and_grandchild_including_inherited_pipes() {
    cancellation_contract(run);
}
#[test]
fn stdout_stderr_and_exit_code() {
    output_contract(run);
}
#[test]
fn tls_client_builds_offline() {
    let _ = sync_cli::client();
}

#[test]
fn output_streams_before_child_exits() {
    streaming_contract(env!("CARGO_BIN_EXE_sync-cli"));
}
#[test]
fn refresh_stops_when_spawn_fails() {
    failed_spawn_contract(env!("CARGO_BIN_EXE_sync-cli"));
}
