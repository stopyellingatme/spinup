//! The `config` tests of the kit (Go `kit/config/config_test.go`, `dotenv_test.go`, and
//! `apidocs_test.go`). The tests inject the environment and the files: they never change the
//! process environment or the working directory.

use std::collections::HashMap;
use std::io;
use std::path::{Path, PathBuf};
use std::time::Duration;

use spinup_kit::config::{self, Base, ConfigError, Env, FromEnv, dotenv, goduration, parse_bool};

fn env_of(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> + use<> {
    let map: HashMap<String, String> = pairs
        .iter()
        .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
        .collect();
    move |k| map.get(k).cloned()
}

/// A file reader over a map. A missing path is `NotFound`.
fn files_of(pairs: &[(&str, &str)]) -> impl Fn(&Path) -> io::Result<Vec<u8>> + use<> {
    let map: HashMap<PathBuf, Vec<u8>> = pairs
        .iter()
        .map(|(k, v)| (PathBuf::from(k), v.as_bytes().to_vec()))
        .collect();
    move |p| {
        map.get(p)
            .cloned()
            .ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
    }
}

#[derive(Debug, Default)]
struct Embedded {
    enabled: bool,
}

impl FromEnv for Embedded {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self {
            enabled: env.value("ENABLED", true),
        }
    }
}

#[derive(Debug)]
struct Settings {
    embedded: Embedded,
    name: String,
    count: i32,
    big: i64,
    ratio: f64,
    timeout: Duration,
    list: Vec<String>,
}

impl FromEnv for Settings {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self {
            embedded: Embedded::from_env(env),
            name: env.required("NAME"),
            count: env.value("COUNT", 42),
            big: env.value("BIG", 0),
            ratio: env.value("RATIO", 0.0),
            timeout: env.value("TIMEOUT", Duration::from_secs(2)),
            list: env.value("LIST", Vec::new()),
        }
    }
}

fn load_settings(pairs: &[(&str, &str)]) -> Result<Settings, ConfigError> {
    config::load_from(env_of(pairs), |path: &Path| {
        if path == Path::new("bad") {
            Err(io::Error::other("missing"))
        } else {
            Ok(b"api\n".to_vec())
        }
    })
}

#[test]
fn config_load_from() {
    let s = load_settings(&[
        ("NAME", "api"),
        ("BIG", "9000000000"),
        ("RATIO", "1.5"),
        ("LIST", "a, b ,c"),
    ])
    .unwrap();
    assert_eq!(s.name, "api");
    assert_eq!(s.count, 42);
    assert!(s.embedded.enabled);
    assert_eq!(s.timeout, Duration::from_secs(2));
    assert_eq!(s.big, 9_000_000_000);
    assert!((s.ratio - 1.5).abs() < f64::EPSILON);
    assert_eq!(s.list, ["a", "b", "c"]);

    let s = load_settings(&[("NAME_FILE", "secret")]).unwrap();
    assert_eq!(s.name, "api");

    for (pairs, want) in [
        (
            vec![("NAME", "api"), ("NAME_FILE", "secret")],
            vec!["NAME: both NAME and NAME_FILE are set"],
        ),
        (
            vec![
                ("COUNT", "no"),
                ("ENABLED", "no"),
                ("BIG", "no"),
                ("RATIO", "no"),
                ("TIMEOUT", "no"),
            ],
            vec![
                "ENABLED: expected a boolean",
                "NAME: required value is missing",
                "COUNT: expected an integer",
                "BIG: expected an integer",
                "RATIO: expected a number",
                "TIMEOUT: expected a duration",
            ],
        ),
        (
            vec![("NAME_FILE", "bad")],
            vec!["NAME: cannot read NAME_FILE"],
        ),
        (vec![("NAME", "")], vec!["NAME: required value is missing"]),
    ] {
        let err = load_settings(&pairs).unwrap_err();
        assert_eq!(err.problems(), want.as_slice(), "{pairs:?}");
        let text = err.to_string();
        assert!(
            text.ends_with(
                "\nTry: correct the named environment variables or secret files and restart"
            ),
            "{text}"
        );
        assert!(
            !text.contains("secret") || text.contains("secret files"),
            "{text}"
        );
    }
}

#[test]
fn config_base_defaults() {
    let b: Base = config::load_from(|_| None, files_of(&[])).unwrap();
    assert_eq!(b, Base::default());
    assert_eq!(b.env, "production");
    assert_eq!(b.port, 8080);
    assert_eq!(b.log_level, "info");
    assert_eq!(b.shutdown_timeout, Duration::from_secs(20));
    assert!(b.behavior(1));
    assert!(!b.behavior(2));
    assert!(!b.is_dev());
}

