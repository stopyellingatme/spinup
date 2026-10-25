//! The own dev loop of spike r0-11: watch the source (`notify`), rebuild,
//! restart the app, and wait for readiness.
//!
//! ```text
//! supervisor --watch todo-api/src --build "cargo build -p todo-api" \
//!     --port 18311 [--listenfd] [--events events.jsonl] -- target/debug/todo-api serve
//! ```
//!
//! On a change it drains the events for `--debounce-ms`, runs `--build`
//! (and `--post-build`, such as the spec emit), and on success stops the old
//! app (SIGTERM, then SIGKILL after `--stop-timeout-ms`), starts the new one,
//! and polls `GET --ready-path` until it answers 200. The old app serves
//! until the new binary exists. A failed build keeps the old app.
//!
//! With `--listenfd`, the supervisor binds the port once and passes the
//! socket to each app as fd 3 (`LISTEN_FDS=1`, the listenfd and systemd
//! protocol). Connections then wait in the backlog during a restart instead
//! of failing.
//!
//! `--events` writes one JSON line per step with the wall clock in
//! nanoseconds since the Unix epoch, so `measure.py` can split a sample into
//! detect, build, stop, start, and ready.

use std::fs::File;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::os::fd::AsRawFd;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitCode};
use std::sync::mpsc::{Receiver, RecvTimeoutError, channel};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use clap::Parser;
use notify::{EventKind, RecursiveMode, Watcher};

#[derive(Parser, Debug)]
#[command(
    name = "supervisor",
    about = "Watch, rebuild, restart, and wait for readiness"
)]
struct Args {
    /// A file or directory to watch (repeatable).
    #[arg(long = "watch", required = true)]
    watch: Vec<PathBuf>,
    /// The file extensions that trigger a rebuild.
    #[arg(long, value_delimiter = ',', default_value = "rs,sql")]
    ext: Vec<String>,
    /// The quiet time after the first event before the build starts.
    #[arg(long, default_value_t = 10)]
    debounce_ms: u64,
    /// The build command (run with `sh -c`).
    #[arg(long)]
    build: String,
    /// A command that runs after a good build, before the restart (`sh -c`).
    #[arg(long)]
    post_build: Option<String>,
    /// The port of the app.
    #[arg(long)]
    port: u16,
    /// The readiness path. A 200 answer means ready.
    #[arg(long, default_value = "/healthz")]
    ready_path: String,
    /// Bind the port in the supervisor and pass it to the app as fd 3.
    #[arg(long)]
    listenfd: bool,
    /// The longest wait after SIGTERM before SIGKILL.
    #[arg(long, default_value_t = 2000)]
    stop_timeout_ms: u64,
    /// The file for the JSON event lines.
    #[arg(long)]
    events: Option<PathBuf>,
    /// The app command and its arguments.
    #[arg(last = true, required = true)]
    run: Vec<String>,
}

fn main() -> ExitCode {
    match run(Args::parse()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("supervisor: {err:#}");
            ExitCode::FAILURE
        }
    }
}

/// The wall clock in nanoseconds since the Unix epoch.
fn now_ns() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos())
}

/// Writes the JSON event lines.
struct Events(Option<File>);

impl Events {
    fn emit(&mut self, event: &str, extra: &str) {
        let line = format!("{{\"t\":{},\"event\":\"{event}\"{extra}}}\n", now_ns());
        if let Some(f) = &mut self.0 {
            let _ = f.write_all(line.as_bytes());
            let _ = f.flush();
        }
        eprint!("supervisor: {line}");
    }
}

/// Returns true when `path` has one of the watched extensions and is not
/// inside a `target` directory.
pub fn relevant(path: &Path, exts: &[String]) -> bool {
    if path.components().any(|c| c.as_os_str() == "target") {
        return false;
    }
    path.extension()
        .and_then(|e| e.to_str())
        .is_some_and(|e| exts.iter().any(|x| x == e))
}

fn run(args: Args) -> Result<()> {
    let mut events = Events(match &args.events {
        Some(p) => Some(File::create(p).with_context(|| format!("create {}", p.display()))?),
        None => None,
    });
    let listener = if args.listenfd {
        let l = TcpListener::bind(("127.0.0.1", args.port))
            .with_context(|| format!("bind 127.0.0.1:{}", args.port))?;
        Some(l)
    } else {
        None
    };

    let (tx, rx) = channel();
    let exts = args.ext.clone();
    let mut watcher = notify::recommended_watcher(move |res: notify::Result<notify::Event>| {
        if let Ok(ev) = res {
            let modifying = matches!(
                ev.kind,
                EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_)
            );
            if modifying && ev.paths.iter().any(|p| relevant(p, &exts)) {
                let _ = tx.send(now_ns());
            }
        }
    })?;
    for w in &args.watch {
        watcher
            .watch(w, RecursiveMode::Recursive)
            .with_context(|| format!("watch {}", w.display()))?;
    }

    let mut child: Option<Child> = None;
    events.emit("start", "");
    if build(&args, &mut events)? {
        child = Some(restart(&args, None, listener.as_ref(), &mut events)?);
    }
    loop {
        let first = rx.recv().context("the watcher stopped")?;
        events.emit("change", &format!(",\"fs_t\":{first}"));
        drain(&rx, Duration::from_millis(args.debounce_ms));
        events.emit("debounced", "");
        if build(&args, &mut events)? {
            child = Some(restart(&args, child, listener.as_ref(), &mut events)?);
        }
    }
}

