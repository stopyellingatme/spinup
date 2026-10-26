//! The log mailer (Go `mail.Log`).

use super::{Mailer, Message, async_trait};

/// Logs a delivery event without the addresses, the subject, or the body, and sends
/// nothing (Go `mail.Log`). [`super::new`] selects it when `SMTP_URL` is empty, so a
/// password reset token never reaches the log.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Log;

#[async_trait]
impl Mailer for Log {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        msg.validate()?;
        tracing::info!(transport = "log", "mail: delivery logged");
        Ok(())
    }
}
