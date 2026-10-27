//! A local SMTP server for tests. It accepts every message and keeps the DATA of each
//! one. It binds 127.0.0.1 only.
//!
//! ```no_run
//! # tokio::runtime::Runtime::new().unwrap().block_on(async {
//! use spinup_kit::mail::stub::SmtpStub;
//! use spinup_kit::mail::{Config, Mailer, Message, new};
//!
//! let stub = SmtpStub::start().await;
//! let mailer = new(&Config { url: stub.url(), ..Config::default() }).unwrap();
//! let msg = Message {
//!     from: "app@example.com".into(),
//!     to: "you@example.com".into(),
//!     subject: "Hello".into(),
//!     text: "Hello from the app.".into(),
//!     ..Message::default()
//! };
//! mailer.send(&msg).await.unwrap();
//! assert!(stub.messages()[0].contains("Subject: Hello"));
//! # });
//! ```

use std::sync::{Arc, Mutex};

use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use tokio::task::JoinHandle;

/// The messages that the stub received.
type Inbox = Arc<Mutex<Vec<String>>>;

/// A running SMTP stub. Drop stops it.
#[derive(Debug)]
pub struct SmtpStub {
    port: u16,
    inbox: Inbox,
    task: JoinHandle<()>,
}

impl SmtpStub {
    /// Starts the stub on a free loopback port.
    ///
    /// # Panics
    ///
    /// When it cannot bind 127.0.0.1.
    pub async fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .unwrap_or_else(|e| panic!("mail stub: cannot bind 127.0.0.1: {e}"));
        let port = listener
            .local_addr()
            .unwrap_or_else(|e| panic!("mail stub: no local address: {e}"))
            .port();
        let inbox = Inbox::default();
        let store = inbox.clone();
        let task = tokio::spawn(async move {
            while let Ok((sock, _)) = listener.accept().await {
                tokio::spawn(serve(sock, store.clone()));
            }
        });
        Self { port, inbox, task }
    }

    /// The port.
    pub fn port(&self) -> u16 {
        self.port
    }

    /// The `SMTP_URL` of the stub: plain text, no user.
    pub fn url(&self) -> String {
        format!("smtp://127.0.0.1:{}", self.port)
    }

    /// The DATA of each received message, in order. Lines end with `\n`.
    pub fn messages(&self) -> Vec<String> {
        self.inbox
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone()
    }
}

impl Drop for SmtpStub {
    fn drop(&mut self) {
        self.task.abort();
    }
}

async fn serve(sock: tokio::net::TcpStream, store: Inbox) {
    let (r, mut w) = sock.into_split();
    let mut lines = BufReader::new(r).lines();
    let _ = w.write_all(b"220 stub ESMTP\r\n").await;
    let mut data: Option<String> = None;
    while let Ok(Some(line)) = lines.next_line().await {
        if let Some(body) = data.as_mut() {
            if line == "." {
                store
                    .lock()
                    .unwrap_or_else(std::sync::PoisonError::into_inner)
                    .push(std::mem::take(body));
                data = None;
                let _ = w.write_all(b"250 OK queued\r\n").await;
            } else {
                body.push_str(line.strip_prefix('.').unwrap_or(&line));
                body.push('\n');
            }
            continue;
        }
        let verb = line.get(..4).unwrap_or("").to_ascii_uppercase();
        let reply: &[u8] = match verb.as_str() {
            "EHLO" => b"250-stub\r\n250 8BITMIME\r\n",
            "HELO" | "MAIL" | "RCPT" | "RSET" | "NOOP" => b"250 OK\r\n",
            "DATA" => {
                data = Some(String::new());
                b"354 End data with <CR><LF>.<CR><LF>\r\n"
            }
            "QUIT" => {
                let _ = w.write_all(b"221 Bye\r\n").await;
                return;
            }
            _ => b"502 Command not implemented\r\n",
        };
        let _ = w.write_all(reply).await;
    }
}
