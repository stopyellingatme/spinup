//! The tests of kit mail (Go `kit/mail/mail_test.go`), plus SMTP round trips against the
//! stub of the testkit. Go `TestCapture` also sends with a canceled context. Rust has no
//! context: a dropped future is the cancellation, so that case has no port.

use std::io;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use spinup_kit::config::load_from;
use spinup_kit::mail::stub::SmtpStub;
use spinup_kit::mail::{
    Capture, Config, Log, Mailer, Message, SmtpMailer, Templates, async_trait, new, render,
};

fn msg() -> Message {
    Message {
        from: "app@example.com".into(),
        to: "you@example.com".into(),
        subject: "Hi".into(),
        text: "Hello".into(),
        html: String::new(),
    }
}

fn files() -> Vec<(&'static str, &'static str)> {
    vec![
        ("welcome.subject.tmpl", "Hello {{ name }}\n"),
        ("welcome.txt.tmpl", "Hello {{ name }}"),
        ("welcome.html.tmpl", "<p>Hello {{ name }}</p>"),
    ]
}

// TestRender.
#[test]
fn mail_render() {
    let t = Templates::new(files());
    let data = serde_json::json!({"name": "<script>"});
    let m = render(&t, "welcome", "app@example.com", "you@example.com", &data).unwrap();
    assert_eq!(m.subject, "Hello <script>");
    assert_eq!(m.text, "Hello <script>");
    assert_eq!(m.html, "<p>Hello &lt;script&gt;</p>");
    for name in ["../welcome", "missing", "", ".", "a\\b"] {
        let err = render(&t, name, &m.from, &m.to, &data).unwrap_err();
        assert!(err.to_string().contains("\nTry: "), "{name}: {err}");
    }
    // A missing value is an error, as Go missingkey=error.
    let err = render(&t, "welcome", &m.from, &m.to, &serde_json::json!({})).unwrap_err();
    assert!(err.to_string().contains("\nTry: "), "{err}");
    let without_text: Vec<_> = files()
        .into_iter()
        .filter(|(n, _)| *n != "welcome.txt.tmpl")
        .collect();
    let err = render(
        &Templates::new(without_text),
        "welcome",
        &m.from,
        &m.to,
        &data,
    )
    .unwrap_err();
    assert!(
        err.to_string().contains("welcome.txt.tmpl"),
        "missing plain text accepted: {err}"
    );
    // A blank text part fails the message check.
    let blank = Templates::new([
        ("b.subject.tmpl", "Subject"),
        ("b.txt.tmpl", "  "),
        ("b.html.tmpl", "<p>x</p>"),
    ]);
    assert!(render(&blank, "b", &m.from, &m.to, &data).is_err());
}

#[test]
fn mail_render_keeps_the_final_newline() {
    let t = Templates::new([
        ("n.subject.tmpl", "  Subject  \n"),
        ("n.txt.tmpl", "Line {{ n }}\n"),
        ("n.html.tmpl", "<p>{{ n }}</p>\n"),
    ]);
    let m = render(
        &t,
        "n",
        "App <app@example.com>",
        "you@example.com",
        &serde_json::json!({"n": 1}),
    )
    .unwrap();
    assert_eq!(m.subject, "Subject");
    assert_eq!(m.text, "Line 1\n");
    assert_eq!(m.html, "<p>1</p>\n");
}

// TestCapture.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn mail_capture() {
    let c = Capture::new();
    let mut tasks = Vec::new();
    for _ in 0..20 {
        let c = c.clone();
        tasks.push(tokio::spawn(async move { c.send(&msg()).await }));
    }
    for t in tasks {
        t.await.unwrap().unwrap();
    }
    let mut got = c.messages();
    assert_eq!(got.len(), 20);
    got[0].text = "changed".into();
    assert_eq!(
        c.messages()[0].text,
        "Hello",
        "capture returned internal storage"
    );
}

