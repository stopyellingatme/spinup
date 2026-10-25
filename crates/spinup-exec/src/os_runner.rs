//! The synchronous operating-system runner with bounded pipe draining.

use crate::lookup;
use crate::process::{self, Input, Pipe};
use crate::redact::{Redactor, redact_string};
use crate::runner::{
    Cancellation, Cmd, ExecError, ExitError, Output, PROCESS_WAIT_DELAY, Runner, Sinks, Stdin,
    StopReason,
};
use std::io::{self, Write};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

const POLL_INTERVAL: Duration = Duration::from_millis(5);

/// Runs commands through the operating system.
#[derive(Debug, Clone, Copy, Default)]
pub struct OsRunner;

impl Runner for OsRunner {
    fn run(
        &self,
        cmd: &Cmd,
        cancel: &Cancellation,
        mut sinks: Sinks<'_>,
    ) -> Result<Output, ExecError> {
        let program = lookup::resolve(cmd)?;
        if cancel.is_cancelled() {
            return Err(stopped(cmd, StopReason::Cancelled, Output::default()));
        }
        let mut command = Command::new(program);
        command
            .args(&cmd.args)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        command.stdin(match cmd.stdin {
            Stdin::Null => Stdio::null(),
            Stdin::Bytes(_) => Stdio::piped(),
            Stdin::Inherit => Stdio::inherit(),
        });
        if let Some(dir) = &cmd.dir {
            command.current_dir(dir);
        }
        if cmd.replace_env {
            command.env_clear();
            // Go preserves SYSTEMROOT for Windows process startup.
            #[cfg(windows)]
            if let Some(root) = std::env::var_os("SYSTEMROOT") {
                command.env("SYSTEMROOT", root);
            }
        }
        command.envs(cmd.env.iter().map(|(key, value)| (key, value)));
        process::configure(&mut command, cmd.interactive);
        let start = Instant::now();
        let mut child = command.spawn().map_err(|source| io_error(cmd, source))?;
        let setup = (|| {
            let stdout = Capture::new(child.stdout.take().expect("stdout was piped"), &cmd.redact)?;
            let stderr = Capture::new(child.stderr.take().expect("stderr was piped"), &cmd.redact)?;
            let bytes = match &cmd.stdin {
                Stdin::Bytes(bytes) => bytes.clone(),
                _ => Vec::new(),
            };
            let input = Input::new(child.stdin.take(), bytes)?;
            Ok::<_, io::Error>((stdout, stderr, input))
        })();
        let (mut stdout, mut stderr, mut input) = match setup {
            Ok(pipes) => pipes,
            Err(source) => {
                let _ = process::kill(&mut child, cmd.interactive);
                let _ = child.wait();
                return Err(io_error(cmd, source));
            }
        };
        let mut reason = None;
        let mut status = None;
        let mut drain_start = None;
        let mut expired = false;
        let wait = (|| {
            loop {
                let requested_stop = stop_reason(cmd, cancel, start);
                if reason.is_none()
                    && let Some(stop) = requested_stop
                {
                    reason = Some(stop);
                    drain_start.get_or_insert_with(Instant::now);
                    process::kill(&mut child, cmd.interactive)?;
                }
                if status.is_none() {
                    status = child.try_wait()?;
                    if status.is_some() {
                        drain_start.get_or_insert_with(Instant::now);
                    }
                }
                let mut progressed = stdout.pump(&mut sinks.stdout)?;
                progressed |= stderr.pump(&mut sinks.stderr)?;
                progressed |= input.pump()?;
                if let Some(status) = status {
                    if stdout.done() && stderr.done() && input.is_done() {
                        return Ok(status);
                    }
                    if drain_start
                        .is_some_and(|start: Instant| start.elapsed() >= PROCESS_WAIT_DELAY)
                    {
                        expired = true;
                        return Ok(status);
                    }
                }
                if !progressed {
                    std::thread::sleep(POLL_INTERVAL);
                }
            }
        })();
        if wait.is_err() {
            let _ = process::kill(&mut child, cmd.interactive);
            let _ = child.wait();
        }
        drop(input);
        let stdout = stdout.finish(&mut sinks.stdout);
        let stderr = stderr.finish(&mut sinks.stderr);
        let status = wait.map_err(|source| io_error(cmd, source))?;
        let output = Output {
            stdout,
            stderr,
            exit_code: status.code().unwrap_or(-1),
            duration: start.elapsed(),
        };
        // A sink can cancel while the final buffered suffix is flushed.
        if let Some(reason) = reason.or_else(|| stop_reason(cmd, cancel, start)) {
            return Err(stopped(cmd, reason, output));
        }
        // Go gives a nonzero exit code precedence over the drain timeout.
        if expired && output.exit_code == 0 {
            return Err(io_error(
                cmd,
                io::Error::new(
                    io::ErrorKind::TimedOut,
                    "exec: WaitDelay expired before I/O complete",
                ),
            ));
        }
        exit_result(cmd, output)
    }
}

