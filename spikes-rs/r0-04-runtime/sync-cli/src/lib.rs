use runtime_common::{Cmd, Output, Redactor, Step};
use std::io::{self, Read, Write};
use std::process::{Command, Stdio};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc,
};
use std::thread;
use std::time::{Duration, Instant};

#[derive(Clone, Default)]
pub struct Cancellation(Arc<AtomicBool>);
impl Cancellation {
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Release);
    }
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Acquire)
    }
}

pub trait Runner {
    fn run(
        &self,
        cmd: &Cmd,
        cancel: &Cancellation,
        stdout: &mut (dyn Write + Send),
        stderr: &mut (dyn Write + Send),
    ) -> io::Result<Output>;
}
pub struct OSRunner;

fn capture(
    mut pipe: impl Read,
    sink: &mut dyn Write,
    secrets: &[String],
    stop: &AtomicBool,
) -> io::Result<Vec<u8>> {
    let mut redactor = Redactor::new(secrets);
    let mut output = Vec::new();
    let mut buffer = [0; 4096];
    while !stop.load(Ordering::Acquire) {
        match pipe.read(&mut buffer) {
            Ok(0) => break,
            Ok(n) => {
                let bytes = redactor.push(&buffer[..n], false);
                sink.write_all(&bytes)?;
                sink.flush()?;
                output.extend(bytes);
            }
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                thread::sleep(Duration::from_millis(5))
            }
            Err(e) => return Err(e),
        }
    }
    let bytes = redactor.push(&[], true);
    sink.write_all(&bytes)?;
    sink.flush()?;
    output.extend(bytes);
    Ok(output)
}

impl Runner for OSRunner {
    fn run(
        &self,
        cmd: &Cmd,
        cancel: &Cancellation,
        stdout: &mut (dyn Write + Send),
        stderr: &mut (dyn Write + Send),
    ) -> io::Result<Output> {
        if cancel.is_cancelled() {
            return Err(io::ErrorKind::Interrupted.into());
        }
        let mut command = Command::new(&cmd.program);
        command
            .args(&cmd.args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            command.process_group(0);
        }
        let mut child = command.spawn()?;
        let out = child.stdout.take().unwrap();
        let err = child.stderr.take().unwrap();
        #[cfg(unix)]
        {
            use std::os::fd::AsRawFd;
            if let Err(e) = runtime_common::nonblocking(out.as_raw_fd())
                .and_then(|()| runtime_common::nonblocking(err.as_raw_fd()))
            {
                let _ = runtime_common::kill_group(child.id());
                let _ = child.wait();
                return Err(e);
            }
        }
        let stop = AtomicBool::new(false);
        thread::scope(|scope| {
            let out = scope.spawn(|| capture(out, stdout, &cmd.secrets, &stop));
            let err = scope.spawn(|| capture(err, stderr, &cmd.secrets, &stop));
            let mut cancelled = false;
            let mut exited = None;
            let mut status = None;
            let wait = (|| {
                loop {
                    if cancel.is_cancelled() && !cancelled {
                        #[cfg(unix)]
                        runtime_common::kill_group(child.id())?;
                        #[cfg(windows)]
                        if status.is_none() {
                            child.kill()?;
                        }
                        cancelled = true;
                    }
                    if status.is_none() {
                        status = child.try_wait()?;
                        if status.is_some() {
                            exited = Some(Instant::now());
                        }
                    }
                    if let Some(status) = status
                        && out.is_finished()
                        && err.is_finished()
                    {
                        return Ok(status);
                    }
                    if exited.is_some_and(|t| t.elapsed() > Duration::from_secs(2)) {
                        return Err(io::Error::new(
                            io::ErrorKind::TimedOut,
                            "inherited pipes stayed open",
                        ));
                    }
                    thread::sleep(Duration::from_millis(5));
                }
            })();
            if wait.is_err() {
                stop.store(true, Ordering::Release);
                #[cfg(unix)]
                {
                    let _ = runtime_common::kill_group(child.id());
                }
                #[cfg(windows)]
                {
                    let _ = child.kill();
                }
                let _ = child.wait();
            }
            let stdout = out
                .join()
                .map_err(|_| io::Error::other("stdout thread panicked"))?;
            let stderr = err
                .join()
                .map_err(|_| io::Error::other("stderr thread panicked"))?;
            Ok(Output {
                stdout: stdout?,
                stderr: stderr?,
                status: wait?,
                cancelled,
            })
        })
    }
}

pub fn run_step(step: &Step, cancel: &Cancellation) -> anyhow::Result<Output> {
    std::fs::write(&step.lock, b"0")?;
    let (stop, receive) = mpsc::channel();
    let path = step.lock.clone();
    let period = step.refresh;
    let refresh = thread::spawn(move || -> io::Result<()> {
        let mut tick = 0_u64;
        while receive.recv_timeout(period) == Err(mpsc::RecvTimeoutError::Timeout) {
            tick += 1;
            std::fs::write(&path, tick.to_string())?;
        }
        Ok(())
    });
    let result = OSRunner.run(&step.cmd, cancel, &mut io::stdout(), &mut io::stderr());
    let _ = stop.send(());
    refresh
        .join()
        .map_err(|_| io::Error::other("refresh thread panicked"))??;
    Ok(result?)
}

pub fn client() -> ureq::Agent {
    ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(10)))
        .tls_config(
            ureq::tls::TlsConfig::builder()
                .unversioned_rustls_crypto_provider(Arc::new(
                    rustls::crypto::ring::default_provider(),
                ))
                .build(),
        )
        .build()
        .into()
}
pub fn https_get(url: &str) -> anyhow::Result<String> {
    anyhow::ensure!(url.starts_with("https://"), "expected HTTPS URL");
    Ok(client().get(url).call()?.body_mut().read_to_string()?)
}