// TestSMTPConfig.
#[test]
fn mail_smtp_config() {
    for (url, ok) in [
        ("smtp://localhost:1025", true),
        ("smtps://user@example.com:465", true),
        ("smtp+starttls://user@example.com:587", true),
        ("smtp://user@example.com:587", true),
        ("http://example.com", false),
        ("smtp://user:secret@example.com", false),
        ("smtp://example.com:0", false),
        ("smtp://example.com?tls=invalid", false),
        ("smtp://user@example.com?tls=none", false),
    ] {
        let config = Config {
            url: url.into(),
            password: "secret".into(),
            timeout: Duration::from_secs(1),
        };
        let got = SmtpMailer::new(&config);
        assert_eq!(got.is_ok(), ok, "{url}: {:?}", got.as_ref().err());
        if let Err(e) = got {
            let text = e.to_string();
            assert!(
                text.contains("Try:") && !text.contains("secret"),
                "unsafe error: {text}"
            );
        }
        assert_eq!(new(&config).is_ok(), ok, "{url}");
    }
}

#[test]
fn mail_new_without_url_logs() {
    // An empty URL selects the log mailer. A Debug form never shows the password.
    let config = Config {
        password: "secret".into(),
        ..Config::default()
    };
    assert!(new(&config).is_ok());
    assert!(!format!("{config:?}").contains("secret"));
}

// TestMessageValidation.
#[tokio::test]
async fn mail_message_validation() {
    let cases = [
        (
            "missing text",
            Message {
                text: String::new(),
                html: "<p>Hi</p>".into(),
                ..msg()
            },
        ),
        (
            "subject injection",
            Message {
                subject: "Hi\r\nBcc: attacker@example.com".into(),
                ..msg()
            },
        ),
        (
            "address injection",
            Message {
                to: "you@example.com\nBcc: attacker@example.com".into(),
                ..msg()
            },
        ),
        (
            "invalid address",
            Message {
                from: "not an address".into(),
                ..msg()
            },
        ),
    ];
    for (name, m) in cases {
        let err = Capture::new().send(&m).await.unwrap_err();
        assert!(err.to_string().contains("\nTry: "), "{name}: {err}");
        assert!(Log.send(&m).await.is_err(), "{name}");
    }
}

// TestConfigSecretFile.
#[test]
fn mail_config_secret_file() {
    let env = [
        ("SMTP_URL", "smtps://user@example.com:465"),
        ("SMTP_PASSWORD_FILE", "/secrets/smtp"),
        ("SMTP_TIMEOUT", "3s"),
    ];
    let lookup = |k: &str| {
        env.iter()
            .find(|(n, _)| *n == k)
            .map(|(_, v)| (*v).to_owned())
    };
    let read = |p: &Path| -> io::Result<Vec<u8>> {
        assert_eq!(p, Path::new("/secrets/smtp"));
        Ok(b"secret\n".to_vec())
    };
    let cfg: Config = load_from(lookup, read).unwrap();
    assert_eq!(cfg.password, "secret");
    assert_eq!(cfg.timeout, Duration::from_secs(3));
    assert_eq!(cfg.url, "smtps://user@example.com:465");
    let defaults: Config = load_from(|_| None, |_| Err(io::ErrorKind::NotFound.into())).unwrap();
    assert_eq!(defaults.timeout, Duration::from_secs(10));
    assert!(defaults.url.is_empty());
}

#[derive(Clone, Default)]
struct Buf(Arc<Mutex<Vec<u8>>>);