/// Waits until no event arrives for `quiet`.
fn drain(rx: &Receiver<u128>, quiet: Duration) {
    loop {
        match rx.recv_timeout(quiet) {
            Ok(_) => continue,
            Err(RecvTimeoutError::Timeout | RecvTimeoutError::Disconnected) => return,
        }
    }
}

/// Runs the build and the post-build command. It returns false on a failed
/// build: the old app keeps running.
fn build(args: &Args, events: &mut Events) -> Result<bool> {
    events.emit("build_start", "");
    let status = Command::new("sh").arg("-c").arg(&args.build).status()?;
    events.emit("build_end", &format!(",\"ok\":{}", status.success()));
    if !status.success() {
        return Ok(false);
    }
    if let Some(post) = &args.post_build {
        let status = Command::new("sh").arg("-c").arg(post).status()?;
        events.emit("post_build_end", &format!(",\"ok\":{}", status.success()));
        if !status.success() {
            return Ok(false);
        }
    }
    Ok(true)
}

/// Stops the old app, starts the new one, and waits for readiness.
fn restart(
    args: &Args,
    old: Option<Child>,
    listener: Option<&TcpListener>,
    events: &mut Events,
) -> Result<Child> {
    if let Some(old) = old {
        stop(old, Duration::from_millis(args.stop_timeout_ms))?;
        events.emit("stopped", "");
    }
    let mut cmd = Command::new(&args.run[0]);
    cmd.args(&args.run[1..]).env("PORT", args.port.to_string());
    if let Some(l) = listener {
        let fd = l.as_raw_fd();
        cmd.env("LISTEN_FDS", "1").env_remove("LISTEN_PID");
        // SAFETY: dup2 and fcntl are async-signal-safe, and the closure
        // touches no memory of the parent.
        unsafe {
            cmd.pre_exec(move || {
                if fd == 3 {
                    // dup2(3, 3) keeps FD_CLOEXEC, so clear it.
                    if libc::fcntl(3, libc::F_SETFD, 0) == -1 {
                        return Err(std::io::Error::last_os_error());
                    }
                } else if libc::dup2(fd, 3) == -1 {
                    return Err(std::io::Error::last_os_error());
                }
                Ok(())
            });
        }
    }
    let mut child = cmd
        .spawn()
        .with_context(|| format!("start {}", args.run[0]))?;
    events.emit("spawned", &format!(",\"pid\":{}", child.id()));
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(status) = child.try_wait()? {
            bail!("the app exited during the start: {status}\nTry: read the app log above");
        }
        if probe(args.port, &args.ready_path) {
            events.emit("ready", "");
            return Ok(child);
        }
        if Instant::now() > deadline {
            bail!("the app is not ready after 60 s\nTry: read the app log above");
        }
        std::thread::sleep(Duration::from_millis(2));
    }
}

/// Sends SIGTERM and waits. After `timeout` it sends SIGKILL.
fn stop(mut child: Child, timeout: Duration) -> Result<()> {
    let pid = i32::try_from(child.id())?;
    // SAFETY: kill has no memory effects.
    unsafe { libc::kill(pid, libc::SIGTERM) };
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if child.try_wait()?.is_some() {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(1));
    }
    child.kill()?;
    child.wait()?;
    Ok(())
}

/// Returns true when `GET path` on the port answers 200.
pub fn probe(port: u16, path: &str) -> bool {
    let Ok(mut s) = TcpStream::connect(("127.0.0.1", port)) else {
        return false;
    };
    let _ = s.set_read_timeout(Some(Duration::from_secs(10)));
    let req = format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
    if s.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut head = [0u8; 12];
    let mut got = 0;
    while got < head.len() {
        match s.read(&mut head[got..]) {
            Ok(0) | Err(_) => return false,
            Ok(n) => got += n,
        }
    }
    &head[9..12] == b"200"
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn relevant_filters_extensions_and_target() {
        let exts = vec!["rs".to_owned(), "sql".to_owned()];
        assert!(relevant(Path::new("todo-api/src/handlers/todos.rs"), &exts));
        assert!(relevant(
            Path::new("todo-api/migrations/00005_x.up.sql"),
            &exts
        ));
        assert!(!relevant(Path::new("todo-api/src/notes.md"), &exts));
        assert!(!relevant(Path::new("target/debug/build/x.rs"), &exts));
        assert!(!relevant(Path::new("todo-api/src/.todos.rs.swp"), &exts));
    }

    #[test]
    fn probe_reads_the_status() {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = l.local_addr().unwrap().port();
        let server = std::thread::spawn(move || {
            for (i, conn) in l.incoming().take(2).enumerate() {
                let mut c = conn.unwrap();
                let mut buf = [0u8; 256];
                let _ = c.read(&mut buf);
                let status = if i == 0 {
                    "200 OK"
                } else {
                    "503 Service Unavailable"
                };
                let _ = write!(c, "HTTP/1.1 {status}\r\nContent-Length: 0\r\n\r\n");
            }
        });
        assert!(probe(port, "/healthz"));
        assert!(!probe(port, "/healthz"));
        server.join().unwrap();
    }
}
