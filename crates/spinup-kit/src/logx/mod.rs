//! The log setup and the redactor (Go `kit/logx`).
//!
//! The kit logs through `tracing`. [`layer`] is a `tracing-subscriber` layer that writes one
//! line for each event in the Go `slog` form:
//!
//! ```text
//! {"time":"2026-10-25T17:30:51.123456Z","level":"INFO","msg":"HTTP request","method":"GET",...}
//! ```
//!
//! The fields of the open spans come first (outermost span first), then the fields of the
//! event. Every field goes through the current [`Redactor`] ([`set_redactor`]). [`init`]
//! installs the layer as the global subscriber. The telemetry module stacks [`layer`] with
//! its OpenTelemetry layers instead.

mod redact;

use std::fmt::{self, Write as _};
use std::io::{self, Write as _};

use tracing::field::{Field, Visit};
use tracing::span::{Attributes, Id, Record};
use tracing::{Event, Level, Subscriber};
use tracing_subscriber::Layer;
use tracing_subscriber::filter::LevelFilter;
use tracing_subscriber::fmt::MakeWriter;
use tracing_subscriber::layer::{Context, SubscriberExt};
use tracing_subscriber::registry::LookupSpan;
use tracing_subscriber::util::SubscriberInitExt;

pub use redact::{
    DefaultRedactor, Redactor, Value, is_secret_key, mask_urls, redact, set_redactor,
};

use crate::config::Base;

/// The line format of the log layer.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Format {
    /// One JSON object for each line (the default).
    #[default]
    Json,
    /// `key=value` pairs, as the Go `slog` text handler writes.
    Text,
}

/// The options of the log layer (Go `logx.Options`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Options {
    /// The lowest level that the layer writes.
    pub level: Level,
    /// The line format.
    pub format: Format,
    /// Adds the source file and line to each line.
    pub add_source: bool,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            level: Level::INFO,
            format: Format::Json,
            add_source: false,
        }
    }
}

impl Options {
    /// JSON lines at the level of `LOG_LEVEL`.
    pub fn from_base(base: &Base) -> Self {
        Self {
            level: parse_level(&base.log_level),
            ..Self::default()
        }
    }
}

/// Parses a level name (`trace`, `debug`, `info`, `warn`, `warning`, or `error`, in any
/// case). An unknown name gives `INFO`, as the Go kit does.
pub fn parse_level(name: &str) -> Level {
    match name.trim().to_ascii_lowercase().as_str() {
        "trace" => Level::TRACE,
        "debug" => Level::DEBUG,
        "warn" | "warning" => Level::WARN,
        "error" => Level::ERROR,
        _ => Level::INFO,
    }
}

/// The log layer for `base`: JSON lines to stderr at `LOG_LEVEL`.
pub fn layer<S>(base: &Base) -> impl Layer<S> + Send + Sync + 'static
where
    S: Subscriber + for<'a> LookupSpan<'a>,
{
    layer_with(Options::from_base(base), io::stderr)
}

/// The log layer with `options`, writing to `writer` (for example a buffer in a test).
pub fn layer_with<S, W>(options: Options, writer: W) -> impl Layer<S> + Send + Sync + 'static
where
    S: Subscriber + for<'a> LookupSpan<'a>,
    W: for<'w> MakeWriter<'w> + Send + Sync + 'static,
{
    LogLayer { options, writer }.with_filter(LevelFilter::from_level(options.level))
}

/// Installs [`layer`] as the global subscriber. Call it once at the start of the app, when
/// the app does not use the telemetry module.
pub fn init(base: &Base) -> anyhow::Result<()> {
    tracing_subscriber::registry()
        .with(layer(base))
        .try_init()
        .map_err(|e| {
            anyhow::anyhow!(
                "cannot start the log: {e}\nTry: call kit::logx::init once, and not together with kit::telemetry::init"
            )
        })
}

/// The fields of a span, kept in the span extensions.
struct SpanFields(Vec<(String, Value)>);

struct LogLayer<W> {
    options: Options,
    writer: W,
}

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
            let _ = write!(text, ": {s}");
            source = s.source();
        }
        self.put(field, Value::Str(text));
    }
    fn record_debug(&mut self, field: &Field, value: &dyn fmt::Debug) {
        self.put(field, Value::Str(format!("{value:?}")));
    }
}

