//! The SMTP mailer (Go `kit/mail/smtp.go`) with lettre and rustls.

use std::sync::{Arc, OnceLock};
use std::time::Duration;

use lettre::message::{MultiPart, SinglePart, header::ContentType};
use lettre::transport::smtp::AsyncSmtpTransportBuilder;
use lettre::transport::smtp::authentication::{Credentials, Mechanism};
use lettre::transport::smtp::client::{Tls, TlsParameters};
use lettre::{AsyncSmtpTransport, AsyncTransport, Tokio1Executor};

use super::{Config, DEFAULT_TIMEOUT, Mailer, Message, async_trait};

/// The error of every bad SMTP setting. It never holds the URL or the password.
const BAD_CONFIG: &str = "mail: cannot configure SMTP: the URL or timeout is invalid\nTry: use smtp://host:1025, smtp+starttls://user@host:587, or smtps://user@host:465 and SMTP_PASSWORD";

/// Sends mail through an SMTP server (Go `smtpMailer`). Clones share one connection pool.
///
/// The pool starts at the first send, inside the tokio runtime, because lettre spawns
/// its pool tasks. So [`SmtpMailer::new`] also works outside a runtime. Drop a mailer
/// that has sent mail inside the runtime.
#[derive(Clone)]
pub struct SmtpMailer {
    builder: AsyncSmtpTransportBuilder,
    transport: Arc<OnceLock<AsyncSmtpTransport<Tokio1Executor>>>,
    timeout: Duration,
}

impl std::fmt::Debug for SmtpMailer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SmtpMailer")
            .field("timeout", &self.timeout)
            .finish_non_exhaustive()
    }
}

/// How the connection uses TLS.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Policy {
    /// Plain text only (local Mailpit).
    None,
    /// STARTTLS is required.
    StartTls,
    /// TLS from the start (`smtps`).
    Implicit,
}

/// The parts of an SMTP URL.
#[derive(Debug, PartialEq, Eq)]
struct SmtpUrl {
    host: String,
    port: u16,
    user: Option<String>,
    policy: Policy,
}

