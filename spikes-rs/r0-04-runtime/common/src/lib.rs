use std::io;
use std::path::PathBuf;
use std::process::ExitStatus;
use std::time::Duration;

/// The spike uses the noninteractive Go runner contract.
pub struct Cmd {
    pub program: String,
    pub args: Vec<String>,
    pub secrets: Vec<String>,
}

pub struct Output {
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
    pub status: ExitStatus,
    pub cancelled: bool,
}

pub struct Step {
    pub cmd: Cmd,
    pub lock: PathBuf,
    pub refresh: Duration,
}

pub struct Redactor {
    stages: Vec<Stage>,
}

impl Redactor {
    pub fn new(secrets: &[String]) -> Self {
        let mut secrets = secrets.to_vec();
        secrets.sort_by_key(|s| std::cmp::Reverse(s.len()));
        Self {
            stages: secrets
                .into_iter()
                .filter(|s| !s.is_empty())
                .map(|s| Stage {
                    secret: s.into_bytes(),
                    pending: Vec::new(),
                })
                .collect(),
        }
    }

    pub fn push(&mut self, data: &[u8], final_chunk: bool) -> Vec<u8> {
        self.stages
            .iter_mut()
            .fold(data.to_vec(), |data, stage| stage.push(&data, final_chunk))
    }
}

struct Stage {
    secret: Vec<u8>,
    pending: Vec<u8>,
}

impl Stage {
    fn push(&mut self, data: &[u8], final_chunk: bool) -> Vec<u8> {
        self.pending.extend_from_slice(data);
        let mut matched = 0;
        while let Some(index) = self.pending[matched..]
            .windows(self.secret.len())
            .position(|s| s == self.secret)
        {
            matched += index + self.secret.len();
        }
        let tail = &self.pending[matched..];
        let retain = if final_chunk {
            0
        } else {
            (1..self.secret.len())
                .rev()
                .find(|&n| tail.ends_with(&self.secret[..n]))
                .unwrap_or(0)
        };
        let limit = self.pending.len() - retain;
        let mut output = Vec::new();
        let mut index = 0;
        while index < limit {
            if self.pending[index..limit].starts_with(&self.secret) {
                output.extend_from_slice(b"***");
                index += self.secret.len();
            } else {
                output.push(self.pending[index]);
                index += 1;
            }
        }
        self.pending.drain(..limit);
        output
    }
}

/// Kill the Unix group. Windows retains the Go direct-child limitation.
#[cfg(unix)]
pub fn kill_group(pid: u32) -> io::Result<()> {
    // SAFETY: kill receives a valid numeric process group, with no borrowed memory.
    if unsafe { libc::kill(-(pid as i32), libc::SIGKILL) } == 0 {
        return Ok(());
    }
    let error = io::Error::last_os_error();
    if error.raw_os_error() == Some(libc::ESRCH) {
        Ok(())
    } else {
        Err(error)
    }
}

#[cfg(unix)]
pub fn nonblocking(fd: std::os::fd::RawFd) -> io::Result<()> {
    // SAFETY: the caller retains the pipe throughout both fcntl operations.
    let flags = unsafe { libc::fcntl(fd, libc::F_GETFL) };
    if flags < 0 || unsafe { libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// CLI arguments: LOCK SECRET PROGRAM [ARGS...], or --get HTTPS_URL.
pub fn parse_step(args: Vec<String>) -> io::Result<Step> {
    if args.len() < 3 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "expected LOCK SECRET PROGRAM [ARGS...]",
        ));
    }
    Ok(Step {
        lock: PathBuf::from(&args[0]),
        cmd: Cmd {
            program: args[2].clone(),
            args: args[3..].to_vec(),
            secrets: vec![args[1].clone()],
        },
        refresh: Duration::from_millis(20),
    })
}

/// Kill the group if an error or a dropped future interrupts the runner.
#[cfg(unix)]
pub struct ProcessGroup(Option<u32>);
#[cfg(unix)]
impl ProcessGroup {
    pub fn new(pid: u32) -> Self {
        Self(Some(pid))
    }
    pub fn pid(&self) -> u32 {
        self.0.unwrap()
    }
    pub fn disarm(&mut self) {
        self.0 = None;
    }
}
#[cfg(unix)]
impl Drop for ProcessGroup {
    fn drop(&mut self) {
        if let Some(pid) = self.0 {
            let _ = kill_group(pid);
        }
    }
}