struct Capture<P> {
    pipe: Option<P>,
    redactor: Redactor,
    bytes: Vec<u8>,
}

impl<P: Pipe> Capture<P> {
    fn new(pipe: P, secrets: &[String]) -> io::Result<Self> {
        process::prepare_pipe(&pipe)?;
        Ok(Self {
            pipe: Some(pipe),
            redactor: Redactor::new(secrets),
            bytes: Vec::new(),
        })
    }

    fn pump(&mut self, sink: &mut Option<&mut (dyn Write + Send)>) -> io::Result<bool> {
        let Some(pipe) = &mut self.pipe else {
            return Ok(false);
        };
        let mut buffer = [0; 8192];
        match process::read_pipe(pipe, &mut buffer) {
            Ok(count) => {
                if count == 0 {
                    self.pipe = None;
                }
                let bytes = self.redactor.push(&buffer[..count], count == 0);
                self.emit(&bytes, sink);
                Ok(true)
            }
            Err(error)
                if matches!(
                    error.kind(),
                    io::ErrorKind::WouldBlock | io::ErrorKind::Interrupted
                ) =>
            {
                Ok(false)
            }
            Err(error) => Err(error),
        }
    }

    fn emit(&mut self, bytes: &[u8], sink: &mut Option<&mut (dyn Write + Send)>) {
        self.bytes.extend_from_slice(bytes);
        if !bytes.is_empty()
            && let Some(sink) = sink
        {
            // Go ignores live sink errors and keeps capturing the command output.
            let _ = sink.write_all(bytes);
            let _ = sink.flush();
        }
    }

    fn done(&self) -> bool {
        self.pipe.is_none()
    }

    fn finish(mut self, sink: &mut Option<&mut (dyn Write + Send)>) -> Vec<u8> {
        let tail = self.redactor.push(&[], true);
        self.emit(&tail, sink);
        self.bytes
    }
}

pub(crate) fn io_error(cmd: &Cmd, source: io::Error) -> ExecError {
    let message = source.to_string();
    let redacted = redact_string(&message, &cmd.redact);
    let source = if message == redacted {
        source
    } else {
        io::Error::new(source.kind(), redacted)
    };
    ExecError::Io {
        cmd: cmd.to_string(),
        source,
    }
}

fn stop_reason(cmd: &Cmd, cancel: &Cancellation, start: Instant) -> Option<StopReason> {
    if cancel.is_cancelled() {
        Some(StopReason::Cancelled)
    } else if cmd
        .timeout
        .is_some_and(|timeout| start.elapsed() >= timeout)
    {
        Some(StopReason::TimedOut)
    } else {
        None
    }
}

fn stopped(cmd: &Cmd, reason: StopReason, output: Output) -> ExecError {
    ExecError::Stopped {
        cmd: cmd.to_string(),
        reason,
        output: Box::new(output),
    }
}