impl<S, W> Layer<S> for LogLayer<W>
where
    S: Subscriber + for<'a> LookupSpan<'a>,
    W: for<'w> MakeWriter<'w> + Send + Sync + 'static,
{
    fn on_new_span(&self, attrs: &Attributes<'_>, id: &Id, ctx: Context<'_, S>) {
        let Some(span) = ctx.span(id) else { return };
        let mut fields = Fields::default();
        attrs.record(&mut fields);
        span.extensions_mut().insert(SpanFields(fields.values));
    }

    fn on_record(&self, id: &Id, values: &Record<'_>, ctx: Context<'_, S>) {
        let Some(span) = ctx.span(id) else { return };
        let mut fields = Fields::default();
        values.record(&mut fields);
        let mut ext = span.extensions_mut();
        if let Some(SpanFields(old)) = ext.get_mut::<SpanFields>() {
            for (key, value) in fields.values {
                match old.iter_mut().find(|(k, _)| *k == key) {
                    Some(slot) => slot.1 = value,
                    None => old.push((key, value)),
                }
            }
        }
    }

    fn on_event(&self, event: &Event<'_>, ctx: Context<'_, S>) {
        let mut fields = Fields::default();
        event.record(&mut fields);
        let mut all: Vec<(String, Value)> = Vec::new();
        if let Some(scope) = ctx.event_scope(event) {
            for span in scope.from_root() {
                if let Some(SpanFields(values)) = span.extensions().get::<SpanFields>() {
                    all.extend(values.iter().cloned());
                }
            }
        }
        all.extend(fields.values);
        let meta = event.metadata();
        let record = LogRecord {
            level: *meta.level(),
            message: mask_urls(&fields.message.unwrap_or_default()),
            source: if self.options.add_source {
                meta.file().map(|f| (f, meta.line().unwrap_or(0)))
            } else {
                None
            },
            fields: all
                .into_iter()
                .map(|(k, v)| {
                    let v = redact(&k, v);
                    (k, v)
                })
                .collect(),
        };
        let line = match self.options.format {
            Format::Json => record.json(),
            Format::Text => record.text(),
        };
        let mut w = self.writer.make_writer_for(meta);
        let _ = w.write_all(line.as_bytes());
    }
}

struct LogRecord<'a> {
    level: Level,
    message: String,
    source: Option<(&'a str, u32)>,
    fields: Vec<(String, Value)>,
}

fn level_name(level: Level) -> &'static str {
    match level {
        Level::TRACE => "TRACE",
        Level::DEBUG => "DEBUG",
        Level::INFO => "INFO",
        Level::WARN => "WARN",
        Level::ERROR => "ERROR",
    }
}

fn now() -> String {
    time::OffsetDateTime::now_utc()
        .format(&time::format_description::well_known::Rfc3339)
        .unwrap_or_default()
}

fn json_string(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| "\"\"".into())
}

fn json_value(v: &Value) -> String {
    match v {
        Value::Str(s) => json_string(s),
        Value::Bool(b) => b.to_string(),
        Value::I64(n) => n.to_string(),
        Value::U64(n) => n.to_string(),
        Value::F64(n) if n.is_finite() => serde_json::to_string(n).unwrap_or_else(|_| "0".into()),
        Value::F64(n) => json_string(&n.to_string()),
    }
}

fn text_value(s: &str) -> String {
    let plain = !s.is_empty()
        && s.chars()
            .all(|c| !c.is_whitespace() && !c.is_control() && c != '"' && c != '=');
    if plain { s.to_string() } else { json_string(s) }
}

impl LogRecord<'_> {
    fn json(&self) -> String {
        let mut out = String::with_capacity(128);
        out.push_str("{\"time\":");
        out.push_str(&json_string(&now()));
        out.push_str(",\"level\":\"");
        out.push_str(level_name(self.level));
        out.push('"');
        if let Some((file, line)) = self.source {
            let _ = write!(
                out,
                ",\"source\":{{\"file\":{},\"line\":{line}}}",
                json_string(file)
            );
        }
        out.push_str(",\"msg\":");
        out.push_str(&json_string(&self.message));
        for (k, v) in &self.fields {
            out.push(',');
            out.push_str(&json_string(k));
            out.push(':');
            out.push_str(&json_value(v));
        }
        out.push_str("}\n");
        out
    }

    fn text(&self) -> String {
        let mut out = String::with_capacity(128);
        let _ = write!(out, "time={} level={}", now(), level_name(self.level));
        if let Some((file, line)) = self.source {
            let _ = write!(out, " source={}", text_value(&format!("{file}:{line}")));
        }
        let _ = write!(out, " msg={}", text_value(&self.message));
        for (k, v) in &self.fields {
            let _ = write!(out, " {}={}", text_value(k), text_value(&v.to_string()));
        }
        out.push('\n');
        out
    }
}
