//! The capture mailer for tests (Go `mail.Capture`).

use std::sync::{Arc, Mutex};

use super::{Mailer, Message, async_trait};

/// Keeps a copy of each valid message, in delivery order (Go `mail.Capture`). Clones
/// share one list, so a test keeps one clone and gives another to the code under test.
#[derive(Debug, Clone, Default)]
pub struct Capture(Arc<Mutex<Vec<Message>>>);

impl Capture {
    /// Returns an empty capture.
    pub fn new() -> Self {
        Self::default()
    }

    /// A copy of the captured messages, in delivery order.
    pub fn messages(&self) -> Vec<Message> {
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone()
    }
}

#[async_trait]
impl Mailer for Capture {
    async fn send(&self, msg: &Message) -> anyhow::Result<()> {
        msg.validate()?;
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .push(msg.clone());
        Ok(())
    }
}
