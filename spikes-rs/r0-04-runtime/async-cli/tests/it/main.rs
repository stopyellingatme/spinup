#![cfg(unix)]
include!("../../../common/runner_contract.rs");

fn run(cmd: Cmd, after: Option<Duration>) -> Output {
    use async_cli::{OSRunner, Runner};
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .unwrap()
        .block_on(async {
            let cancel = tokio_util::sync::CancellationToken::new();
            let timer = after.map(|delay| {
                let token = cancel.clone();
                tokio::spawn(async move {
                    tokio::time::sleep(delay).await;
                    token.cancel();
                })
            });
            let mut stdout = Vec::new();
            let mut stderr = Vec::new();
            let result = OSRunner
                .run(&cmd, &cancel, &mut stdout, &mut stderr)
                .await
                .unwrap();
            assert_eq!(stdout, result.stdout);
            assert_eq!(stderr, result.stderr);
            if let Some(timer) = timer {
                timer.await.unwrap();
            }
            result
        })
}
#[test]
fn lock_refresh_and_normal_completion() {
    cli_contract(env!("CARGO_BIN_EXE_async-cli"), false);
}
#[test]
fn ctrl_c_kills_group_and_stops_refresh() {
    cli_contract(env!("CARGO_BIN_EXE_async-cli"), true);
}
#[test]
fn cancel_kills_child_and_grandchild_including_inherited_pipes() {
    cancellation_contract(run);
}
#[test]
fn stdout_stderr_and_exit_code() {
    output_contract(run);
}
#[tokio::test]
async fn tls_client_builds_offline() {
    let _ = async_cli::client().unwrap();
}

#[test]
fn output_streams_before_child_exits() {
    streaming_contract(env!("CARGO_BIN_EXE_async-cli"));
}
#[test]
fn refresh_stops_when_spawn_fails() {
    failed_spawn_contract(env!("CARGO_BIN_EXE_async-cli"));
}