impl io::Write for Buf {
    fn write(&mut self, b: &[u8]) -> io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(b);
        Ok(b.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// TestLogOmitsMessageContent.
#[tokio::test]
async fn mail_log_omits_message_content() {
    let buf = Buf::default();
    let w = buf.clone();
    let subscriber = tracing_subscriber::fmt()
        .with_writer(move || w.clone())
        .with_ansi(false)
        .finish();
    let _guard = tracing::subscriber::set_default(subscriber);
    let m = Message {
        to: "private@example.com".into(),
        subject: "private subject".into(),
        text: "secret reset token".into(),
        ..msg()
    };
    Log.send(&m).await.unwrap();
    let out = String::from_utf8(buf.0.lock().unwrap().clone()).unwrap();
    assert!(out.contains("mail: delivery logged"), "{out}");
    assert!(
        !out.contains("private") && !out.contains("secret"),
        "message content was logged: {out}"
    );
}

#[tokio::test]
async fn mail_smtp_round_trip() {
    let stub = SmtpStub::start().await;
    let mailer = new(&Config {
        url: stub.url(),
        ..Config::default()
    })
    .unwrap();
    mailer.send(&msg()).await.unwrap();
    let html = Message {
        subject: "Reset your password".into(),
        text: "Reset: https://app.example/reset/abc\n".into(),
        html: "<p><a href=\"https://app.example/reset/abc\">Reset</a></p>".into(),
        ..msg()
    };
    mailer.send(&html).await.unwrap();
    let got = stub.messages();
    assert_eq!(got.len(), 2, "{got:?}");
    assert!(got[0].contains("Subject: Hi"), "{}", got[0]);
    assert!(got[0].contains("To: you@example.com"), "{}", got[0]);
    assert!(got[0].contains("text/plain"), "{}", got[0]);
    assert!(got[1].contains("multipart/alternative"), "{}", got[1]);
    assert!(got[1].contains("text/html"), "{}", got[1]);
    assert!(
        got[1].contains("https://app.example/reset/abc"),
        "{}",
        got[1]
    );
    // An invalid message never reaches the server.
    let bad = Message {
        subject: String::new(),
        ..msg()
    };
    assert!(mailer.send(&bad).await.is_err());
    assert_eq!(stub.messages().len(), 2);
}

#[tokio::test]
async fn mail_smtp_timeout_and_refusal() {
    // A server that accepts and never answers: the send stops at the timeout.
    let silent = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = silent.local_addr().unwrap().port();
    let hold = tokio::spawn(async move {
        let mut conns = Vec::new();
        while let Ok((c, _)) = silent.accept().await {
            conns.push(c);
        }
    });
    let mailer = SmtpMailer::new(&Config {
        url: format!("smtp://127.0.0.1:{port}"),
        password: String::new(),
        timeout: Duration::from_millis(300),
    })
    .unwrap();
    let err = mailer.send(&msg()).await.unwrap_err().to_string();
    assert!(err.contains("\nTry: "), "{err}");
    hold.abort();
    // No server: the send fails with a Try: line.
    let closed = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = closed.local_addr().unwrap().port();
    drop(closed);
    let mailer = SmtpMailer::new(&Config {
        url: format!("smtp://127.0.0.1:{port}"),
        password: String::new(),
        timeout: Duration::from_secs(2),
    })
    .unwrap();
    let err = mailer.send(&msg()).await.unwrap_err().to_string();
    assert!(err.contains("\nTry: "), "{err}");
}

/// An app mailer, written with the re-exported `async_trait`.
struct Counting(Arc<Mutex<usize>>);

#[async_trait]
impl Mailer for Counting {
    async fn send(&self, m: &Message) -> anyhow::Result<()> {
        m.validate()?;
        *self.0.lock().unwrap() += 1;
        Ok(())
    }
}

#[tokio::test]
async fn mail_app_mailer_through_arc() {
    let n = Arc::new(Mutex::new(0));
    let m: Arc<dyn Mailer> = Arc::new(Counting(n.clone()));
    m.send(&msg()).await.unwrap();
    // Arc<dyn Mailer> is a Mailer too.
    let wrapped = Arc::new(m);
    wrapped.send(&msg()).await.unwrap();
    assert_eq!(*n.lock().unwrap(), 2);
}

#[test]
fn mail_message_json_equals_go() {
    let m = msg();
    assert_eq!(
        serde_json::to_string(&m).unwrap(),
        r#"{"from":"app@example.com","to":"you@example.com","subject":"Hi","text":"Hello"}"#
    );
    let back: Message = serde_json::from_str(
        r#"{"from":"a@example.com","to":"b@example.com","subject":"S","text":"T","html":"<p>H</p>"}"#,
    )
    .unwrap();
    assert_eq!(back.html, "<p>H</p>");
}
