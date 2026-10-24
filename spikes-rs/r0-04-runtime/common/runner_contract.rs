use runtime_common::{Cmd, Output};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::thread;
use std::time::{Duration, Instant};

static NEXT: AtomicUsize = AtomicUsize::new(0);
struct Fixture(PathBuf);
impl Fixture {
    fn new() -> Self {
        let dir = std::env::temp_dir().join(format!(
            "r04-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir_all(&dir).unwrap();
        Self(dir)
    }
    fn path(&self, name: &str) -> PathBuf {
        self.0.join(name)
    }
    fn tree(&self, parent_exits: bool) -> Cmd {
        let script = r#"
            echo $$ > "$1/parent"
            sh -c 'echo $$ > "$1/grandchild"; while :; do echo tick >> "$1/heartbeat"; sleep 0.02; done' fixture "$1" &
            if [ "$2" = exit ]; then exit 0; fi
            wait
        "#;
        Cmd {
            program: "sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                self.0.to_string_lossy().into(),
                if parent_exits { "exit" } else { "wait" }.into(),
            ],
            secrets: vec![],
        }
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        // Also clean up on assertion failure.
        for name in ["parent", "grandchild"] {
            if let Ok(pid) = fs::read_to_string(self.path(name))
                && let Ok(pid) = pid.trim().parse()
            {
                let _ = runtime_common::kill_group(pid);
            }
        }
        let _ = fs::remove_dir_all(&self.0);
    }
}
fn wait_for(path: &Path) {
    let end = Instant::now() + Duration::from_secs(5);
    while !path.exists() {
        assert!(Instant::now() < end, "fixture did not create {path:?}");
        thread::sleep(Duration::from_millis(10));
    }
}
fn stopped(f: &Fixture) {
    wait_for(&f.path("grandchild"));
    thread::sleep(Duration::from_millis(80));
    let before = fs::read(f.path("heartbeat")).unwrap();
    thread::sleep(Duration::from_millis(80));
    assert_eq!(
        before,
        fs::read(f.path("heartbeat")).unwrap(),
        "grandchild survived"
    );
}
fn cancellation_contract(run: fn(Cmd, Option<Duration>) -> Output) {
    for parent_exits in [false, true] {
        let f = Fixture::new();
        let result = run(f.tree(parent_exits), Some(Duration::from_millis(300)));
        assert!(result.cancelled);
        use std::os::unix::process::ExitStatusExt;
        if parent_exits {
            assert!(result.status.success());
        } else {
            assert_eq!(result.status.signal(), Some(9));
        }
        stopped(&f);
    }
}
fn output_contract(run: fn(Cmd, Option<Duration>) -> Output) {
    let result = run(Cmd { program: "sh".into(), args: vec!["-c".into(), "printf sec; sleep 0.03; printf 'ret!'; printf sec >&2; sleep 0.03; printf 'ret?' >&2; exit 7".into()], secrets: vec!["secret".into()] }, None);
    assert_eq!(result.stdout, b"***!");
    assert_eq!(result.stderr, b"***?");
    assert_eq!(result.status.code(), Some(7));
    assert!(!result.cancelled);
}
struct Cli(Child);
impl Drop for Cli {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}
fn cli_contract(binary: &str, interrupt: bool) {
    let f = Fixture::new();
    let cmd = if interrupt {
        f.tree(false)
    } else {
        Cmd {
            program: "sh".into(),
            args: vec!["-c".into(), "sleep 0.25".into()],
            secrets: vec![],
        }
    };
    let mut cli = Cli(Command::new(binary)
        .arg(f.path("lock"))
        .arg("secret")
        .arg(cmd.program)
        .args(cmd.args)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap());
    wait_for(&f.path("lock"));
    let first = fs::read(f.path("lock")).unwrap();
    thread::sleep(Duration::from_millis(70));
    assert_ne!(
        first,
        fs::read(f.path("lock")).unwrap(),
        "refresh did not run"
    );
    if interrupt {
        wait_for(&f.path("grandchild"));
        assert!(
            Command::new("kill")
                .args(["-INT", &cli.0.id().to_string()])
                .status()
                .unwrap()
                .success()
        );
    }
    let end = Instant::now() + Duration::from_secs(5);
    let status = loop {
        if let Some(status) = cli.0.try_wait().unwrap() {
            break status;
        }
        assert!(Instant::now() < end, "CLI did not finish");
        thread::sleep(Duration::from_millis(10));
    };
    assert_eq!(status.success(), !interrupt);
    let last = fs::read(f.path("lock")).unwrap();
    thread::sleep(Duration::from_millis(60));
    assert_eq!(last, fs::read(f.path("lock")).unwrap());
    if interrupt {
        stopped(&f);
    }
}

fn streaming_contract(binary: &str) {
    use std::io::Read;
    let f = Fixture::new();
    let script = "printf sec; sleep 0.03; printf 'ret!'; printf sec >&2; sleep 0.03; printf 'ret?' >&2; while [ ! -f \"$1/proceed\" ]; do sleep 0.02; done";
    let mut cli = Cli(Command::new(binary)
        .arg(f.path("lock"))
        .arg("secret")
        .args(["sh", "-c", script, "fixture"])
        .arg(&f.0)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap());
    let mut out = cli.0.stdout.take().unwrap();
    let mut err = cli.0.stderr.take().unwrap();
    let (send, receive) = std::sync::mpsc::channel();
    let reader = thread::spawn(move || {
        let mut stdout = [0; 4];
        let mut stderr = [0; 4];
        out.read_exact(&mut stdout).unwrap();
        err.read_exact(&mut stderr).unwrap();
        send.send((stdout, stderr)).unwrap();
    });
    assert_eq!(
        receive.recv_timeout(Duration::from_secs(5)).unwrap(),
        (*b"***!", *b"***?")
    );
    assert!(
        cli.0.try_wait().unwrap().is_none(),
        "child exited before release"
    );
    fs::write(f.path("proceed"), b"go").unwrap();
    assert!(cli.0.wait().unwrap().success());
    reader.join().unwrap();
}

fn failed_spawn_contract(binary: &str) {
    let f = Fixture::new();
    let status = Command::new(binary)
        .arg(f.path("lock"))
        .arg("secret")
        .arg(f.path("missing-executable"))
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .unwrap();
    assert!(!status.success());
    let last = fs::read(f.path("lock")).unwrap();
    thread::sleep(Duration::from_millis(60));
    assert_eq!(last, fs::read(f.path("lock")).unwrap());
}
