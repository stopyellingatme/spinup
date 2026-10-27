//! The `healthcheck` command (Go `kit/app/healthcheck.go`).

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::time::{Duration, Instant};

/// The time limit of the health check.
const LIMIT: Duration = Duration::from_secs(2);

/// Asks the server on `127.0.0.1:<port>` for `GET /healthz` and fails unless it answers 200
/// within two seconds. Distroless images have no shell and no curl, so the Docker health
/// check runs the app binary with `healthcheck`. It uses plain HTTP/1.1 over a
/// `std::net::TcpStream` and blocks the thread.
pub fn healthcheck(port: u16) -> anyhow::Result<()> {
    let start = Instant::now();
    let fail = |e: std::io::Error| {
        anyhow::anyhow!("healthcheck: {e}\nTry: check that the server is running on port {port}")
    };
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    let mut stream = TcpStream::connect_timeout(&addr, LIMIT).map_err(fail)?;
    let remaining = || {
        LIMIT
            .checked_sub(start.elapsed())
            .filter(|d| !d.is_zero())
            .unwrap_or(Duration::from_millis(1))
    };
    stream.set_write_timeout(Some(remaining())).map_err(fail)?;
    stream
        .write_all(b"GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
        .map_err(fail)?;
    let mut head = Vec::new();
    let mut buf = [0u8; 512];
    // The status line is enough. Read until the first line ends, the peer closes, or the
    // time is over.
    while !head.contains(&b'\n') {
        if start.elapsed() >= LIMIT {
            anyhow::bail!(
                "healthcheck: no answer in 2s\nTry: check that the server is running on port {port}"
            );
        }
        stream.set_read_timeout(Some(remaining())).map_err(fail)?;
        match stream.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => head.extend_from_slice(&buf[..n]),
            Err(e)
                if matches!(
                    e.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                ) =>
            {
                anyhow::bail!(
                    "healthcheck: no answer in 2s\nTry: check that the server is running on port {port}"
                );
            }
            Err(e) => return Err(fail(e)),
        }
    }
    let line = String::from_utf8_lossy(&head);
    let status = line.split_whitespace().nth(1).unwrap_or("");
    if status != "200" {
        anyhow::bail!("healthcheck: /healthz answered {status:?}\nTry: read the server logs");
    }
    Ok(())
}
