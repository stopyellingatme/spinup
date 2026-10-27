//! Mail through SMTP, a log mailer, a capture mailer for tests, and minijinja templates
//! (Go `kit/mail`).
//!
//! - [`Message`] is one email with a required plain-text part and an optional HTML part.
//! - [`Mailer`] sends messages. Apps implement it with the re-exported
//!   [`async_trait`](macro@async_trait), so they need no `async-trait` dependency (ADR 0021
//!   §3).
//! - [`new`] returns an [`SmtpMailer`] for `SMTP_URL`, or the [`Log`] mailer when the URL
//!   is empty. [`Config`] reads `SMTP_URL`, `SMTP_PASSWORD` (or `SMTP_PASSWORD_FILE`), and
//!   `SMTP_TIMEOUT` through [`crate::config`].
//! - [`Capture`] keeps the messages in memory for tests.
//! - [`render`] renders the templates `<name>.subject.tmpl`, `<name>.txt.tmpl`, and
//!   `<name>.html.tmpl` with minijinja. Only the HTML part escapes values. Apps keep their
//!   templates in `api/templates/mail/`.
//!
//! Every mailer checks a message before it sends it: one sender, one recipient, a
//! single-line subject, and a plain-text body. Errors never hold a password, a message
//! body, or an SMTP answer.
//!
//! ```
//! # tokio::runtime::Runtime::new().unwrap().block_on(async {
//! use spinup_kit::mail::{Capture, Mailer, Message};
//!
//! let capture = Capture::new();
//! let msg = Message {
//!     from: "app@example.com".into(),
//!     to: "you@example.com".into(),
//!     subject: "Hello".into(),
//!     text: "Hello from the app.".into(),
//!     ..Message::default()
//! };
//! capture.send(&msg).await.unwrap();
//! assert_eq!(capture.messages(), vec![msg]);
//! # });
//! ```

mod capture;
mod log;
mod smtp;
#[cfg(any(test, feature = "testkit"))]
pub mod stub;
mod templates;

use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::config::{Env, FromEnv};

pub use self::capture::Capture;
pub use self::log::Log;
pub use self::smtp::SmtpMailer;
pub use self::templates::{Templates, render};
pub use async_trait::async_trait;

/// The limit of one SMTP send when `SMTP_TIMEOUT` is not set (Go `default:"10s"`).
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(10);

/// An email with a required plain-text part and an optional HTML part (Go `mail.Message`).
/// The JSON form equals the Go form, so a queued message survives the move between the
/// two kits.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Message {
    /// The sender address, such as `app@example.com` or `App <app@example.com>`.
    pub from: String,
    /// The recipient address.
    pub to: String,
    /// The subject: one line of text.
    pub subject: String,
    /// The plain-text body.
    pub text: String,
    /// The HTML alternative. Empty means no HTML part.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub html: String,
}

impl Message {
    /// Checks the message as every mailer does before it sends: a single-line, non-blank
    /// subject, a non-blank text body, and one valid sender and one valid recipient
    /// without line breaks.
    ///
    /// # Errors
    ///
    /// An error with a `Try:` line. It names no address and no content.
    pub fn validate(&self) -> anyhow::Result<()> {
        if self.subject.contains(['\r', '\n'])
            || self.subject.trim().is_empty()
            || self.text.trim().is_empty()
        {
            anyhow::bail!(
                "mail: cannot send: a single-line subject and a plain-text body are required\nTry: provide a subject and a txt template"
            );
        }
        for addr in [&self.from, &self.to] {
            if addr.contains(['\r', '\n']) {
                anyhow::bail!(
                    "mail: cannot send: an address contains a line break\nTry: provide one valid sender and recipient"
                );
            }
            if addr.parse::<lettre::message::Mailbox>().is_err() {
                anyhow::bail!(
                    "mail: cannot send: a sender or recipient address is invalid\nTry: provide one valid sender and recipient"
                );
            }
        }
        Ok(())
    }
}

/// Delivers an email (Go `mail.Mailer`). Implementations must allow calls from many tasks
/// at the same time.
///
/// ```
/// use spinup_kit::mail::{Mailer, Message, async_trait};
///
/// struct Discard;
///
/// #[async_trait]
/// impl Mailer for Discard {
///     async fn send(&self, msg: &Message) -> anyhow::Result<()> {
///         msg.validate()
///     }
/// }
/// ```
#[async_trait]
pub trait Mailer: Send + Sync + 'static {
    /// Sends one message.
    ///
    /// # Errors
    ///
    /// An invalid message or a failed delivery. The text ends with a `Try:` line.
    async fn send(&self, msg: &Message) -> anyhow::Result<()>;
}

#[async_trait]
impl<T: Mailer + ?Sized> Mailer for Arc<T> {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        (**self).send(msg).await
    }
}

/// The SMTP settings (Go `mail.Config`). Load it with [`crate::config::load`], so
/// `SMTP_PASSWORD_FILE` works.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct Config {
    /// `SMTP_URL`: `smtp://`, `smtp+starttls://`, or `smtps://`. An empty URL selects
    /// the [`Log`] mailer.
    pub url: String,
    /// `SMTP_PASSWORD` or `SMTP_PASSWORD_FILE`. The URL never holds the password.
    pub password: String,
    /// `SMTP_TIMEOUT`: the limit of each send, with the connection and the SMTP commands.
    /// The default is 10 seconds. Zero also gives the default.
    pub timeout: Duration,
}

impl std::fmt::Debug for Config {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Config")
            .field("url", &self.url)
            .field(
                "password",
                &if self.password.is_empty() { "" } else { "***" },
            )
            .field("timeout", &self.timeout)
            .finish()
    }
}

impl FromEnv for Config {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self {
            url: env.value("SMTP_URL", String::new()),
            password: env.value("SMTP_PASSWORD", String::new()),
            timeout: env.value("SMTP_TIMEOUT", DEFAULT_TIMEOUT),
        }
    }
}

/// Returns an [`SmtpMailer`] for `config`, or the [`Log`] mailer when `config.url` is
/// empty (Go `mail.New`).
///
/// - `smtp://host:1025` sends in plain text, for a local Mailpit. With a user name, the
///   server must offer STARTTLS, and the default port is 587.
/// - `smtp+starttls://user@host:587` requires STARTTLS.
/// - `smtps://user@host:465` uses TLS from the start, without a fallback.
/// - The query `tls=starttls` requires STARTTLS. `tls=none` turns TLS off for `smtp://`
///   without a user name.
///
/// # Errors
///
/// An invalid URL, a password in the URL, or a bad port. The text has a `Try:` line and
/// never holds the password.
pub fn new(config: &Config) -> anyhow::Result<Arc<dyn Mailer>> {
    if config.url.is_empty() {
        return Ok(Arc::new(Log));
    }
    Ok(Arc::new(SmtpMailer::new(config)?))
}
