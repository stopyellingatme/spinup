use runtime_common::{Cmd, Output, Redactor, Step};
use std::io;
use std::process::Stdio;
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;

pub trait Runner {
    fn run<'a>(
        &'a self,
        cmd: &'a Cmd,
        cancel: &'a CancellationToken,
        stdout: &'a mut (dyn AsyncWrite + Unpin + Send),
        stderr: &'a mut (dyn AsyncWrite + Unpin + Send),
    ) -> impl Future<Output = io::Result<Output>> + Send + 'a;
}
pub struct OSRunner;

async fn capture(
    mut pipe: impl AsyncRead + Unpin,
    sink: &mut (dyn AsyncWrite + Unpin + Send),
    secrets: &[String],
) -> io::Result<Vec<u8>> {
    let mut redactor = Redactor::new(secrets);
    let mut output = Vec::new();
    let mut buffer = [0; 4096];
    loop {
        let n = pipe.read(&mut buffer).await?;
        let bytes = redactor.push(&buffer[..n], n == 0);
        sink.write_all(&bytes).await?;
        sink.flush().await?;
        output.extend(bytes);
        if n == 0 {
            return Ok(output);
        }
    }
}

impl Runner for OSRunner {
    async fn run(
        &self,
        cmd: &Cmd,
        cancel: &CancellationToken,
        stdout: &mut (dyn AsyncWrite + Unpin + Send),
        stderr: &mut (dyn AsyncWrite + Unpin + Send),
    ) -> io::Result<Output> {
        if cancel.is_cancelled() {
            return Err(io::ErrorKind::Interrupted.into());
        }
        let mut command = Command::new(&cmd.program);
        command
            .args(&cmd.args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        #[cfg(unix)]
        command.process_group(0);
        let mut child = command.spawn()?;
        #[cfg(unix)]
        let mut group = runtime_common::ProcessGroup::new(child.id().unwrap());
        let out = child.stdout.take().unwrap();
        let err = child.stderr.take().unwrap();
        let pipes = async {
            tokio::try_join!(
                capture(out, stdout, &cmd.secrets),
                capture(err, stderr, &cmd.secrets)
            )
        };
        tokio::pin!(pipes);
        let mut status = None;
        let mut output = None;
        let mut cancelled = false;
        let drain = tokio::time::sleep(Duration::from_secs(2));
        tokio::pin!(drain);
        while status.is_none() || output.is_none() {
            tokio::select! {
                _ = cancel.cancelled(), if !cancelled => {
                    #[cfg(unix)] runtime_common::kill_group(group.pid())?;
                    #[cfg(windows)] if status.is_none() { child.start_kill()?; }
                    cancelled = true;
                }
                result = child.wait(), if status.is_none() => {
                    status = Some(result?);
                    drain.as_mut().reset(tokio::time::Instant::now() + Duration::from_secs(2));
                }
                result = &mut pipes, if output.is_none() => { output = Some(result?); }
                _ = &mut drain, if status.is_some() => {
                    return Err(io::Error::new(io::ErrorKind::TimedOut, "inherited pipes stayed open"));
                }
            }
        }
        #[cfg(unix)]
        group.disarm();
        let (stdout, stderr) = output.unwrap();
        Ok(Output {
            stdout,
            stderr,
            status: status.unwrap(),
            cancelled,
        })
    }
}

pub async fn run_step(step: &Step, cancel: &CancellationToken) -> anyhow::Result<Output> {
    tokio::fs::write(&step.lock, b"0").await?;
    let stop = CancellationToken::new();
    // Keep this future in scope, so a dropped step also drops the refresh task.
    let refresh = async {
        let mut tick = 0_u64;
        loop {
            tokio::select! {
                biased;
                _ = stop.cancelled() => return Ok::<(), io::Error>(()),
                _ = tokio::time::sleep(step.refresh) => {
                    tick += 1;
                    tokio::fs::write(&step.lock, tick.to_string()).await?;
                }
            }
        }
    };
    let run = async {
        let result = OSRunner
            .run(
                &step.cmd,
                cancel,
                &mut tokio::io::stdout(),
                &mut tokio::io::stderr(),
            )
            .await;
        stop.cancel();
        result
    };
    let (result, refreshed) = tokio::join!(run, refresh);
    refreshed?;
    Ok(result?)
}

pub fn client() -> anyhow::Result<reqwest::Client> {
    Ok(reqwest::Client::builder()
        .https_only(true)
        .timeout(Duration::from_secs(10))
        .build()?)
}
pub async fn https_get(url: &str) -> anyhow::Result<String> {
    Ok(client()?
        .get(url)
        .send()
        .await?
        .error_for_status()?
        .text()
        .await?)
}
