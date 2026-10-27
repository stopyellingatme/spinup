//! Serve with graceful shutdown (Go `App.serve` and `App.serveListener`).

use std::future::Future;
use std::net::SocketAddr;

use tokio::net::TcpListener;
use tokio::sync::oneshot;
use tokio::time::Instant;

use super::App;
use crate::config::goduration;

impl App {
    /// Binds [`App::listen_addr`] and serves until `shutdown` completes (see
    /// [`App::serve_on`]).
    pub async fn serve(
        self,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        let addr = self.listen_addr();
        let listener = TcpListener::bind(&addr).await.map_err(|e| {
            anyhow::anyhow!(
                "cannot listen on {addr}: {e}\nTry: select an available listen address and restart"
            )
        })?;
        self.serve_on(listener, shutdown).await
    }

    /// Serves HTTP on `listener` until `shutdown` completes. Then it stops new connections
    /// and waits for open requests, at most the shutdown timeout. At the end it runs the
    /// shutdown hooks in reverse order, with the rest of the same deadline. It reports every
    /// failure together, with a `Try:` line.
    ///
    /// The router gets `ConnectInfo<SocketAddr>`: the peer address of each connection.
    pub async fn serve_on(
        mut self,
        listener: TcpListener,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        let router = self.router();
        let timeout = self.shutdown_timeout();
        let hooks = std::mem::take(&mut self.shutdown);
        let local = listener
            .local_addr()
            .map(|a| a.to_string())
            .unwrap_or_default();
        tracing::info!(addr = %local, "HTTP server listening");
        let (started_tx, started_rx) = oneshot::channel::<Instant>();
        let server = axum::serve(
            listener,
            router.into_make_service_with_connect_info::<SocketAddr>(),
        )
        .with_graceful_shutdown(async move {
            shutdown.await;
            let _ = started_tx.send(Instant::now());
        })
        .into_future();
        tokio::pin!(server);
        let mut problems: Vec<String> = Vec::new();
        let deadline = tokio::select! {
            result = &mut server => {
                if let Err(e) = result {
                    problems.push(format!("HTTP server failed: {e}"));
                }
                Instant::now() + timeout
            }
            started = started_rx => {
                let deadline = started.unwrap_or_else(|_| Instant::now()) + timeout;
                match tokio::time::timeout_at(deadline, &mut server).await {
                    Ok(Ok(())) => {}
                    Ok(Err(e)) => problems.push(format!("HTTP server failed: {e}")),
                    Err(_) => problems.push(format!(
                        "HTTP shutdown did not finish in {}",
                        goduration::format_std(timeout)
                    )),
                }
                deadline
            }
        };
        for hook in hooks.into_iter().rev() {
            match tokio::time::timeout_at(deadline, hook()).await {
                Ok(Ok(())) => {}
                Ok(Err(e)) => problems.push(format!("shutdown hook failed: {e:#}")),
                Err(_) => problems.push(
                    "shutdown hook failed: it did not finish before the shutdown deadline".into(),
                ),
            }
        }
        tracing::info!("HTTP server stopped");
        if problems.is_empty() {
            Ok(())
        } else {
            anyhow::bail!(
                "{}\nTry: correct the reported server or shutdown failure and restart",
                problems.join("\n")
            )
        }
    }
}

/// Completes on SIGINT or SIGTERM on Unix, and on Ctrl-C on Windows.
pub async fn shutdown_signal() {
    let ctrl_c = async {
        if tokio::signal::ctrl_c().await.is_err() {
            std::future::pending::<()>().await;
        }
    };
    #[cfg(unix)]
    let term = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut s) => {
                s.recv().await;
            }
            Err(_) => std::future::pending::<()>().await,
        }
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();
    tokio::select! {
        () = ctrl_c => {},
        () = term => {},
    }
}
