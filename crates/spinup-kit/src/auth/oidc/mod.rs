//! Generic OpenID Connect sign-in (Go `kit/auth/oidc.go`, ADR 0013 §3).
//!
//! ```text
//! GET /api/auth/oidc/providers             -> 200 [{"name","label"}]
//! GET /api/auth/oidc/{provider}/start      -> 303 to the provider, 400, 404, 503
//! GET /api/auth/oidc/{provider}/callback   -> 303 to the return path, 400, 404, 429, 503
//! ```
//!
//! - The authorization code flow uses PKCE (S256), `state`, and `nonce`. The flow keeps
//!   them and the return path in the cookie `oidc_<provider>` for ten minutes, sealed with
//!   AES-256-GCM. Every callback removes the cookie, so a code works once.
//! - The return path comes from `?next=` (default `/account`). It must be a local path:
//!   one leading `/`, also after percent-decoding, and no backslash or control character.
//! - The ID token must have a valid signature of a provider key, the provider issuer, the
//!   client ID as audience, a future expiry, the nonce of the flow, a subject, and a valid
//!   email with `email_verified` equal to `true` (the boolean or the string, as in Go).
//! - A known identity signs in to its account, also when the email claim changed. A new
//!   identity with the email of an existing account takes over that account: its password
//!   and its sessions end. Else a new account without a password starts.
//! - Configuration errors never stop the app. The provider routes answer 503 with a
//!   `Try:` line. The routes contact no provider before the first sign-in.
//!
//! Settings: `AUTH_OIDC_PROVIDERS` (a comma-separated list of lower-case names),
//! `PUBLIC_ORIGIN` (default `http://localhost:5173`), `AUTH_OIDC_COOKIE_KEY` (32 bytes,
//! unpadded base64url; a random key when empty), and for each provider
//! `AUTH_OIDC_<NAME>_{ISSUER,CLIENT_ID,CLIENT_SECRET,SCOPES,LABEL}`. Each setting also
//! reads `<KEY>_FILE`.
//!
//! ```no_run
//! # async fn demo(pool: spinup_kit::db::PgPool) -> anyhow::Result<()> {
//! use spinup_kit::auth::oidc::{self, OidcOptions};
//! use spinup_kit::auth::{Auth, Options};
//!
//! let auth = Auth::new(pool, Options::default())?;
//! let api = auth.routes().merge(oidc::routes(&auth, OidcOptions::load()));
//! let api = auth.layer(api);
//! # let _ = api;
//! # Ok(())
//! # }
//! ```

#[cfg(any(test, feature = "testkit"))]
pub mod fake_issuer;
mod flow;
mod store;

use std::collections::{BTreeMap, HashSet};
use std::io;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use aws_lc_rs::aead::{AES_256_GCM, Aad, LessSafeKey, NONCE_LEN, Nonce, UnboundKey};
use axum::extract::{Path as UrlPath, RawQuery, State};
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router, middleware};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use serde::{Deserialize, Serialize};
use subtle::ConstantTimeEq;
use tokio::sync::Mutex;

use crate::auth::ratelimit::{OIDC_CALLBACK_SUFFIX, OIDC_PREFIX};
use crate::auth::required::{Auth, Session, internal_error};
use crate::auth::routes::{endpoint, normalize_email};
use crate::config::{Env, dotenv};
use crate::httpx::Problem;
use flow::{Client, Discovered, OidcError};

/// The lifetime of a sign-in flow and its cookie.
pub const OIDC_LIFETIME: Duration = Duration::from_secs(600);
/// The public origin when `PUBLIC_ORIGIN` is not set.
pub const DEFAULT_PUBLIC_ORIGIN: &str = "http://localhost:5173";
/// The return path when the start has no `next`.
pub const DEFAULT_NEXT: &str = "/account";
/// The path of the provider list.
pub const PATH_PROVIDERS: &str = "/api/auth/oidc/providers";
/// The limit of one request to a provider.
pub const PROVIDER_TIMEOUT: Duration = Duration::from_secs(15);

/// The longest return path.
const MAX_NEXT: usize = 1024;
/// The longest flow cookie that the callback reads.
const MAX_COOKIE: usize = 4096;
/// The scopes that every provider gets first.
const BASE_SCOPES: [&str; 3] = ["openid", "email", "profile"];

