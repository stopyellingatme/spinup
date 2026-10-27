//! The mail transport and the sender address of the application.

use std::sync::Arc;

use kit::config::{Base, Env, FromEnv};
use kit::mail::{Mailer, Message, async_trait};

/// Renders typed mailers and sends their messages through the transport or job queue.
#[derive(Clone)]
pub struct Mailers {
    /// The mailer. Tests can replace it with `kit::mail::Capture`.
    pub mailer: Arc<dyn Mailer>,
    /// The sender address from `MAIL_FROM`.
    pub from: String,
    #[cfg(feature = "jobs")]
    pub(super) transport: Arc<dyn Mailer>,
}

struct Config {
    base: Base,
    smtp: kit::mail::Config,
    from: String,
}

impl FromEnv for Config {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self {
            base: Base::from_env(env),
            smtp: kit::mail::Config::from_env(env),
            from: env.value("MAIL_FROM", "noreply@example.com".into()),
        }
    }
}

impl Mailers {
    /// Loads SMTP settings and `MAIL_FROM`. Development uses Mailpit when `SMTP_URL` is empty.
    pub fn new(pool: &kit::db::PgPool) -> anyhow::Result<Mailers> {
        Self::configured(pool, kit::config::load()?)
    }

    fn configured(pool: &kit::db::PgPool, mut config: Config) -> anyhow::Result<Self> {
        if config.base.is_dev() && config.smtp.url.is_empty() {
            config.smtp.url = "smtp://localhost:1025".into();
        }
        let transport = kit::mail::new(&config.smtp)?;
        #[cfg(feature = "jobs")]
        let mailer = Arc::new(super::worker::Queued::new(pool.clone()));
        #[cfg(not(feature = "jobs"))]
        let mailer = {
            let _ = pool;
            transport.clone()
        };
        Ok(Self {
            mailer,
            from: config.from,
            #[cfg(feature = "jobs")]
            transport,
        })
    }

    /// Sends a message through the configured transport or job queue.
    pub async fn send(&self, message: &Message) -> anyhow::Result<()> {
        self.mailer.send(message).await
    }
}

#[async_trait]
impl Mailer for Mailers {
    async fn send(&self, message: &Message) -> anyhow::Result<()> {
        Mailers::send(self, message).await
    }
}
