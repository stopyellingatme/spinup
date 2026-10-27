//! Mail through lettre, as the Go `kit/mail` package: an SMTP mailer from
//! `SMTP_URL`, a file mailer for development and tests, and (with the
//! `test-util` feature) a local SMTP stub.

use std::path::PathBuf;

use async_trait::async_trait;
use lettre::message::header::ContentType;
use lettre::{AsyncFileTransport, AsyncSmtpTransport, AsyncTransport, Tokio1Executor};

/// One plain-text message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Message {
    /// The sender.
    pub from: String,
    /// The recipient.
    pub to: String,
    /// The subject.
    pub subject: String,
    /// The text body.
    pub text: String,
}

impl Message {
    /// Builds the lettre message. It fails for an invalid address.
    pub fn build(&self) -> anyhow::Result<lettre::Message> {
        Ok(lettre::Message::builder()
            .from(self.from.parse()?)
            .to(self.to.parse()?)
            .subject(&self.subject)
            .header(ContentType::TEXT_PLAIN)
            .body(self.text.clone())?)
    }
}

/// Sends messages.
#[async_trait]
pub trait Mailer: Send + Sync + 'static {
    /// Sends one message.
    async fn send(&self, msg: &Message) -> anyhow::Result<()>;
}

/// An SMTP mailer (lettre with rustls).
#[derive(Clone)]
pub struct SmtpMailer(AsyncSmtpTransport<Tokio1Executor>);

impl SmtpMailer {
    /// Returns a mailer from `SMTP_URL`, for example `smtp://localhost:1025`
    /// (Mailpit, no TLS) or `smtps://user:pass@smtp.example.com`. lettre
    /// reads the TLS mode from the scheme and the `tls` query parameter.
    pub fn from_url(url: &str) -> anyhow::Result<Self> {
        let t = AsyncSmtpTransport::<Tokio1Executor>::from_url(url)
            .map_err(|e| {
                anyhow::anyhow!(
                    "mail: SMTP_URL is not valid: {e}\nTry: set SMTP_URL to smtp://host:port or smtps://user@host"
                )
            })?
            .build();
        Ok(Self(t))
    }
}

#[async_trait]
impl Mailer for SmtpMailer {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        self.0.send(msg.build()?).await?;
        Ok(())
    }
}

/// Writes each message as an `.eml` file into a directory.
#[derive(Clone)]
pub struct FileMailer {
    transport: AsyncFileTransport<Tokio1Executor>,
    dir: PathBuf,
}

impl FileMailer {
    /// Returns a mailer that writes into `dir`.
    pub fn new(dir: impl Into<PathBuf>) -> Self {
        let dir = dir.into();
        Self {
            transport: AsyncFileTransport::new(&dir),
            dir,
        }
    }

    /// The directory.
    pub fn dir(&self) -> &std::path::Path {
        &self.dir
    }
}

#[async_trait]
impl Mailer for FileMailer {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        self.transport.send(msg.build()?).await?;
        Ok(())
    }
}

/// A local SMTP server for tests. It accepts every message and keeps the
/// DATA of each one.
#[cfg(any(test, feature = "test-util"))]
pub mod smtp_stub {
    use std::sync::{Arc, Mutex};

    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    use tokio::net::TcpListener;

    /// The messages that the stub received.
    pub type Inbox = Arc<Mutex<Vec<String>>>;

    /// Starts the stub on a free loopback port. It returns the port and the inbox.
    pub async fn start() -> (u16, Inbox) {
        let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind");
        let port = listener.local_addr().expect("addr").port();
        let inbox: Inbox = Arc::default();
        let store = inbox.clone();
        tokio::spawn(async move {
            while let Ok((sock, _)) = listener.accept().await {
                let store = store.clone();
                tokio::spawn(async move {
                    let (r, mut w) = sock.into_split();
                    let mut lines = BufReader::new(r).lines();
                    let _ = w.write_all(b"220 stub ESMTP\r\n").await;
                    let mut data: Option<String> = None;
                    while let Ok(Some(line)) = lines.next_line().await {
                        if let Some(body) = data.as_mut() {
                            if line == "." {
                                store.lock().expect("inbox").push(std::mem::take(body));
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
                });
            }
        });
        (port, inbox)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn msg() -> Message {
        Message {
            from: "app@example.com".into(),
            to: "person@example.com".into(),
            subject: "Reset your password".into(),
            text: "Reset your password: https://app.example/reset/abc\n".into(),
        }
    }

    #[tokio::test]
    async fn smtp_mailer_against_the_stub() {
        let (port, inbox) = smtp_stub::start().await;
        let m = SmtpMailer::from_url(&format!("smtp://127.0.0.1:{port}")).unwrap();
        m.send(&msg()).await.unwrap();
        let got = inbox.lock().unwrap().clone();
        assert_eq!(got.len(), 1);
        assert!(
            got[0].contains("Subject: Reset your password"),
            "{}",
            got[0]
        );
        assert!(
            got[0].contains("https://app.example/reset/abc"),
            "{}",
            got[0]
        );
    }

    #[tokio::test]
    async fn file_mailer_writes_eml() {
        let dir = std::env::temp_dir().join(format!("kit-mail-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        FileMailer::new(&dir).send(&msg()).await.unwrap();
        let files: Vec<_> = std::fs::read_dir(&dir).unwrap().collect();
        assert_eq!(files.len(), 1);
        let body = std::fs::read_to_string(files[0].as_ref().unwrap().path()).unwrap();
        assert!(body.contains("To: person@example.com"), "{body}");
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn bad_url_has_try() {
        let err = SmtpMailer::from_url("ftp://x").err().unwrap().to_string();
        assert!(err.contains("\nTry: "), "{err}");
    }
}
