use tokio_util::sync::CancellationToken;
#[tokio::main(flavor = "multi_thread", worker_threads = 2)]
async fn main() -> anyhow::Result<()> {
    let args: Vec<_> = std::env::args().skip(1).collect();
    if args.first().is_some_and(|s| s == "--get") {
        println!(
            "{}",
            async_cli::https_get(args.get(1).ok_or_else(|| anyhow::anyhow!("missing URL"))?)
                .await?
        );
        return Ok(());
    }
    let step = runtime_common::parse_step(args)?;
    let cancel = CancellationToken::new();
    let run = async_cli::run_step(&step, &cancel);
    tokio::pin!(run);
    let output = tokio::select! {
        result = &mut run => result?,
        signal = tokio::signal::ctrl_c() => {
            signal?;
            cancel.cancel();
            run.await?
        }
    };
    anyhow::ensure!(!output.cancelled, "command cancelled");
    anyhow::ensure!(output.status.success(), "command failed: {}", output.status);
    Ok(())
}
