//! The OTel log bridge with redaction (Go `telemetry.SlogHandler` with `otelslog`).
//!
//! `opentelemetry-appender-tracing` has no hook for field values, so the kit has its own
//! small bridge. It writes each field through [`logx::redact`] and masks the URL passwords
//! of the message, as the local log does.

use std::fmt;

use opentelemetry::logs::{AnyValue, LogRecord as _, Logger as _, LoggerProvider as _, Severity};
use opentelemetry_sdk::logs::{SdkLogger, SdkLoggerProvider};
use tracing::field::{Field, Visit};
use tracing::span::{Attributes, Id, Record};
use tracing::{Event, Level, Subscriber};
use tracing_subscriber::Layer;
use tracing_subscriber::layer::Context;
use tracing_subscriber::registry::LookupSpan;

use super::SCOPE;
use crate::logx::{self, Value};

/// Returns the log bridge of `provider`: a layer that sends each event to the OTel log
/// export with redacted fields. The fields of the open spans come first, then the fields
/// of the event. The record gets the trace context of the current span.
///
/// The layer has no filter. [`Telemetry::layer`](super::Telemetry::layer) filters it to
/// `LOG_LEVEL`.
pub fn log_bridge(provider: &SdkLoggerProvider) -> LogBridge {
    LogBridge {
        logger: provider.logger(SCOPE),
    }
}

/// The layer of [`log_bridge`].
pub struct LogBridge {
    logger: SdkLogger,
}

impl fmt::Debug for LogBridge {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("LogBridge")
    }
}

/// The fields of a span, kept in the span extensions.
struct BridgeFields(Vec<(String, Value)>);

/// Collects the fields of a span or an event.
#[derive(Default)]
struct Fields {
    message: Option<String>,
    values: Vec<(String, Value)>,
}

impl Fields {
    fn put(&mut self, field: &Field, value: Value) {
        if field.name() == "message" {
            self.message = Some(value.to_string());
            return;
        }
        let name = field.name().strip_prefix("r#").unwrap_or(field.name());
        self.values.push((name.to_string(), value));
    }
}

impl Visit for Fields {
    fn record_str(&mut self, field: &Field, value: &str) {
        self.put(field, Value::Str(value.to_string()));
    }
    fn record_bool(&mut self, field: &Field, value: bool) {
        self.put(field, Value::Bool(value));
    }
    fn record_i64(&mut self, field: &Field, value: i64) {
        self.put(field, Value::I64(value));
    }
    fn record_u64(&mut self, field: &Field, value: u64) {
        self.put(field, Value::U64(value));
    }
    fn record_f64(&mut self, field: &Field, value: f64) {
        self.put(field, Value::F64(value));
    }
    fn record_error(&mut self, field: &Field, value: &(dyn std::error::Error + 'static)) {
        let mut text = value.to_string();
        let mut source = value.source();
        while let Some(s) = source {
            text.push_str(": ");
            text.push_str(&s.to_string());
            source = s.source();
        }
        self.put(field, Value::Str(text));
    }
    fn record_debug(&mut self, field: &Field, value: &dyn fmt::Debug) {
        self.put(field, Value::Str(format!("{value:?}")));
    }
}

fn severity(level: Level) -> Severity {
    match level {
        Level::TRACE => Severity::Trace,
        Level::DEBUG => Severity::Debug,
        Level::INFO => Severity::Info,
        Level::WARN => Severity::Warn,
        Level::ERROR => Severity::Error,
    }
}

fn any_value(value: Value) -> AnyValue {
    match value {
        Value::Str(s) => AnyValue::from(s),
        Value::Bool(b) => AnyValue::from(b),
        Value::I64(n) => AnyValue::from(n),
        Value::U64(n) => match i64::try_from(n) {
            Ok(n) => AnyValue::from(n),
            Err(_) => AnyValue::from(n.to_string()),
        },
        Value::F64(n) => AnyValue::from(n),
    }
}

impl<S> Layer<S> for LogBridge
where
    S: Subscriber + for<'a> LookupSpan<'a>,
{
    fn on_new_span(&self, attrs: &Attributes<'_>, id: &Id, ctx: Context<'_, S>) {
        let Some(span) = ctx.span(id) else { return };
        let mut fields = Fields::default();
        attrs.record(&mut fields);
        span.extensions_mut().insert(BridgeFields(fields.values));
    }

    fn on_record(&self, id: &Id, values: &Record<'_>, ctx: Context<'_, S>) {
        let Some(span) = ctx.span(id) else { return };
        let mut fields = Fields::default();
        values.record(&mut fields);
        let mut ext = span.extensions_mut();
        if let Some(BridgeFields(old)) = ext.get_mut::<BridgeFields>() {
            for (key, value) in fields.values {
                match old.iter_mut().find(|(k, _)| *k == key) {
                    Some(slot) => slot.1 = value,
                    None => old.push((key, value)),
                }
            }
        }
    }

    fn on_event(&self, event: &Event<'_>, ctx: Context<'_, S>) {
        let meta = event.metadata();
        let sev = severity(*meta.level());
        if !self
            .logger
            .event_enabled(sev, meta.target(), Some(meta.name()))
        {
            return;
        }
        let mut fields = Fields::default();
        event.record(&mut fields);
        let mut all: Vec<(String, Value)> = Vec::new();
        if let Some(scope) = ctx.event_scope(event) {
            for span in scope.from_root() {
                if let Some(BridgeFields(values)) = span.extensions().get::<BridgeFields>() {
                    all.extend(values.iter().cloned());
                }
            }
        }
        all.extend(fields.values);

        let mut record = self.logger.create_log_record();
        record.set_target(meta.target().to_owned());
        record.set_event_name(meta.name());
        record.set_severity_number(sev);
        record.set_severity_text(meta.level().as_str());
        record.set_timestamp(std::time::SystemTime::now());
        record.set_body(AnyValue::from(logx::mask_urls(
            &fields.message.unwrap_or_default(),
        )));
        for (key, value) in all {
            let value = logx::redact(&key, value);
            record.add_attribute(key, any_value(value));
        }
        self.logger.emit(record);
    }
}
