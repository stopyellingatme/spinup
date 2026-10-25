use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::process::ChildStdin;

pub(crate) trait Pipe: Read + AsRawFd {}
impl<T: Read + AsRawFd> Pipe for T {}

#[allow(unsafe_code)]
pub(crate) fn prepare_pipe(pipe: &impl AsRawFd) -> io::Result<()> {
    // SAFETY: The borrowed pipe owns this descriptor throughout both fcntl calls.
    let flags = unsafe { libc::fcntl(pipe.as_raw_fd(), libc::F_GETFL) };
    if flags == -1 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: F_SETFL changes only the flags of the same live pipe descriptor.
    if unsafe { libc::fcntl(pipe.as_raw_fd(), libc::F_SETFL, flags | libc::O_NONBLOCK) } == -1 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

pub(crate) fn read_pipe(pipe: &mut impl Pipe, bytes: &mut [u8]) -> io::Result<usize> {
    pipe.read(bytes)
}

#[allow(unsafe_code)]
pub(super) fn kill_group(pid: u32) -> io::Result<()> {
    let pid = i32::try_from(pid).map_err(|_| io::Error::other("process group exceeds pid_t"))?;
    // SAFETY: The child PID identifies its group. No memory crosses this call.
    if unsafe { libc::kill(-pid, libc::SIGKILL) } == 0 {
        return Ok(());
    }
    let error = io::Error::last_os_error();
    if error.raw_os_error() == Some(libc::ESRCH) {
        Ok(())
    } else {
        Err(error)
    }
}

pub(crate) struct Input {
    pipe: Option<ChildStdin>,
    bytes: Vec<u8>,
    position: usize,
}

impl Input {
    pub(crate) fn new(pipe: Option<ChildStdin>, bytes: Vec<u8>) -> io::Result<Self> {
        if let Some(pipe) = &pipe {
            prepare_pipe(pipe)?;
        }
        Ok(Self {
            pipe,
            bytes,
            position: 0,
        })
    }

    pub(crate) fn pump(&mut self) -> io::Result<bool> {
        if self.position == self.bytes.len() {
            self.pipe = None;
        }
        let Some(pipe) = &mut self.pipe else {
            return Ok(false);
        };
        let end = self.bytes.len().min(self.position + 8192);
        match pipe.write(&self.bytes[self.position..end]) {
            Ok(0) => Err(io::Error::new(
                io::ErrorKind::WriteZero,
                "command stdin accepted no bytes",
            )),
            Ok(count) => {
                self.position += count;
                Ok(true)
            }
            Err(error) if error.kind() == io::ErrorKind::BrokenPipe => {
                self.pipe = None;
                Ok(false)
            }
            Err(error)
                if matches!(
                    error.kind(),
                    io::ErrorKind::Interrupted | io::ErrorKind::WouldBlock
                ) =>
            {
                Ok(false)
            }
            Err(error) => Err(error),
        }
    }

    pub(crate) fn is_done(&self) -> bool {
        self.pipe.is_none()
    }
}