fn percent_decode(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let hex = s.get(i + 1..i + 3)?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

/// Parses `raw` with the rules of Go `mail.New`. `None` for every bad URL.
fn parse_url(raw: &str) -> Option<SmtpUrl> {
    let (scheme, rest) = raw.split_once("://")?;
    let scheme = scheme.to_ascii_lowercase();
    if rest.contains('#') {
        return None;
    }
    let (authority, query) = match rest.split_once('?') {
        Some((a, q)) => (a, q),
        None => (rest, ""),
    };
    // A path (even "/") is not allowed.
    if authority.contains('/') {
        return None;
    }
    let (userinfo, hostport) = match authority.rsplit_once('@') {
        Some((u, h)) => (Some(u), h),
        None => (None, authority),
    };
    let user = match userinfo {
        // A password in the URL is refused: it belongs in SMTP_PASSWORD.
        Some(u) if u.contains(':') => return None,
        Some(u) => Some(percent_decode(u)?).filter(|u| !u.is_empty()),
        None => None,
    };
    let (host, port) = if let Some(v6) = hostport.strip_prefix('[') {
        let (h, after) = v6.split_once(']')?;
        match after {
            "" => (h, ""),
            p => (h, p.strip_prefix(':')?),
        }
    } else {
        match hostport.rsplit_once(':') {
            Some((h, p)) => (h, p),
            None => (hostport, ""),
        }
    };
    if host.is_empty() || host.contains(['%', '[', ']', ' ']) {
        return None;
    }
    let (mut port_default, mut policy, implicit) = match scheme.as_str() {
        "smtp" if user.is_some() => (587, Policy::StartTls, false),
        "smtp" => (25, Policy::None, false),
        "smtp+starttls" => (587, Policy::StartTls, false),
        "smtps" => (465, Policy::Implicit, true),
        _ => return None,
    };
    let pairs: Vec<(String, String)> = form_urlencoded::parse(query.as_bytes())
        .into_owned()
        .collect();
    if pairs.iter().any(|(k, _)| k != "tls") || pairs.len() > 1 {
        return None;
    }
    match pairs.first().map(|(_, v)| v.as_str()) {
        None | Some("") => {}
        Some("starttls") if !implicit => policy = Policy::StartTls,
        Some("none") if scheme == "smtp" && user.is_none() => policy = Policy::None,
        Some(_) => return None,
    }
    if !port.is_empty() {
        port_default = port.parse::<u16>().ok().filter(|p| *p >= 1)?;
    }
    Some(SmtpUrl {
        host: host.to_owned(),
        port: port_default,
        user,
        policy,
    })
}

impl SmtpMailer {
    /// Returns a mailer for `config` (Go `mail.New` with a URL). See [`super::new`] for
    /// the URL forms. It connects to no server.
    ///
    /// # Errors
    ///
    /// An invalid URL, a password in the URL, or a bad port. The text has a `Try:` line
    /// and never holds the password.
    pub fn new(config: &Config) -> anyhow::Result<Self> {
        let u = parse_url(&config.url).ok_or_else(|| anyhow::anyhow!(BAD_CONFIG))?;
        let timeout = if config.timeout.is_zero() {
            DEFAULT_TIMEOUT
        } else {
            config.timeout
        };
        let tls = match u.policy {
            Policy::None => Tls::None,
            Policy::StartTls => Tls::Required(
                TlsParameters::new(u.host.clone()).map_err(|_| anyhow::anyhow!(BAD_CONFIG))?,
            ),
            Policy::Implicit => Tls::Wrapper(
                TlsParameters::new(u.host.clone()).map_err(|_| anyhow::anyhow!(BAD_CONFIG))?,
            ),
        };
        let mut b = AsyncSmtpTransport::<Tokio1Executor>::builder_dangerous(u.host)
            .port(u.port)
            .timeout(Some(timeout))
            .tls(tls);
        if let Some(user) = u.user {
            b = b
                .credentials(Credentials::new(user, config.password.clone()))
                .authentication(vec![Mechanism::Plain]);
        }
        Ok(Self {
            builder: b,
            transport: Arc::default(),
            timeout,
        })
    }
}

/// Builds the lettre message: text only, or text and HTML as alternatives.
fn build(m: &Message) -> anyhow::Result<lettre::Message> {
    let bad = |what: &str| {
        anyhow::anyhow!("mail: cannot set {what}\nTry: provide one valid sender and recipient")
    };
    let builder = lettre::Message::builder()
        .from(m.from.parse().map_err(|_| bad("the sender"))?)
        .to(m.to.parse().map_err(|_| bad("the recipient"))?)
        .subject(m.subject.clone());
    let text = SinglePart::builder()
        .header(ContentType::TEXT_PLAIN)
        .body(m.text.clone());
    let built = if m.html.is_empty() {
        builder.singlepart(text)
    } else {
        builder.multipart(
            MultiPart::alternative().singlepart(text).singlepart(
                SinglePart::builder()
                    .header(ContentType::TEXT_HTML)
                    .body(m.html.clone()),
            ),
        )
    };
    built.map_err(|_| {
        anyhow::anyhow!("mail: cannot build the message\nTry: check the addresses and the subject")
    })
}

#[async_trait]
impl Mailer for SmtpMailer {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        msg.validate()?;
        let email = build(msg)?;
        let transport = self.transport.get_or_init(|| self.builder.clone().build());
        match tokio::time::timeout(self.timeout, transport.send(email)).await {
            Ok(Ok(_)) => Ok(()),
            // SMTP answers can hold credentials or message content. Do not show them.
            Ok(Err(_)) => anyhow::bail!(
                "mail: SMTP delivery failed\nTry: check SMTP_URL, SMTP_PASSWORD, TLS, and SMTP server availability"
            ),
            Err(_) => anyhow::bail!(
                "mail: SMTP delivery timed out\nTry: retry delivery or check SMTP_TIMEOUT"
            ),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mail_smtp_url_forms() {
        let u = parse_url("smtp://localhost:1025").unwrap();
        assert_eq!(
            u,
            SmtpUrl {
                host: "localhost".into(),
                port: 1025,
                user: None,
                policy: Policy::None
            }
        );
        let u = parse_url("smtp://user%40example.com@mail.example.com").unwrap();
        assert_eq!(u.user.as_deref(), Some("user@example.com"));
        assert_eq!((u.port, u.policy), (587, Policy::StartTls));
        let u = parse_url("smtps://user@example.com").unwrap();
        assert_eq!((u.port, u.policy), (465, Policy::Implicit));
        let u = parse_url("smtp://[::1]:2525?tls=starttls").unwrap();
        assert_eq!(
            (u.host.as_str(), u.port, u.policy),
            ("::1", 2525, Policy::StartTls)
        );
        assert_eq!(
            parse_url("smtp://localhost?tls=none").unwrap().policy,
            Policy::None
        );
        for bad in [
            "smtp://localhost/",
            "smtp://localhost#x",
            "smtp://:25",
            "smtp://localhost:70000",
            "smtp://localhost?tls=none&tls=none",
            "smtp://localhost?other=1",
            "smtps://localhost?tls=starttls",
            "smtp+starttls://localhost?tls=none",
            "localhost:25",
        ] {
            assert!(parse_url(bad).is_none(), "{bad}");
        }
    }
}