/// One OIDC provider (Go `oidcProvider`), from `AUTH_OIDC_<NAME>_*`.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct Provider {
    /// The name in the routes, such as `google`: a lower-case letter, then lower-case
    /// letters, digits, and `_`.
    pub name: String,
    /// The label for the sign-in button. The default is the name.
    pub label: String,
    /// The issuer URL. Discovery reads `<issuer>/.well-known/openid-configuration`.
    pub issuer: String,
    /// The client ID.
    pub client_id: String,
    /// The client secret. Empty for a public client.
    pub client_secret: String,
    /// The scopes: `openid`, `email`, `profile`, then the extra scopes.
    pub scopes: Vec<String>,
    /// A configuration problem. The routes of this provider then answer 503.
    pub error: Option<String>,
}

impl std::fmt::Debug for Provider {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Provider")
            .field("name", &self.name)
            .field("label", &self.label)
            .field("issuer", &self.issuer)
            .field("client_id", &self.client_id)
            .field(
                "client_secret",
                &if self.client_secret.is_empty() {
                    ""
                } else {
                    "***"
                },
            )
            .field("scopes", &self.scopes)
            .field("error", &self.error)
            .finish()
    }
}

fn issuer_ok(raw: &str) -> bool {
    url::Url::parse(raw).is_ok_and(|u| {
        matches!(u.scheme(), "http" | "https")
            && u.host_str().is_some_and(|h| !h.is_empty())
            && u.username().is_empty()
            && u.password().is_none()
            && u.query().is_none()
            && u.fragment().is_none()
    })
}

/// The Go origin rule: an issuer URL without a path, and `https` when the cookies are
/// Secure.
fn origin_ok(raw: &str, secure: bool) -> bool {
    let bare = raw
        .split_once("://")
        .is_some_and(|(_, rest)| !rest.contains(['/', '?', '#', '@']));
    issuer_ok(raw) && bare && (!secure || raw.starts_with("https://"))
}

fn name_ok(name: &str) -> bool {
    name.starts_with(|c: char| c.is_ascii_lowercase())
        && name
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
}

impl Provider {
    /// Reads the provider `name` from `AUTH_OIDC_<NAME>_ISSUER` and `_CLIENT_ID`
    /// (required), `_CLIENT_SECRET`, `_SCOPES` (comma- or space-separated), and `_LABEL`
    /// (Go `loadOIDCProvider`). Each setting also reads `<KEY>_FILE`. A problem goes into
    /// [`Provider::error`] and never holds a value.
    pub fn from_env(
        name: &str,
        lookup: impl Fn(&str) -> Option<String>,
        read_file: impl Fn(&Path) -> io::Result<Vec<u8>>,
    ) -> Self {
        let prefix = format!("AUTH_OIDC_{}_", name.to_ascii_uppercase());
        let key = |k: &str| format!("{prefix}{k}");
        let mut env = Env::new(&lookup, &read_file);
        let issuer: String = env.required(&key("ISSUER"));
        let client_id: String = env.required(&key("CLIENT_ID"));
        let client_secret = env.value(&key("CLIENT_SECRET"), String::new());
        let extra = env.value(&key("SCOPES"), String::new());
        let label = env.value(&key("LABEL"), String::new());
        let mut problems = env.problems().to_vec();
        if problems.is_empty() && !issuer_ok(&issuer) {
            problems.push(format!("{}: the issuer URL is invalid", key("ISSUER")));
        }
        let mut scopes: Vec<String> = BASE_SCOPES.iter().map(|s| (*s).to_owned()).collect();
        for s in extra.replace(',', " ").split_whitespace() {
            if !scopes.iter().any(|x| x == s) {
                scopes.push(s.to_owned());
            }
        }
        Self {
            name: name.to_owned(),
            label: if label.is_empty() {
                name.to_owned()
            } else {
                label
            },
            issuer,
            client_id,
            client_secret,
            scopes,
            error: (!problems.is_empty()).then(|| problems.join("\n")),
        }
    }
}

