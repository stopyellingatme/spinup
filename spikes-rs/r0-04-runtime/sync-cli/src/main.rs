use sync_cli::{Cancellation, run_step};
fn main() -> anyhow::Result<()> {
    let args: Vec<_> = std::env::args().skip(1).collect();
    if args.first().is_some_and(|s| s == "--get") {
        println!(
            "{}",
            sync_cli::https_get(args.get(1).ok_or_else(|| anyhow::anyhow!("missing URL"))?)?
        );
        return Ok(());
    }
    let step = runtime_common::parse_step(args)?;
    let cancel = Cancellation::default();
    #[cfg(unix)]
    let (signal_handle, signal_thread) = {
        let mut signals = signal_hook::iterator::Signals::new([
            signal_hook::consts::SIGINT,
            signal_hook::consts::SIGTERM,
        ])?;
        let handle = signals.handle();
        let token = cancel.clone();
        let worker = std::thread::spawn(move || {
            if signals.forever().next().is_some() {
                token.cancel();
            }
        });
        (handle, worker)
    };
    let result = run_step(&step, &cancel);
    #[cfg(unix)]
    {
        signal_handle.close();
        signal_thread
            .join()
            .map_err(|_| anyhow::anyhow!("signal thread panicked"))?;
    }
    let output = result?;
    anyhow::ensure!(!output.cancelled, "command cancelled");
    anyhow::ensure!(output.status.success(), "command failed: {}", output.status);
    Ok(())
}
