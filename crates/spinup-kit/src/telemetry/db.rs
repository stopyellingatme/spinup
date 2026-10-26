//! The database span layer (Go `telemetry.ConfigurePool` with `otelpgx`).
//!
//! sqlx has no OpenTelemetry hook. After each statement it writes one `tracing` event with
//! the target `sqlx::query` and the fields `summary`, `db.statement`, `rows_affected`,
//! `rows_returned`, and `elapsed_secs`. [`db_layer`] turns that event into a client span
//! that ends now and starts `elapsed_secs` earlier, under the current OTel context.

use std::fmt;
use std::time::{Duration, SystemTime};

use opentelemetry::trace::{Span as _, SpanKind, Tracer as _};
use opentelemetry::{Context as OtelContext, KeyValue, global};
use tracing::field::{Field, Visit};
use tracing::{Event, Metadata, Subscriber};
use tracing_subscriber::Layer;
use tracing_subscriber::filter::filter_fn;
use tracing_subscriber::layer::Context;
use tracing_subscriber::registry::LookupSpan;

use super::SCOPE;

/// The target of the statement events of sqlx.
pub const SQLX_TARGET: &str = "sqlx::query";

/// Returns the database span layer: each `sqlx::query` event becomes a client span named
/// after the SQL operation (`SELECT`, `INSERT`, ...). The span has `db.system.name`,
/// `db.operation.name`, `db.response.returned_rows`, and `db.response.affected_rows`. It
/// has no SQL text and no parameters, to protect app data.
///
/// The layer uses the global tracer provider, so it records nothing before the telemetry
/// starts. [`Telemetry::layer`](super::Telemetry::layer) includes it.
pub fn db_layer<S>() -> impl Layer<S> + Send + Sync + 'static
where
    S: Subscriber + for<'a> LookupSpan<'a>,
{
    // Not `is_event()`: sqlx first asks `tracing::enabled!`, whose metadata has the kind
    // `HINT`. A filter that rejects it stops sqlx from emitting the event at all.
    DbLayer(()).with_filter(filter_fn(|meta: &Metadata<'_>| {
        !meta.is_span() && meta.target() == SQLX_TARGET
    }))
}

/// The layer of [`db_layer`].
#[derive(Debug, Clone, Copy)]
pub struct DbLayer(());

/// The fields of one statement event.
#[derive(Default)]
struct Statement {
    summary: String,
    rows_returned: Option<u64>,
    rows_affected: Option<u64>,
    elapsed_secs: f64,
}

impl Visit for Statement {
    fn record_str(&mut self, field: &Field, value: &str) {
        if field.name() == "summary" {
            self.summary = value.to_owned();
        }
    }
    fn record_u64(&mut self, field: &Field, value: u64) {
        match field.name() {
            "rows_returned" => self.rows_returned = Some(value),
            "rows_affected" => self.rows_affected = Some(value),
            _ => {}
        }
    }
    fn record_i64(&mut self, field: &Field, value: i64) {
        self.record_u64(field, u64::try_from(value).unwrap_or(0));
    }
    fn record_f64(&mut self, field: &Field, value: f64) {
        if field.name() == "elapsed_secs" {
            self.elapsed_secs = value;
        }
    }
    fn record_debug(&mut self, field: &Field, value: &dyn fmt::Debug) {
        if field.name() == "summary" {
            self.summary = format!("{value:?}");
        }
    }
}

/// The SQL operation: the first word of the summary in upper case, or `None` when it is not
/// a word.
pub(crate) fn operation(summary: &str) -> Option<String> {
    let word = summary.split_whitespace().next()?;
    word.chars()
        .all(|c| c.is_ascii_alphabetic())
        .then(|| word.to_ascii_uppercase())
}

impl<S: Subscriber> Layer<S> for DbLayer {
    fn on_event(&self, event: &Event<'_>, _ctx: Context<'_, S>) {
        let mut stmt = Statement::default();
        event.record(&mut stmt);
        let end = SystemTime::now();
        let elapsed = Duration::try_from_secs_f64(stmt.elapsed_secs).unwrap_or_default();
        let start = end.checked_sub(elapsed).unwrap_or(end);
        let op = operation(&stmt.summary);
        let mut attrs = vec![KeyValue::new("db.system.name", "postgresql")];
        if let Some(op) = &op {
            attrs.push(KeyValue::new("db.operation.name", op.clone()));
        }
        if let Some(n) = stmt.rows_returned {
            attrs.push(KeyValue::new(
                "db.response.returned_rows",
                i64::try_from(n).unwrap_or(i64::MAX),
            ));
        }
        if let Some(n) = stmt.rows_affected {
            attrs.push(KeyValue::new(
                "db.response.affected_rows",
                i64::try_from(n).unwrap_or(i64::MAX),
            ));
        }
        let tracer = global::tracer(SCOPE);
        let parent = OtelContext::current();
        let mut span = tracer
            .span_builder(op.unwrap_or_else(|| "postgresql".to_string()))
            .with_kind(SpanKind::Client)
            .with_start_time(start)
            .with_attributes(attrs)
            .start_with_context(&tracer, &parent);
        span.end_with_timestamp(end);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn telemetry_db_operation() {
        assert_eq!(operation("select id from todos").as_deref(), Some("SELECT"));
        assert_eq!(operation("INSERT INTO todos …").as_deref(), Some("INSERT"));
        assert_eq!(operation("").as_deref(), None);
        assert_eq!(operation("/* x */ SELECT 1").as_deref(), None);
    }
}