/// The settings of [`routes`] (Go `oidcSettings` and the provider list).
#[derive(Clone, Default)]
pub struct OidcOptions {
    /// The providers, in the order of `AUTH_OIDC_PROVIDERS`. With no provider and no
    /// error, [`routes`] gives an empty router.
    pub providers: Vec<Provider>,
    /// `PUBLIC_ORIGIN`: the origin of the callback URLs, such as `https://app.example`.
    /// It has no path. With Secure cookies it must use `https`.
    pub public_origin: String,
    /// `AUTH_OIDC_COOKIE_KEY`: the AES-256-GCM key of the flow cookie. `None` gives a
    /// random key, so flows end when the app restarts. Several instances need one key.
    pub cookie_key: Option<[u8; 32]>,
    /// A configuration problem of the settings. Every provider route then answers 503.
    pub error: Option<String>,
    /// The HTTP client for the provider requests. `None` gives a client with a 15-second
    /// limit that follows no redirect. Tests pass their own.
    pub http: Option<reqwest::Client>,
}

impl std::fmt::Debug for OidcOptions {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("OidcOptions")
            .field("providers", &self.providers)
            .field("public_origin", &self.public_origin)
            .field("cookie_key", &self.cookie_key.map(|_| "***"))
            .field("error", &self.error)
            .finish_non_exhaustive()
    }
}

impl OidcOptions {
    /// Reads the settings with `lookup` and `read_file` (tests). A problem goes into
    /// [`OidcOptions::error`] or [`Provider::error`] and never stops the app.
    pub fn from_env(
        lookup: impl Fn(&str) -> Option<String>,
        read_file: impl Fn(&Path) -> io::Result<Vec<u8>>,
    ) -> Self {
        let mut env = Env::new(&lookup, &read_file);
        let names = env.value("AUTH_OIDC_PROVIDERS", String::new());
        let public_origin = env.value("PUBLIC_ORIGIN", DEFAULT_PUBLIC_ORIGIN.to_owned());
        let key = env.value("AUTH_OIDC_COOKIE_KEY", String::new());
        let mut problems = env.problems().to_vec();
        let cookie_key = if key.is_empty() {
            None
        } else {
            match B64URL.decode(key.as_bytes()) {
                Ok(b) if b.len() == 32 => {
                    let mut k = [0u8; 32];
                    k.copy_from_slice(&b);
                    Some(k)
                }
                _ => {
                    problems.push(
                        "AUTH_OIDC_COOKIE_KEY must encode 32 bytes as unpadded base64url".into(),
                    );
                    None
                }
            }
        };
        let mut providers = Vec::new();
        if !names.trim().is_empty() {
            let mut seen = HashSet::new();
            for name in names.split(',').map(str::trim) {
                if !name_ok(name) || !seen.insert(name.to_owned()) {
                    problems.push(
                        "AUTH_OIDC_PROVIDERS must list distinct lower-case provider names".into(),
                    );
                    continue;
                }
                providers.push(Provider::from_env(name, &lookup, &read_file));
            }
        }
        Self {
            providers,
            public_origin,
            cookie_key,
            error: (!problems.is_empty()).then(|| problems.join("\n")),
            http: None,
        }
    }

    /// Reads the settings from the process environment (Go `config.Load`). When
    /// `SPINUP_ENV` is empty or `dev`, it also reads `./.env` (or `../.env`), and a variable
    /// of the process environment wins. It never changes the process environment.
    pub fn load() -> Self {
        let lookup = |key: &str| std::env::var_os(key).map(|v| v.to_string_lossy().into_owned());
        let read = |path: &Path| std::fs::read(path);
        let (values, problem) = match lookup("SPINUP_ENV").as_deref() {
            None | Some("" | "dev") => match dotenv::load(&read) {
                Ok(v) => (v, None),
                Err(e) => (BTreeMap::new(), Some(e)),
            },
            Some(_) => (BTreeMap::new(), None),
        };
        let merged = |key: &str| -> Option<String> {
            if let Some(v) = lookup(key) {
                return Some(v);
            }
            // The environment wins over both forms of a setting in `.env`.
            let other = match key.strip_suffix("_FILE") {
                Some(base) => base.to_owned(),
                None => format!("{key}_FILE"),
            };
            if lookup(&other).is_some() {
                return None;
            }
            values.get(key).cloned()
        };
        let mut o = Self::from_env(merged, read);
        if let Some(p) = problem {
            o.error = Some(match o.error.take() {
                Some(e) => format!("{p}\n{e}"),
                None => p,
            });
        }
        o
    }
}

