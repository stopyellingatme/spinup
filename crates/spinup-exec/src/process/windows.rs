use std::ffi::c_void;
use std::io::{self, Read, Write};
use std::os::windows::io::AsRawHandle;
use std::process::ChildStdin;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use std::thread::{self, JoinHandle};
use std::time::Duration;

// These APIs ship in Windows. No additional dependency or runtime is required.
// SAFETY: These declarations use the kernel32 signatures and the Windows system ABI.
#[allow(unsafe_code)]
#[link(name = "kernel32")]
unsafe extern "system" {
    fn PeekNamedPipe(
        handle: *mut c_void,
        buffer: *mut c_void,
        size: u32,
        read: *mut u32,
        available: *mut u32,
        remaining: *mut u32,
    ) -> i32;
    fn CancelSynchronousIo(thread: *mut c_void) -> i32;
}

pub(crate) trait Pipe: Read + AsRawHandle {}
impl<T: Read + AsRawHandle> Pipe for T {}

pub(crate) fn prepare_pipe(_: &impl AsRawHandle) -> io::Result<()> {
    Ok(())
}

#[allow(unsafe_code)]
pub(crate) fn read_pipe(pipe: &mut impl Pipe, bytes: &mut [u8]) -> io::Result<usize> {
    let mut available = 0;
    // SAFETY: The pipe stays borrowed. Only the valid available pointer is written.
    let success = unsafe {
        PeekNamedPipe(
            pipe.as_raw_handle(),
            std::ptr::null_mut(),
            0,
            std::ptr::null_mut(),
            &mut available,
            std::ptr::null_mut(),
        )
    };
    if success == 0 {
        let error = io::Error::last_os_error();
        return if error.raw_os_error() == Some(109) {
            Ok(0)
        } else {
            Err(error)
        };
    }
    if available == 0 {
        return Err(io::ErrorKind::WouldBlock.into());
    }
    let count = bytes.len().min(available as usize);
    // This is the only reader. Read no more than PeekNamedPipe reported.
    pipe.read(&mut bytes[..count])
}

pub(crate) struct Input {
    writer: Option<JoinHandle<io::Result<()>>>,
    stop: Arc<AtomicBool>,
}

impl Input {
    pub(crate) fn new(pipe: Option<ChildStdin>, bytes: Vec<u8>) -> io::Result<Self> {
        let stop = Arc::new(AtomicBool::new(false));
        let signal = stop.clone();
        let writer = if let Some(mut pipe) = pipe {
            Some(
                thread::Builder::new()
                    .name("spinup-stdin".into())
                    .spawn(move || {
                        let mut position = 0;
                        while position < bytes.len() && !signal.load(Ordering::Acquire) {
                            let end = bytes.len().min(position + 8192);
                            match pipe.write(&bytes[position..end]) {
                                Ok(0) => return Err(io::ErrorKind::WriteZero.into()),
                                Ok(count) => position += count,
                                Err(error) if error.kind() == io::ErrorKind::BrokenPipe => {
                                    return Ok(());
                                }
                                Err(error) if error.kind() == io::ErrorKind::Interrupted => {
                                    continue;
                                }
                                Err(_) if signal.load(Ordering::Acquire) => return Ok(()),
                                Err(error) => return Err(error),
                            }
                        }
                        Ok(())
                    })?,
            )
        } else {
            None
        };
        Ok(Self { writer, stop })
    }

    pub(crate) fn pump(&mut self) -> io::Result<bool> {
        if self.writer.as_ref().is_some_and(JoinHandle::is_finished) {
            self.writer
                .take()
                .unwrap()
                .join()
                .map_err(|_| io::Error::other("stdin thread panicked"))??;
            return Ok(true);
        }
        Ok(false)
    }

    pub(crate) fn is_done(&self) -> bool {
        self.writer.is_none()
    }
}

impl Drop for Input {
    #[allow(unsafe_code)]
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(writer) = self.writer.take() {
            while !writer.is_finished() {
                // SAFETY: The JoinHandle keeps this thread handle open until join.
                // Repetition covers the race between the stop check and a blocked write.
                unsafe {
                    CancelSynchronousIo(writer.as_raw_handle());
                }
                thread::sleep(Duration::from_millis(5));
            }
            let _ = writer.join();
        }
    }
}
