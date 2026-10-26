//! The `mail.send` job and its SMTP delivery handler.

use kit::jobs::{Args, Client, InsertOpts, Job, Worker};
use kit::mail::{Mailer, Message, async_trait};

use super::Mailers;

// The queue holds the rendered message. Treat its storage as sensitive data.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
struct SendArgs {
    message: Message,
}

impl Args for SendArgs {
    const KIND: &'static str = "mail.send";
}

pub(super) struct Queued {
    client: Client,
}

impl Queued {
    pub(super) fn new(pool: kit::db::PgPool) -> Self {
        Self {
            client: Client::new(pool),
        }
    }
}

#[async_trait]
impl Mailer for Queued {
    async fn send(&self, message: &Message) -> anyhow::Result<()> {
        message.validate()?;
        self.client
            .insert(
                &SendArgs {
                    message: message.clone(),
                },
                &InsertOpts {
                    // The standard worker consumes the default queue for mail and app jobs.
                    max_attempts: Some(10),
                    ..InsertOpts::default()
                },
            )
            .await?;
        Ok(())
    }
}

impl Mailers {
    /// Registers SMTP delivery. A delivery error makes the worker retry the job.
    pub fn register_jobs(&self, worker: &mut Worker) -> anyhow::Result<()> {
        let transport = self.transport.clone();
        worker.register(move |job: Job<SendArgs>| {
            let transport = transport.clone();
            async move { transport.send(&job.args.message).await }
        });
        Ok(())
    }
}