#[test]
fn config_base_values_and_errors() {
    let b: Base = config::load_from(
        env_of(&[
            ("SPINUP_ENV", "dev"),
            ("PORT", "9090"),
            ("SHUTDOWN_TIMEOUT", "1m30s"),
            ("DATABASE_URL", "postgres://u:p@db/app"),
            ("DATABASE_MAX_CONNS", "7"),
        ]),
        files_of(&[]),
    )
    .unwrap();
    assert!(b.is_dev());
    assert_eq!(b.port, 9090);
    assert_eq!(b.shutdown_timeout, Duration::from_secs(90));
    assert_eq!(b.database_max_conns, 7);
    let err = config::load_from::<Base>(
        env_of(&[
            ("PORT", "abc"),
            ("SHUTDOWN_TIMEOUT", "soon"),
            ("PORT_FILE", "x"),
        ]),
        files_of(&[]),
    )
    .unwrap_err();
    assert_eq!(
        err.problems(),
        [
            "PORT: both PORT and PORT_FILE are set",
            "SHUTDOWN_TIMEOUT: expected a duration"
        ]
    );
    // Debug masks the database password.
    let debug = format!("{b:?}");
    assert!(debug.contains("postgres://u:****@db/app"), "{debug}");
}

#[test]
fn config_base_serializes_with_env_names() {
    let v = serde_json::to_value(Base::default()).unwrap();
    assert_eq!(
        v,
        serde_json::json!({
            "SPINUP_ENV": "production",
            "SPINUP_API_DOCS": false,
            "PORT": 8080,
            "DATABASE_URL": "",
            "DATABASE_MAX_CONNS": 0,
            "LOG_LEVEL": "info",
            "SHUTDOWN_TIMEOUT": "20s",
            "SPINUP_BEHAVIOR_VERSION": 1
        })
    );
}

#[test]
fn config_api_docs_environment() {
    for (value, want) in [("", false), ("1", true), ("0", false), ("true", true)] {
        let lookup =
            move |k: &str| (k == "SPINUP_API_DOCS" && !value.is_empty()).then(|| value.to_string());
        let b: Base = config::load_from(lookup, files_of(&[])).unwrap();
        assert_eq!(b.api_docs, want, "{value:?}");
    }
}

#[test]
fn config_load_environment_default() {
    for (env, dotenv_text, want) in [
        (None, "", "production"),
        (None, "SPINUP_ENV=dev\n", "dev"),
        (Some("dev"), "", "dev"),
        (Some("production"), "SPINUP_ENV=dev\n", "production"),
    ] {
        let lookup = move |k: &str| {
            (k == "SPINUP_ENV")
                .then(|| env.map(str::to_string))
                .flatten()
        };
        let b: Base = config::load_with(lookup, files_of(&[(".env", dotenv_text)])).unwrap();
        assert_eq!(b.env, want, "{env:?} {dotenv_text:?}");
    }
}

#[derive(Debug)]
struct Pair {
    value: String,
    over: String,
}

impl FromEnv for Pair {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self {
            value: env.value("SPINUP_TEST_VALUE", "default".to_string()),
            over: env.value("SPINUP_TEST_OVERRIDE", String::new()),
        }
    }
}

#[test]
fn config_load_dotenv() {
    let text = "SPINUP_TEST_VALUE=file\nSPINUP_TEST_OVERRIDE=file\n";
    for (env, location, want) in [
        ("dev", ".env", "file"),
        ("", "../.env", "file"),
        ("production", ".env", "default"),
    ] {
        let lookup = env_of(&[("SPINUP_ENV", env), ("SPINUP_TEST_OVERRIDE", "environment")]);
        let p: Pair = config::load_with(lookup, files_of(&[(location, text)])).unwrap();
        assert_eq!(p.value, want, "{env} {location}");
        assert_eq!(p.over, "environment");
    }
    // ./.env wins over ../.env.
    let p: Pair = config::load_with(
        |_| None,
        files_of(&[
            (".env", "SPINUP_TEST_VALUE=here"),
            ("../.env", "SPINUP_TEST_VALUE=parent"),
        ]),
    )
    .unwrap();
    assert_eq!(p.value, "here");
    // A bad .env file fails the load and names the file and the line.
    let err =
        config::load_with::<Pair>(|_| None, files_of(&[(".env", "NO_EQUALS\n")])).unwrap_err();
    assert_eq!(err.problems(), [".env: line 1: expected KEY=value"]);
}

#[derive(Debug)]
struct Secret(String);

impl FromEnv for Secret {
    fn from_env(env: &mut Env<'_>) -> Self {
        Self(env.value("SPINUP_TEST_SECRET", String::new()))
    }
}

#[test]
fn config_environment_overrides_dotenv_secret() {
    for (shell_key, shell_value, dotenv_text, want) in [
        (
            "SPINUP_TEST_SECRET_FILE",
            "secret",
            "SPINUP_TEST_SECRET=dotenv",
            "from-file",
        ),
        (
            "SPINUP_TEST_SECRET",
            "from-shell",
            "SPINUP_TEST_SECRET_FILE=missing",
            "from-shell",
        ),
        (
            "SPINUP_TEST_SECRET",
            "from-shell",
            "SPINUP_TEST_SECRET=dotenv",
            "from-shell",
        ),
        (
            "SPINUP_TEST_SECRET_FILE",
            "secret",
            "SPINUP_TEST_SECRET_FILE=missing",
            "from-file",
        ),
        (
            "SPINUP_TEST_SECRET",
            "",
            "SPINUP_TEST_SECRET_FILE=missing",
            "",
        ),
    ] {
        let lookup = env_of(&[("SPINUP_ENV", "dev"), (shell_key, shell_value)]);
        let files = files_of(&[
            ("secret", "from-file\n"),
            (".env", &format!("{dotenv_text}\n")),
        ]);
        let s: Secret = config::load_with(lookup, files).unwrap();
        assert_eq!(s.0, want, "{shell_key}={shell_value} with {dotenv_text}");
    }
}

