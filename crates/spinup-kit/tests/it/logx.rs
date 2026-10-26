//! The `logx` tests of the kit (Go `kit/logx/logx_test.go` and `redact_test.go`). Nextest
//! runs each test in its own process, so the global redactor of one test does not reach
//! another test.

use std::sync::Arc;

use spinup_kit::config::Base;
use spinup_kit::logx::{self, Format, Options, Redactor, Value};

use crate::httpx::{capture, capture_with};

struct SecretRedactor;

impl Redactor for SecretRedactor {
    fn redact(&self, key: &str, value: Value) -> Value {
        if key == "secret" {
            Value::Str("[redacted]".into())
        } else {
            value
        }
    }
}

#[test]
fn logx_redactor_applies_to_span_and_event_fields() {
    let (_guard, logs) = capture();
    let span = tracing::info_span!("request", secret = "before");
    let _enter = span.enter();
    logx::set_redactor(Some(Arc::new(SecretRedactor)));
    tracing::info!(secret = "after", label = "visible", "test");
    logx::set_redactor(None);
    let text = logs.text();
    assert!(
        !text.contains("before") && !text.contains("after"),
        "{text}"
    );
    let r = &logs.records()[0];
    assert_eq!(r["secret"], "[redacted]");
    assert_eq!(r["label"], "visible");
    assert_eq!(r["msg"], "test");
    assert_eq!(r["level"], "INFO");
    assert!(r["time"].as_str().unwrap().ends_with('Z'));
}

#[test]
fn logx_concurrent_redactor() {
    let threads: Vec<_> = (0..8)
        .map(|_| {
            std::thread::spawn(|| {
                let (_guard, _logs) = capture();
                for _ in 0..100 {
                    logx::set_redactor(Some(Arc::new(SecretRedactor)));
                    tracing::info!(secret = "value", "test");
                    logx::set_redactor(None);
                }
            })
        })
        .collect();
    for t in threads {
        t.join().unwrap();
    }
}

#[test]
fn logx_levels_formats_and_source() {
    for format in [Format::Json, Format::Text] {
        let (_guard, logs) = capture_with(Options {
            level: tracing::Level::WARN,
            format,
            add_source: true,
        });
        tracing::info!("hidden");
        tracing::warn!(n = 3, "visible");
        let text = logs.text();
        assert!(!text.contains("hidden"), "{text}");
        assert!(text.contains("visible"), "{text}");
        assert!(text.contains("source"), "{text}");
        if format == Format::Text {
            assert!(text.contains(" level=WARN source="), "{text}");
            assert!(text.ends_with(" msg=visible n=3\n"), "{text}");
        }
    }
}

#[test]
fn logx_options_from_base() {
    for (level, want) in [
        ("debug", tracing::Level::DEBUG),
        ("info", tracing::Level::INFO),
        ("WARN", tracing::Level::WARN),
        ("error", tracing::Level::ERROR),
        ("loud", tracing::Level::INFO),
        ("", tracing::Level::INFO),
    ] {
        let base = Base {
            log_level: level.into(),
            ..Base::default()
        };
        assert_eq!(Options::from_base(&base).level, want, "{level}");
    }
}

#[test]
fn logx_default_redactor_keys() {
    logx::set_redactor(None);
    for key in [
        "password",
        "client_SECRET",
        "accessToken",
        "Authorization",
        "Set_Cookie",
        "API_KEY",
        "database_dsn",
    ] {
        assert!(logx::is_secret_key(key), "{key}");
        assert_eq!(
            logx::redact(key, Value::Str("hidden".into())),
            Value::Str("****".into())
        );
    }
    let (_guard, logs) = capture();
    let span = tracing::info_span!("saved", password = "hidden-before", label = "visible");
    let _enter = span.enter();
    tracing::info!(api_key = "hidden-after", label = "visible", "test");
    let text = logs.text();
    assert!(!text.contains("hidden-"), "{text}");
    assert_eq!(text.matches("****").count(), 2, "{text}");
    assert_eq!(text.matches("visible").count(), 2, "{text}");
}

#[test]
fn logx_url_redaction() {
    logx::set_redactor(None);
    for value in [
        "postgres://user:private@db/app",
        "postgres://:private@db/app",
        "postgres://user:private/part@db/app",
        "postgres://user:private@part@db/app",
        "failed: https://user:private@example.com/path and postgres://other:p%40ss@db/app",
    ] {
        let (_guard, logs) = capture();
        tracing::info!(url = value, "connect {value}");
        let text = logs.text();
        assert!(
            !text.contains("private") && !text.contains("p%40ss"),
            "{text}"
        );
        assert!(text.contains(":****@"), "{text}");
    }
    assert_eq!(
        logx::mask_urls("postgres://u:p@db/app"),
        "postgres://u:****@db/app"
    );
    assert_eq!(logx::mask_urls("no url"), "no url");
}

#[test]
fn logx_errors_and_debug_values_are_masked() {
    logx::set_redactor(None);
    let (_guard, logs) = capture();
    let err = std::io::Error::other("dial postgres://u:secretpw@db/app");
    tracing::error!(error = &err as &dyn std::error::Error, dbg = ?"postgres://u:pw2@db", "failed");
    let text = logs.text();
    assert!(
        !text.contains("secretpw") && !text.contains("pw2"),
        "{text}"
    );
}

#[test]
fn logx_init_twice_fails_with_try() {
    let base = Base::default();
    logx::init(&base).unwrap();
    let err = logx::init(&base).unwrap_err().to_string();
    assert!(err.contains("\nTry: "), "{err}");
}