/// One provider of the running routes.
struct ProviderState {
    p: Provider,
    redirect_uri: String,
    discovered: Mutex<Option<Arc<Discovered>>>,
}

impl ProviderState {
    fn client(&self) -> Client<'_> {
        Client {
            client_id: &self.p.client_id,
            secret: &self.p.client_secret,
            redirect_uri: &self.redirect_uri,
            scopes: &self.p.scopes,
        }
    }
}

/// The flow data in the sealed cookie (Go `oidcFlow`).
#[derive(Serialize, Deserialize)]
struct Flow {
    provider: String,
    state: String,
    nonce: String,
    verifier: String,
    next: String,
    expires: i64,
}

struct OidcState {
    auth: Auth,
    providers: Vec<Arc<ProviderState>>,
    key: LessSafeKey,
    err: bool,
    http: Option<reqwest::Client>,
}

fn random_token() -> String {
    let mut b = [0u8; 32];
    if aws_lc_rs::rand::fill(&mut b).is_err() {
        panic!("auth: the system random source failed");
    }
    B64URL.encode(b)
}

/// Returns the OIDC routes with the state applied (Go `oidcRoutes`). With no provider and
/// no configuration error, it returns an empty router. It contacts no provider. The
/// routes need [`Auth::layer`]. A bad `PUBLIC_ORIGIN` or any problem in `options` makes
/// the provider routes answer 503 with a `Try:` line.
pub fn routes(auth: &Auth, options: OidcOptions) -> Router {
    if options.providers.is_empty() && options.error.is_none() {
        return Router::new();
    }
    let origin = options.public_origin.clone();
    let mut err = options.error.is_some();
    if !origin_ok(&origin, auth.secure()) {
        err = true;
        tracing::warn!(
            "try" = "set PUBLIC_ORIGIN to the https origin of the web app, without a path",
            "auth: OIDC sign-in is unavailable: PUBLIC_ORIGIN is invalid"
        );
    }
    if let Some(e) = &options.error {
        tracing::warn!(error = %e, "try" = "correct the AUTH_OIDC settings", "auth: OIDC sign-in is unavailable");
    }
    let key = options.cookie_key.unwrap_or_else(|| {
        let mut k = [0u8; 32];
        if aws_lc_rs::rand::fill(&mut k).is_err() {
            panic!("auth: the system random source failed");
        }
        k
    });
    let key = match UnboundKey::new(&AES_256_GCM, &key) {
        Ok(k) => LessSafeKey::new(k),
        Err(_) => panic!("auth: AES-256-GCM refused a 32-byte key"),
    };
    let http = options.http.clone().or_else(|| {
        reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(PROVIDER_TIMEOUT)
            .build()
            .ok()
    });
    let providers = options
        .providers
        .into_iter()
        .map(|p| {
            if let Some(e) = &p.error {
                tracing::warn!(provider = %p.name, error = %e, "auth: an OIDC provider is unavailable");
            }
            Arc::new(ProviderState {
                redirect_uri: format!("{origin}{OIDC_PREFIX}{}{OIDC_CALLBACK_SUFFIX}", p.name),
                p,
                discovered: Mutex::new(None),
            })
        })
        .collect();
    let state = Arc::new(OidcState {
        auth: auth.clone(),
        providers,
        key,
        err,
        http,
    });
    Router::new()
        .route(PATH_PROVIDERS, get(list))
        .route("/api/auth/oidc/{provider}/start", get(start))
        .route("/api/auth/oidc/{provider}/callback", get(callback))
        .layer(middleware::map_response(endpoint))
        .with_state(state)
}

fn percent_decode(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let hex = s.get(i + 1..i + 3)?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    Some(String::from_utf8_lossy(&out).into_owned())
}

/// Reports whether `path` is a safe local return path (Go `oidcRedirectOK`): it starts
/// with one `/`, also after percent-decoding, and has no backslash or control character.
/// Browsers treat a backslash as a separator.
pub fn redirect_ok(path: &str) -> bool {
    let plain = |v: &str| {
        v.starts_with('/') && !v.starts_with("//") && !v.contains(['\\', '\r', '\n', '\0', '\t'])
    };
    if !plain(path) {
        return false;
    }
    let path_part = path.split(['?', '#']).next().unwrap_or("");
    percent_decode(path_part).is_some_and(|d| plain(&d))
}