#[test]
fn config_secret_newline() {
    for (input, want) in [
        ("secret\n", "secret"),
        ("secret\r\n", "secret"),
        ("secret\n\n", "secret\n"),
        ("secret ", "secret "),
    ] {
        let s: Secret = config::load_from(
            env_of(&[("SPINUP_TEST_SECRET_FILE", "file")]),
            files_of(&[("file", input)]),
        )
        .unwrap();
        assert_eq!(s.0, want, "{input:?}");
    }
}

#[test]
fn config_parse_dotenv() {
    let got = dotenv::parse(
        "# comment\nA=one # tail\nexport B='two # literal'\nC=\"three\\nlines\" # tail\nD=\nE=a=b\nexport\tF=x\n  G = spaced  \nH=no#comment\nI=\"\\u00e9\\x41\\101\"\n",
    )
    .unwrap();
    let want: Vec<(&str, &str)> = vec![
        ("A", "one"),
        ("B", "two # literal"),
        ("C", "three\nlines"),
        ("D", ""),
        ("E", "a=b"),
        ("F", "x"),
        ("G", "spaced"),
        ("H", "no#comment"),
        ("I", "\u{e9}AA"),
    ];
    assert_eq!(
        got.iter()
            .map(|(k, v)| (k.as_str(), v.as_str()))
            .collect::<Vec<_>>(),
        want
    );
    let err = dotenv::parse("NO_EQUALS\nA='unterminated\n1INVALID=x\nB=\"bad\\q\"\n").unwrap_err();
    assert_eq!(
        err,
        "line 1: expected KEY=value\nline 2 (A): unterminated quoted value\nline 3: expected KEY=value\nline 4 (B): invalid quoted value"
    );
    assert_eq!(
        dotenv::parse("A='value' garbage").unwrap_err(),
        "line 1 (A): unexpected text after quoted value"
    );
}

#[test]
fn config_goduration_matches_go() {
    for (ns, want) in [
        (0, "0s"),
        (1, "1ns"),
        (1_500, "1.5\u{b5}s"),
        (1_000_000, "1ms"),
        (1_500_000, "1.5ms"),
        (50_000_000, "50ms"),
        (3_000_000_000, "3s"),
        (20_000_000_000, "20s"),
        (90_000_000_000, "1m30s"),
        (3_600_000_000_000, "1h0m0s"),
        (86_400_000_000_000, "24h0m0s"),
        (604_800_000_000_000, "168h0m0s"),
        (-3_000_000_000, "-3s"),
        (i64::MAX, "2562047h47m16.854775807s"),
        (i64::MIN, "-2562047h47m16.854775808s"),
    ] {
        assert_eq!(goduration::format(ns), want, "{ns}");
    }
    for (s, want) in [
        ("3s", Some(3_000_000_000)),
        ("1h", Some(3_600_000_000_000)),
        ("1h30m", Some(5_400_000_000_000)),
        ("1.5h", Some(5_400_000_000_000)),
        ("250ms", Some(250_000_000)),
        ("1us", Some(1_000)),
        ("1\u{b5}s", Some(1_000)),
        ("1\u{3bc}s", Some(1_000)),
        ("10ns", Some(10)),
        (".5s", Some(500_000_000)),
        ("1.s", Some(1_000_000_000)),
        ("0", Some(0)),
        ("+0", Some(0)),
        ("0s", Some(0)),
        ("-1s", Some(-1_000_000_000)),
        ("+2m", Some(120_000_000_000)),
        ("2562047h47m16.854775807s", Some(i64::MAX)),
        ("-2562047h47m16.854775808s", Some(i64::MIN)),
        ("2562047h47m16.854775808s", None),
        ("9223372036854775808ns", None),
        ("3", None),
        ("", None),
        ("-", None),
        ("s", None),
        (".s", None),
        ("1d", None),
        ("1h 30m", None),
    ] {
        assert_eq!(goduration::parse(s), want, "{s:?}");
    }
    assert_eq!(goduration::parse_std("-1s"), None);
    assert_eq!(goduration::parse_std("2s"), Some(Duration::from_secs(2)));
    assert_eq!(goduration::format_std(Duration::from_millis(1500)), "1.5s");
}

#[test]
fn config_parse_bool_matches_go() {
    for t in ["1", "t", "T", "TRUE", "true", "True"] {
        assert_eq!(parse_bool(t), Some(true), "{t}");
    }
    for f in ["0", "f", "F", "FALSE", "false", "False"] {
        assert_eq!(parse_bool(f), Some(false), "{f}");
    }
    for bad in ["", "yes", "tRUE", "2"] {
        assert_eq!(parse_bool(bad), None, "{bad}");
    }
}
