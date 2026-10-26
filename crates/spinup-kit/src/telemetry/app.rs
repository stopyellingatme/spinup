//! The app glue (Go `kit/app/telemetry.go`).

use axum::middleware;

use super::{HttpMetrics, Telemetry, http_layer};
use crate::app::App;

impl App {
    /// Adds the telemetry of `telemetry` to the app (Go `telemetryHandler` and
    /// `telemetryLogger`). When the export runs, the HTTP layer of
    /// [`telemetry::http_layer`](crate::telemetry::http_layer) goes between the request ID
    /// layer and the access log layer, so the access log line gets the trace ID. An exit
    /// hook flushes and stops the export after any command or serve. When the export is off,
    /// the app does not change.
    #[must_use]
    pub fn with_telemetry(mut self, telemetry: &Telemetry) -> Self {
        if !telemetry.enabled() {
            return self;
        }
        let metrics = HttpMetrics::global();
        self.push_outer_layer(move |router| {
            router.layer(middleware::from_fn_with_state(metrics.clone(), http_layer))
        });
        let telemetry = telemetry.clone();
        self.on_exit(move || async move { telemetry.shutdown().await });
        self
    }
}