/// Cleans the path of a local redirect as Go `http.Redirect` does (`path.Clean`, with
/// the trailing slash kept), and percent-encodes bytes that a header cannot hold.
fn location(next: &str) -> String {
    let (path, query) = match next.find('?') {
        Some(i) => next.split_at(i),
        None => (next, ""),
    };
    let mut parts: Vec<&str> = Vec::new();
    for seg in path.split('/') {
        match seg {
            "" | "." => {}
            ".." => {
                parts.pop();
            }
            s => parts.push(s),
        }
    }
    let mut clean = format!("/{}", parts.join("/"));
    if path.ends_with('/') && !clean.ends_with('/') {
        clean.push('/');
    }
    clean.push_str(query);
    let mut out = String::with_capacity(clean.len());
    for b in clean.bytes() {
        if b.is_ascii_graphic() {
            out.push(char::from(b));
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

fn unavailable() -> Response {
    Problem::new(
        StatusCode::SERVICE_UNAVAILABLE,
        "This sign-in provider is unavailable or its configuration is invalid.\nTry: check AUTH_OIDC settings and provider availability, then retry.",
    )
    .into_response()
}

fn invalid_answer() -> Response {
    Problem::new(
        StatusCode::BAD_REQUEST,
        "The sign-in response is invalid, expired, or has no verified email.\nTry: start sign-in again with a verified email.",
    )
    .into_response()
}

fn see_other(location: &str) -> Response {
    let mut res = StatusCode::SEE_OTHER.into_response();
    if let Ok(v) = HeaderValue::from_str(location) {
        res.headers_mut().insert(header::LOCATION, v);
    }
    res
}

/// Formats an HTTP date, as Go `http.TimeFormat`.
fn http_date(t: time::OffsetDateTime) -> String {
    let format = time::macros::format_description!(
        "[weekday repr:short], [day] [month repr:short] [year] [hour]:[minute]:[second] GMT"
    );
    t.to_offset(time::UtcOffset::UTC)
        .format(format)
        .unwrap_or_default()
}

impl OidcState {
    #[allow(clippy::result_large_err)] // the error is the answer itself
    fn provider(&self, name: &str) -> Result<Arc<ProviderState>, Response> {
        if self.err {
            return Err(unavailable());
        }
        match self.providers.iter().find(|p| p.p.name == name) {
            Some(p) if p.p.error.is_some() => Err(unavailable()),
            Some(p) => Ok(p.clone()),
            None => Err(Problem::new(
                StatusCode::NOT_FOUND,
                "The sign-in provider is unknown.\nTry: choose a configured provider.",
            )
            .into_response()),
        }
    }

    fn http(&self) -> Result<&reqwest::Client, OidcError> {
        self.http
            .as_ref()
            .ok_or_else(|| OidcError::Unavailable("no HTTP client".into()))
    }

    async fn discover(&self, p: &ProviderState) -> Result<Arc<Discovered>, OidcError> {
        let mut slot = p.discovered.lock().await;
        if let Some(d) = slot.as_ref() {
            return Ok(d.clone());
        }
        let d = Arc::new(flow::discover(self.http()?, &p.p.issuer).await?);
        *slot = Some(d.clone());
        Ok(d)
    }

    fn cookie_name(p: &ProviderState) -> String {
        format!("oidc_{}", p.p.name)
    }

    /// The `Set-Cookie` line of the flow cookie, as Go `http.SetCookie` writes it.
    /// `None` removes the cookie.
    fn cookie(&self, p: &ProviderState, value: Option<&str>) -> Option<HeaderValue> {
        let mut line = format!(
            "{}={}; Path={OIDC_PREFIX}{}/",
            Self::cookie_name(p),
            value.unwrap_or(""),
            p.p.name
        );
        match value {
            Some(_) => {
                let expires = self.auth.now() + OIDC_LIFETIME;
                line.push_str(&format!(
                    "; Expires={}; Max-Age={}",
                    http_date(expires),
                    OIDC_LIFETIME.as_secs()
                ));
            }
            None => line.push_str("; Max-Age=0"),
        }
        line.push_str("; HttpOnly");
        if self.auth.secure() {
            line.push_str("; Secure");
        }
        line.push_str("; SameSite=Lax");
        HeaderValue::from_str(&line).ok()
    }

    fn seal(&self, p: &ProviderState, flow: &Flow) -> Option<String> {
        let mut nonce = [0u8; NONCE_LEN];
        aws_lc_rs::rand::fill(&mut nonce).ok()?;
        let mut buf = serde_json::to_vec(flow).ok()?;
        self.key
            .seal_in_place_append_tag(
                Nonce::assume_unique_for_key(nonce),
                Aad::from(p.p.name.as_bytes()),
                &mut buf,
            )
            .ok()?;
        let mut out = nonce.to_vec();
        out.extend(buf);
        Some(B64URL.encode(out))
    }

    /// The flow of the request cookie, or `None` for a missing, tampered, foreign, or
    /// expired cookie.
    fn flow(&self, headers: &HeaderMap, p: &ProviderState) -> Option<Flow> {
        let name = Self::cookie_name(p);
        let value = headers
            .get_all(header::COOKIE)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .flat_map(|v| v.split(';'))
            .filter_map(|kv| kv.trim().split_once('='))
            .find(|(k, _)| *k == name)
            .map(|(_, v)| v.to_owned())?;
        if value.len() > MAX_COOKIE {
            return None;
        }
        let data = B64URL.decode(value.as_bytes()).ok()?;
        if data.len() < NONCE_LEN {
            return None;
        }
        let (n, sealed) = data.split_at(NONCE_LEN);
        let mut buf = sealed.to_vec();
        let plain = self
            .key
            .open_in_place(
                Nonce::try_assume_unique_for_key(n).ok()?,
                Aad::from(p.p.name.as_bytes()),
                &mut buf,
            )
            .ok()?;
        let flow: Flow = serde_json::from_slice(plain).ok()?;
        let now = self.auth.now().unix_timestamp();
        let lifetime = i64::try_from(OIDC_LIFETIME.as_secs()).unwrap_or(i64::MAX);
        let ok = flow.provider == p.p.name
            && !flow.state.is_empty()
            && !flow.nonce.is_empty()
            && !flow.verifier.is_empty()
            && flow.expires > now
            && flow.expires <= now.saturating_add(lifetime)
            && redirect_ok(&flow.next);
        ok.then_some(flow)
    }
}

async fn list(State(s): State<Arc<OidcState>>) -> Response {
    let out: Vec<_> = s
        .providers
        .iter()
        .map(|p| serde_json::json!({"name": p.p.name, "label": p.p.label}))
        .collect();
    Json(out).into_response()
}

fn query_pairs(q: Option<&str>) -> Vec<(String, String)> {
    form_urlencoded::parse(q.unwrap_or("").as_bytes())
        .into_owned()
        .collect()
}

// The session extractor answers 500 without the auth layer, as Go `endpoint` does.
async fn start(
    State(s): State<Arc<OidcState>>,
    UrlPath(name): UrlPath<String>,
    _session: Session,
    RawQuery(q): RawQuery,
) -> Response {
    let p = match s.provider(&name) {
        Ok(p) => p,
        Err(r) => return r,
    };
    let next = query_pairs(q.as_deref())
        .into_iter()
        .find(|(k, _)| k == "next")
        .map(|(_, v)| v)
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| DEFAULT_NEXT.into());
    if !redirect_ok(&next) || next.len() > MAX_NEXT {
        return invalid_answer();
    }
    let Ok(d) = s.discover(&p).await else {
        return unavailable();
    };
    let lifetime = i64::try_from(OIDC_LIFETIME.as_secs()).unwrap_or(i64::MAX);
    let flow = Flow {
        provider: p.p.name.clone(),
        state: random_token(),
        nonce: random_token(),
        verifier: random_token(),
        next,
        expires: s.auth.now().unix_timestamp().saturating_add(lifetime),
    };
    let Ok(target) = flow::authorize_url(&d, &p.client(), &flow.state, &flow.nonce, &flow.verifier)
    else {
        return unavailable();
    };
    let Some(cookie) = s.seal(&p, &flow).and_then(|v| s.cookie(&p, Some(&v))) else {
        return internal_error("seal the OIDC flow", &"the flow cookie cannot be written");
    };
    let mut res = see_other(&target);
    res.headers_mut().append(header::SET_COOKIE, cookie);
    res
}

async fn callback(
    State(s): State<Arc<OidcState>>,
    UrlPath(name): UrlPath<String>,
    session: Session,
    headers: HeaderMap,
    RawQuery(q): RawQuery,
) -> Response {
    let p = match s.provider(&name) {
        Ok(p) => p,
        Err(r) => return r,
    };
    let mut res = callback_inner(&s, &p, &session, &headers, q.as_deref()).await;
    // Every answer removes the flow cookie, so a code cannot be replayed.
    if let Some(c) = s.cookie(&p, None) {
        res.headers_mut().append(header::SET_COOKIE, c);
    }
    res
}

async fn callback_inner(
    s: &OidcState,
    p: &ProviderState,
    session: &Session,
    headers: &HeaderMap,
    q: Option<&str>,
) -> Response {
    let flow = s.flow(headers, p);
    let pairs = query_pairs(q);
    let all = |k: &str| -> Vec<&str> {
        pairs
            .iter()
            .filter(|(n, _)| n == k)
            .map(|(_, v)| v.as_str())
            .collect()
    };
    let (states, codes, errors) = (all("state"), all("code"), all("error"));
    let Some(flow) = flow else {
        return invalid_answer();
    };
    if states.len() != 1
        || !bool::from(states[0].as_bytes().ct_eq(flow.state.as_bytes()))
        || errors.first().is_some_and(|e| !e.is_empty())
        || codes.len() != 1
        || codes[0].is_empty()
    {
        return invalid_answer();
    }
    let Ok(d) = s.discover(p).await else {
        return unavailable();
    };
    let Ok(http) = s.http() else {
        return unavailable();
    };
    let client = p.client();
    let id = match flow::exchange(http, &d, &client, codes[0], &flow.verifier).await {
        Ok(raw) => flow::verify_id_token(http, &d, &client, &raw, &flow.nonce).await,
        Err(e) => Err(e),
    };
    let id = match id {
        Ok(id) => id,
        Err(OidcError::Unavailable(_)) => return unavailable(),
        Err(OidcError::Invalid(why)) => {
            tracing::info!(provider = %p.p.name, why = %why, "auth: OIDC callback refused");
            return invalid_answer();
        }
    };
    let (email, valid) = normalize_email(&id.email);
    if !valid || !id.email_verified {
        return invalid_answer();
    }
    let user = match store::link(&s.auth, &p.p.name, &id.subject, &email).await {
        Ok(u) => u,
        Err(e) => return internal_error("link the OIDC identity", &e),
    };
    s.auth.start_session(session, &user);
    see_other(&location(&flow.next))
}

#[cfg(test)]
mod tests {
    use super::*;

    // TestOIDCRedirect of oidc_test.go.
    #[test]
    fn oidc_redirect_table() {
        for (p, want) in [
            ("/", true),
            ("/account?tab=profile", true),
            ("", false),
            ("https://evil.test", false),
            ("//evil.test", false),
            ("/\\evil.test", false),
            ("/%2f%2fevil.test", false),
            ("/%5cevil.test", false),
            ("/\nLocation:evil", false),
        ] {
            assert_eq!(redirect_ok(p), want, "{p:?}");
        }
    }

    #[test]
    fn oidc_origin_rules() {
        assert!(origin_ok("http://localhost:5173", false));
        assert!(!origin_ok("http://localhost:5173", true));
        assert!(origin_ok("https://app.example", true));
        assert!(!origin_ok("https://app.example/", true));
        assert!(!origin_ok("https://user@app.example", true));
        assert!(!origin_ok("app.example", false));
    }

    #[test]
    fn oidc_location_is_clean() {
        assert_eq!(location("/account?tab=profile"), "/account?tab=profile");
        assert_eq!(location("/a/./b/../c/"), "/a/c/");
        assert_eq!(location("/a//b?x=/../y"), "/a/b?x=/../y");
        assert_eq!(location("/café"), "/caf%C3%A9");
        assert_eq!(location("/"), "/");
    }

    #[test]
    fn oidc_names() {
        assert!(name_ok("google"));
        assert!(name_ok("a_1"));
        assert!(!name_ok("Bad"));
        assert!(!name_ok("1a"));
        assert!(!name_ok(""));
    }
}