pub(crate) fn exit_result(cmd: &Cmd, output: Output) -> Result<Output, ExecError> {
    if output.exit_code == 0 {
        return Ok(output);
    }
    let stderr = String::from_utf8_lossy(&output.stderr);
    let lines: Vec<_> = stderr
        .strip_suffix('\n')
        .unwrap_or(&stderr)
        .split('\n')
        .collect();
    let stderr_tail = lines[lines.len().saturating_sub(20)..].join("\n");
    Err(ExitError {
        cmd: cmd.to_string(),
        code: output.exit_code,
        stderr_tail,
        output,
    }
    .into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runner::*;
    use std::io::{Read, Write};
    use std::sync::mpsc;

    fn helper(mode: &str) -> Cmd {
        Cmd::new(std::env::current_exe().unwrap().to_str().unwrap())
            .args(["--exact", "os_runner::tests::child_process", "--nocapture"])
            .env("SPINUP_EXEC_CHILD_MODE", mode)
    }

    #[test]
    fn child_process() {
        let Ok(mode) = std::env::var("SPINUP_EXEC_CHILD_MODE") else {
            return;
        };
        match mode.as_str() {
            "echo" => {
                let mut input = Vec::new();
                std::io::stdin().read_to_end(&mut input).unwrap();
                std::io::stdout().write_all(&input).unwrap();
            }
            "duplex" => {
                let mut buffer = [0; 16384];
                loop {
                    let count = std::io::stdin().read(&mut buffer).unwrap();
                    if count == 0 {
                        break;
                    }
                    std::io::stdout().write_all(&buffer[..count]).unwrap();
                    std::io::stderr().write_all(&buffer[..count]).unwrap();
                }
            }
            "env-parent" => {
                for replace in [false, true] {
                    let mut cmd = helper("env");
                    cmd.replace_env = replace;
                    let output = OsRunner.output(&cmd, &Cancellation::new()).unwrap();
                    let text = String::from_utf8(output.stdout).unwrap();
                    assert_eq!(
                        text.contains("SPINUP_EXEC_VALUE_PARENT=parent-only"),
                        !replace
                    );
                }
            }
            "env" => {
                for (key, value) in
                    std::env::vars().filter(|(key, _)| key.starts_with("SPINUP_EXEC_VALUE"))
                {
                    println!("{key}={value}");
                }
                println!("dir={}", std::env::current_dir().unwrap().display());
            }
            "exit" => {
                print!("secret!");
                for line in 1..=21 {
                    eprintln!("line{line} secret");
                }
                std::io::stdout().flush().unwrap();
                std::process::exit(7);
            }
            "stream" => {
                print!("sec");
                std::io::stdout().flush().unwrap();
                std::thread::sleep(Duration::from_millis(30));
                print!("ret!");
                std::io::stdout().flush().unwrap();
                eprint!("sec");
                std::io::stderr().flush().unwrap();
                std::thread::sleep(Duration::from_millis(30));
                eprint!("ret?");
                std::io::stderr().flush().unwrap();
                std::thread::sleep(Duration::from_secs(10));
            }
            "sleep" => std::thread::sleep(Duration::from_secs(10)),
            _ => panic!("unknown child mode"),
        }
        std::process::exit(0);
    }

    #[test]
    fn stdin_large_and_null_input() {
        for size in [0, 1024 * 1024] {
            let data = vec![b'x'; size];
            let mut cmd = helper("echo");
            if size > 0 {
                cmd.stdin = Stdin::Bytes(data.clone());
            }
            let result = OsRunner.output(&cmd, &Cancellation::new()).unwrap();
            // libtest prints a prelude before entering the child helper.
            assert!(result.stdout.ends_with(&data));
            assert!(result.duration > Duration::ZERO);
        }
    }

    #[test]
    fn large_input_and_both_output_pipes_do_not_deadlock() {
        let mut cmd = helper("duplex");
        let bytes = vec![b'x'; 1024 * 1024];
        cmd.stdin = Stdin::Bytes(bytes.clone());
        cmd.timeout = Some(Duration::from_secs(5));
        let output = OsRunner.output(&cmd, &Cancellation::new()).unwrap();
        assert!(output.stdout.ends_with(&bytes));
        assert_eq!(output.stderr, bytes);
    }

    #[test]
    fn cancellation_does_not_block_on_unread_stdin() {
        let mut cmd = helper("sleep");
        cmd.stdin = Stdin::Bytes(vec![b'x'; 1024 * 1024]);
        cmd.timeout = Some(Duration::from_millis(100));
        let start = Instant::now();
        assert!(matches!(
            OsRunner.output(&cmd, &Cancellation::new()),
            Err(ExecError::Stopped {
                reason: StopReason::TimedOut,
                ..
            })
        ));
        assert!(start.elapsed() < Duration::from_secs(4));
    }

    #[test]
    fn output_exit_error_and_twenty_line_tail_are_redacted() {
        let mut cmd = helper("exit");
        cmd.redact = vec!["secret".into()];
        let mut stream = Vec::new();
        let error = OsRunner
            .run(
                &cmd,
                &Cancellation::new(),
                Sinks {
                    stdout: Some(&mut stream),
                    stderr: None,
                },
            )
            .unwrap_err();
        let ExecError::Exit(exit) = error else {
            panic!("expected exit error")
        };
        assert_eq!(exit.code, 7);
        assert!(exit.output.stdout.ends_with(b"***!"));
        assert_eq!(stream, exit.output.stdout);
        assert_eq!(exit.stderr_tail.lines().count(), 20);
        assert!(exit.stderr_tail.starts_with("line2 ***\n"));
        assert!(exit.stderr_tail.ends_with("line21 ***"));
        assert!(!exit.to_string().contains("secret"));
    }

    #[test]
    fn timeout_cancel_and_precancel_are_typed() {
        let mut cmd = helper("sleep");
        cmd.timeout = Some(Duration::from_millis(100));
        let start = Instant::now();
        assert!(matches!(
            OsRunner.output(&cmd, &Cancellation::new()),
            Err(ExecError::Stopped {
                reason: StopReason::TimedOut,
                ..
            })
        ));
        assert!(start.elapsed() < Duration::from_secs(4));
        let cancel = Cancellation::new();
        cancel.cancel();
        assert!(matches!(
            OsRunner.output(&helper("sleep"), &cancel),
            Err(ExecError::Stopped {
                reason: StopReason::Cancelled,
                ..
            })
        ));
        std::thread::scope(|scope| {
            let cancel = Cancellation::new();
            let copy = cancel.clone();
            scope.spawn(move || {
                std::thread::sleep(Duration::from_millis(100));
                copy.cancel();
            });
            assert!(matches!(
                OsRunner.output(&helper("sleep"), &cancel),
                Err(ExecError::Stopped {
                    reason: StopReason::Cancelled,
                    ..
                })
            ));
        });
    }

    #[test]
    fn environment_replacement_directory_and_last_pair_win() {
        OsRunner
            .output(
                &helper("env-parent").env("SPINUP_EXEC_VALUE_PARENT", "parent-only"),
                &Cancellation::new(),
            )
            .unwrap();
        let dir = tempfile::tempdir().unwrap();
        for replace in [false, true] {
            let mut cmd = helper("env")
                .dir(dir.path())
                .env("SPINUP_EXEC_VALUE", "old")
                .env("SPINUP_EXEC_VALUE", "explicit");
            cmd.replace_env = replace;
            let output = OsRunner.output(&cmd, &Cancellation::new()).unwrap();
            let text = String::from_utf8(output.stdout).unwrap();
            assert!(text.contains("SPINUP_EXEC_VALUE=explicit\n"));
            assert!(!text.contains("SPINUP_EXEC_VALUE=old"));
            // Compare canonical forms: on Windows the child may print a short (8.3) or a
            // verbatim path for the same directory.
            let printed = text
                .lines()
                .find_map(|line| line.strip_prefix("dir="))
                .expect("dir line");
            assert_eq!(
                std::fs::canonicalize(printed).unwrap(),
                std::fs::canonicalize(dir.path()).unwrap()
            );
        }
    }

    #[test]
    fn invalid_directory_and_nul_errors_do_not_expose_secrets() {
        let dir = tempfile::tempdir().unwrap();
        let mut cmd = helper("echo").dir(dir.path().join("missing-secret"));
        cmd.redact = vec!["secret".into()];
        let error = OsRunner.output(&cmd, &Cancellation::new()).unwrap_err();
        assert!(matches!(error, ExecError::Io { .. }));
        assert!(!error.to_string().contains("secret"));
        let mut cmd = helper("echo").args(["secret\0"]);
        cmd.redact = vec!["secret".into()];
        let error = OsRunner.output(&cmd, &Cancellation::new()).unwrap_err();
        assert!(!error.to_string().contains("secret"));
    }

    #[test]
    fn cancellation_during_final_redaction_flush_is_reported() {
        struct CancellingSink(Cancellation);
        impl Write for CancellingSink {
            fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
                if bytes == b"sec" {
                    self.0.cancel();
                } else {
                    std::thread::sleep(Duration::from_millis(50));
                }
                Ok(bytes.len())
            }
            fn flush(&mut self) -> io::Result<()> {
                Ok(())
            }
        }
        let cancel = Cancellation::new();
        let mut cmd = helper("echo");
        cmd.stdin = Stdin::Bytes(b"sec".to_vec());
        cmd.redact = vec!["secret".into()];
        let error = OsRunner
            .run(
                &cmd,
                &cancel,
                Sinks {
                    stdout: Some(&mut CancellingSink(cancel.clone())),
                    stderr: None,
                },
            )
            .unwrap_err();
        assert!(matches!(
            error,
            ExecError::Stopped {
                reason: StopReason::Cancelled,
                ..
            }
        ));
        assert!(error.output().unwrap().stdout.ends_with(b"sec"));
    }

    struct StreamingSink(mpsc::Sender<Vec<u8>>);
    impl Write for StreamingSink {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            self.0.send(bytes.to_vec()).unwrap();
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn live_output_arrives_before_exit() {
        // The child exits only when cancellation follows both complete streamed tokens.
        let cancel = Cancellation::new();
        let (send, receive) = mpsc::channel();
        let copy = cancel.clone();
        let consumer = std::thread::spawn(move || {
            let mut text = Vec::new();
            while !(text.windows(4).any(|chunk| chunk == b"***?")
                && text.windows(4).any(|chunk| chunk == b"***!"))
            {
                text.extend(receive.recv_timeout(Duration::from_secs(4)).unwrap());
            }
            assert!(!text.windows(6).any(|chunk| chunk == b"secret"));
            assert!(text.windows(4).any(|chunk| chunk == b"***!"));
            copy.cancel();
        });
        let mut cmd = helper("stream");
        cmd.redact = vec!["secret".into()];
        let error = OsRunner
            .run(
                &cmd,
                &cancel,
                Sinks {
                    stdout: Some(&mut StreamingSink(send.clone())),
                    stderr: Some(&mut StreamingSink(send)),
                },
            )
            .unwrap_err();
        assert!(matches!(error, ExecError::Stopped { .. }));
        consumer.join().unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn cancellation_kills_group_after_direct_child_exit() {
        for direct_exits in [false, true] {
            let mut cmd = Cmd::new("sh").args([
                "-c",
                if direct_exits {
                    "(sleep 3; echo survived) & echo hi"
                } else {
                    "echo hi; sleep 10 & wait"
                },
            ]);
            cmd.timeout = Some(Duration::from_millis(300));
            let error = OsRunner.output(&cmd, &Cancellation::new()).unwrap_err();
            assert!(matches!(
                error,
                ExecError::Stopped {
                    reason: StopReason::TimedOut,
                    ..
                }
            ));
            assert_eq!(error.output().unwrap().stdout, b"hi\n");
            assert_eq!(
                error.output().unwrap().exit_code,
                if direct_exits { 0 } else { -1 }
            );
        }
    }

    #[cfg(unix)]
    #[test]
    fn interactive_cancel_preserves_descendants() {
        for interactive in [false, true] {
            let mut cmd = Cmd::new("sh").args(["-c", "(sleep 0.5; echo survived) & wait"]);
            cmd.interactive = interactive;
            cmd.timeout = Some(Duration::from_millis(100));
            let error = OsRunner.output(&cmd, &Cancellation::new()).unwrap_err();
            assert!(matches!(
                error,
                ExecError::Stopped {
                    reason: StopReason::TimedOut,
                    ..
                }
            ));
            assert_eq!(
                error.output().unwrap().stdout,
                if interactive {
                    b"survived\n".as_slice()
                } else {
                    b""
                }
            );
        }
    }

    #[cfg(unix)]
    #[test]
    fn explicit_cancel_kills_group_after_child_exit() {
        let cancel = Cancellation::new();
        let copy = cancel.clone();
        let timer = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(300));
            copy.cancel();
        });
        let start = Instant::now();
        let error = OsRunner
            .output(&Cmd::new("sh").args(["-c", "sleep 10 & echo hi"]), &cancel)
            .unwrap_err();
        timer.join().unwrap();
        assert!(matches!(
            error,
            ExecError::Stopped {
                reason: StopReason::Cancelled,
                ..
            }
        ));
        assert_eq!(error.output().unwrap().stdout, b"hi\n");
        assert!(start.elapsed() < PROCESS_WAIT_DELAY);
    }

    #[cfg(unix)]
    #[test]
    fn exit_signal_is_an_exit_error() {
        let error = OsRunner
            .output(
                &Cmd::new("sh").args(["-c", "kill -TERM $$"]),
                &Cancellation::new(),
            )
            .unwrap_err();
        assert!(matches!(error, ExecError::Exit(_)));
        assert_eq!(error.output().unwrap().exit_code, -1);
    }

    #[cfg(unix)]
    #[test]
    fn inherited_pipes_have_a_bound_without_cancellation() {
        let start = Instant::now();
        let error = OsRunner
            .output(
                &Cmd::new("sh").args(["-c", "sleep 3 & echo hi"]),
                &Cancellation::new(),
            )
            .unwrap_err();
        assert!(matches!(error, ExecError::Io { .. }));
        assert!(start.elapsed() >= PROCESS_WAIT_DELAY);
        assert!(start.elapsed() < Duration::from_secs(4));
    }
}
