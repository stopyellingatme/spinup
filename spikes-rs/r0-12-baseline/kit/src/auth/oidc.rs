//! Generic OpenID Connect sign-in (ADR 0013 §3), as `kit/auth/oidc.go`,
//! with `oauth2` 5 (authorization URL, PKCE, code exchange) and
//! `jsonwebtoken` 11 with the aws-lc-rs backend (ID token verification).
//!
//! The authorization code flow keeps `state`, `nonce`, the PKCE verifier,
//! and the return path in a short-lived cookie, sealed with AES-256-GCM.
//! Configuration errors never stop the app: the provider routes answer 503.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use aws_lc_rs::aead::{AES_256_GCM, Aad, LessSafeKey, NONCE_LEN, Nonce, UnboundKey};
use axum::extract::{Path, RawQuery, State};
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router, middleware};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use jsonwebtoken::jwk::JwkSet;
use jsonwebtoken::{Algorithm, DecodingKey, Validation};
use oauth2::basic::{
    BasicErrorResponse, BasicRevocationErrorResponse, BasicTokenIntrospectionResponse,
    BasicTokenType,
};
use oauth2::{
    AuthUrl, AuthorizationCode, ClientId, ClientSecret, CsrfToken, EndpointNotSet, EndpointSet,
    ExtraTokenFields, PkceCodeChallenge, PkceCodeVerifier, RedirectUrl, Scope,
    StandardRevocableToken, StandardTokenResponse, TokenUrl,
};
use serde::{Deserialize, Serialize};
use subtle::ConstantTimeEq;
use tokio::sync::{Mutex, RwLock};
use tower_sessions::Session;

use super::handlers::endpoint;
use super::{Auth, User, internal_error, normalize_email, queries};
use crate::problem::Problem;

/// The lifetime of a sign-in flow (the flow cookie).
pub const OIDC_LIFETIME: Duration = Duration::from_secs(600);

/// An OIDC failure. The kit answers every kind with 400, except
/// `Unavailable` (503).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OidcError {
    /// The provider cannot be reached, or its metadata is not valid.
    Unavailable(String),
    /// The response of the provider is not valid.
    Invalid(String),
}

impl std::fmt::Display for OidcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Unavailable(s) => write!(f, "oidc: provider unavailable: {s}"),
            Self::Invalid(s) => write!(f, "oidc: invalid response: {s}"),
        }
    }
}

impl std::error::Error for OidcError {}

fn invalid(s: impl std::fmt::Display) -> OidcError {
    OidcError::Invalid(s.to_string())
}

/// The static settings of one provider.
#[derive(Debug, Clone)]
pub struct ProviderConfig {
    /// The issuer URL.
    pub issuer: String,
    /// The client ID.
    pub client_id: String,
    /// The client secret. A public client has none.
    pub secret: Option<String>,
    /// The callback URL.
    pub redirect_uri: String,
    /// The scopes, with `openid` first.
    pub scopes: Vec<String>,
}

/// The discovered metadata of a provider, with its keys.
#[derive(Debug)]
pub struct Discovered {
    /// The issuer that the provider states.
    pub issuer: String,
    auth_url: String,
    token_url: String,
    jwks_uri: String,
    algs: Vec<Algorithm>,
    jwks: RwLock<JwkSet>,
}

#[derive(Deserialize)]
struct Metadata {
    issuer: String,
    authorization_endpoint: String,
    token_endpoint: String,
    jwks_uri: String,
    #[serde(default)]
    id_token_signing_alg_values_supported: Vec<String>,
}

/// The asymmetric algorithms that an ID token may use. HMAC is never
/// allowed: the client secret is not a verification key here.
fn asymmetric(alg: &str) -> Option<Algorithm> {
    use Algorithm::*;
    Some(match alg {
        "RS256" => RS256,
        "RS384" => RS384,
        "RS512" => RS512,
        "PS256" => PS256,
        "PS384" => PS384,
        "PS512" => PS512,
        "ES256" => ES256,
        "ES384" => ES384,
        "EdDSA" => EdDSA,
        _ => return None,
    })
}

async fn get_json<T: serde::de::DeserializeOwned>(
    http: &reqwest::Client,
    url: &str,
) -> Result<T, OidcError> {
    let res = http
        .get(url)
        .send()
        .await
        .map_err(|e| OidcError::Unavailable(e.to_string()))?;
    if !res.status().is_success() {
        return Err(OidcError::Unavailable(format!("{url}: {}", res.status())));
    }
    res.json()
        .await
        .map_err(|e| OidcError::Unavailable(e.to_string()))
}

/// Reads the discovery document and the keys of `issuer`. The stated issuer
/// must equal `issuer`.
pub async fn discover(http: &reqwest::Client, issuer: &str) -> Result<Discovered, OidcError> {
    let url = format!(
        "{}/.well-known/openid-configuration",
        issuer.trim_end_matches('/')
    );
    let m: Metadata = get_json(http, &url).await?;
    if m.issuer != issuer {
        return Err(OidcError::Unavailable(format!(
            "the provider states the issuer {:?}, want {issuer:?}",
            m.issuer
        )));
    }
    let mut algs: Vec<Algorithm> = m
        .id_token_signing_alg_values_supported
        .iter()
        .filter_map(|a| asymmetric(a))
        .collect();
    if algs.is_empty() {
        algs.push(Algorithm::RS256);
    }
    let jwks: JwkSet = get_json(http, &m.jwks_uri).await?;
    Ok(Discovered {
        issuer: m.issuer,
        auth_url: m.authorization_endpoint,
        token_url: m.token_endpoint,
        jwks_uri: m.jwks_uri,
        algs,
        jwks: RwLock::new(jwks),
    })
}

/// The ID token field of the token response.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IdTokenField {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    id_token: Option<String>,
}

impl ExtraTokenFields for IdTokenField {}

type TokenResponse = StandardTokenResponse<IdTokenField, BasicTokenType>;
type OAuthClient = oauth2::Client<
    BasicErrorResponse,
    TokenResponse,
    BasicTokenIntrospectionResponse,
    StandardRevocableToken,
    BasicRevocationErrorResponse,
    EndpointSet,
    EndpointNotSet,
    EndpointNotSet,
    EndpointNotSet,
    EndpointSet,
>;

fn client(d: &Discovered, p: &ProviderConfig) -> Result<OAuthClient, OidcError> {
    let bad = |e: url::ParseError| OidcError::Unavailable(e.to_string());
    let c = oauth2::Client::new(ClientId::new(p.client_id.clone()))
        .set_auth_uri(AuthUrl::new(d.auth_url.clone()).map_err(bad)?)
        .set_token_uri(TokenUrl::new(d.token_url.clone()).map_err(bad)?)
        .set_redirect_uri(RedirectUrl::new(p.redirect_uri.clone()).map_err(bad)?);
    Ok(match &p.secret {
        Some(s) => c.set_client_secret(ClientSecret::new(s.clone())),
        None => c,
    })
}

/// The authorization URL with `state`, `nonce`, and the S256 challenge of
/// `verifier`.
pub fn authorize_url(
    d: &Discovered,
    p: &ProviderConfig,
    state: &str,
    nonce: &str,
    verifier: &str,
) -> Result<String, OidcError> {
    let state = state.to_owned();
    let (url, _) = client(d, p)?
        .authorize_url(move || CsrfToken::new(state))
        .add_scopes(p.scopes.iter().cloned().map(Scope::new))
        .add_extra_param("nonce", nonce)
        .set_pkce_challenge(PkceCodeChallenge::from_code_verifier_sha256(
            &PkceCodeVerifier::new(verifier.to_owned()),
        ))
        .url();
    Ok(url.to_string())
}

/// An HTTP error of the oauth2 adapter.
#[derive(Debug)]
pub struct HttpError(String);

impl std::fmt::Display for HttpError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for HttpError {}

/// Sends an oauth2 request with reqwest 0.13. oauth2 5 has a built-in
/// client only for reqwest 0.12, so this adapter keeps one reqwest version.
async fn send(
    http: reqwest::Client,
    req: oauth2::HttpRequest,
) -> Result<oauth2::HttpResponse, HttpError> {
    let (parts, body) = req.into_parts();
    let res = http
        .request(parts.method, parts.uri.to_string())
        .headers(parts.headers)
        .body(body)
        .send()
        .await
        .map_err(|e| HttpError(e.to_string()))?;
    let mut out = oauth2::http::Response::builder().status(res.status());
    if let Some(h) = out.headers_mut() {
        *h = res.headers().clone();
    }
    let bytes = res.bytes().await.map_err(|e| HttpError(e.to_string()))?;
    out.body(bytes.to_vec())
        .map_err(|e| HttpError(e.to_string()))
}

/// Exchanges `code` with the PKCE verifier and returns the raw ID token.
pub async fn exchange(
    http: &reqwest::Client,
    d: &Discovered,
    p: &ProviderConfig,
    code: &str,
    verifier: &str,
) -> Result<String, OidcError> {
    let c = http.clone();
    let call = move |req: oauth2::HttpRequest| send(c.clone(), req);
    let token = client(d, p)?
        .exchange_code(AuthorizationCode::new(code.to_owned()))
        .set_pkce_verifier(PkceCodeVerifier::new(verifier.to_owned()))
        .request_async(&call)
        .await
        .map_err(invalid)?;
    use oauth2::TokenResponse as _;
    let _ = token.access_token();
    token
        .extra_fields()
        .id_token
        .clone()
        .ok_or_else(|| invalid("the token response has no id_token"))
}

/// The identity of a verified ID token.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Identity {
    /// The `sub` claim.
    pub subject: String,
    /// The `email` claim.
    pub email: String,
    /// `email_verified`: `true`, or the string `"true"` (some providers).
    pub email_verified: bool,
}

#[derive(Deserialize)]
struct Claims {
    sub: String,
    #[serde(default)]
    nonce: Option<String>,
    #[serde(default)]
    email: Option<String>,
    #[serde(default)]
    email_verified: Option<serde_json::Value>,
}

/// Verifies the signature, the issuer, the audience, the expiry, and the
/// nonce of `raw`. An unknown key ID reloads the keys once.
pub async fn verify_id_token(
    http: &reqwest::Client,
    d: &Discovered,
    p: &ProviderConfig,
    raw: &str,
    nonce: &str,
) -> Result<Identity, OidcError> {
    let header = jsonwebtoken::decode_header(raw).map_err(invalid)?;
    if !d.algs.contains(&header.alg) {
        return Err(invalid(format!(
            "the algorithm {:?} is not allowed",
            header.alg
        )));
    }
    let find = |set: &JwkSet| match &header.kid {
        Some(kid) => set.find(kid).cloned(),
        None if set.keys.len() == 1 => set.keys.first().cloned(),
        None => None,
    };
    let mut jwk = find(&*d.jwks.read().await);
    if jwk.is_none() {
        // The provider may have rotated its keys.
        let fresh: JwkSet = get_json(http, &d.jwks_uri).await?;
        jwk = find(&fresh);
        *d.jwks.write().await = fresh;
    }
    let jwk = jwk.ok_or_else(|| invalid("no key matches the token"))?;
    let key = DecodingKey::from_jwk(&jwk).map_err(invalid)?;
    let mut v = Validation::new(header.alg);
    v.set_audience(&[&p.client_id]);
    v.set_issuer(&[&d.issuer]);
    v.set_required_spec_claims(&["exp", "iss", "aud", "sub"]);
    let data = jsonwebtoken::decode::<Claims>(raw, &key, &v).map_err(invalid)?;
    let c = data.claims;
    let nonce_ok = c
        .nonce
        .as_deref()
        .is_some_and(|n| bool::from(n.as_bytes().ct_eq(nonce.as_bytes())));
    if !nonce_ok || c.sub.is_empty() {
        return Err(invalid("the nonce or the subject is not valid"));
    }
    let email_verified = match c.email_verified {
        Some(serde_json::Value::Bool(b)) => b,
        Some(serde_json::Value::String(s)) => s == "true",
        _ => false,
    };
    Ok(Identity {
        subject: c.sub,
        email: c.email.unwrap_or_default(),
        email_verified,
    })
}

// ---------------------------------------------------------------------------
// The HTTP routes.

struct Provider {
    name: String,
    label: String,
    config: ProviderConfig,
    err: bool,
    discovered: Mutex<Option<Arc<Discovered>>>,
}

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
    providers: Vec<Arc<Provider>>,
    key: LessSafeKey,
    err: bool,
    http: reqwest::Client,
}

fn random_token() -> String {
    let mut b = [0u8; 32];
    aws_lc_rs::rand::fill(&mut b).expect("the system random source works");
    B64URL.encode(b)
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

fn origin_ok(raw: &str, secure: bool) -> bool {
    let bare = raw
        .split_once("://")
        .is_some_and(|(_, rest)| !rest.contains(['/', '?', '#', '@']));
    issuer_ok(raw) && bare && (!secure || raw.starts_with("https://"))
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

/// Reports whether `path` is a safe local return path: it starts with one
/// `/`, also after percent-decoding, and has no backslash or control
/// character. Browsers treat backslashes as separators.
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

fn unavailable() -> Response {
    Problem::new(
        StatusCode::SERVICE_UNAVAILABLE,
        "This sign-in provider is unavailable or its configuration is invalid.\nTry: check AUTH_OIDC settings and provider availability, then retry.",
    )
    .into_response()
}

fn invalid_response() -> Response {
    Problem::new(
        StatusCode::BAD_REQUEST,
        "The sign-in response is invalid, expired, or has no verified email.\nTry: start sign-in again with a verified email.",
    )
    .into_response()
}

impl Auth {
    /// The OIDC routes from AUTH_OIDC_PROVIDERS. It reads configuration and
    /// contacts no provider. Without providers it returns an empty router.
    pub fn oidc_routes(&self) -> Router {
        self.oidc_routes_with(
            reqwest::Client::builder()
                .redirect(reqwest::redirect::Policy::none())
                .timeout(Duration::from_secs(15))
                .build()
                .expect("a reqwest client with rustls"),
        )
    }

    /// The OIDC routes with an HTTP client of the caller (tests).
    pub fn oidc_routes_with(&self, http: reqwest::Client) -> Router {
        let names = self.lookup("AUTH_OIDC_PROVIDERS").unwrap_or_default();
        if names.trim().is_empty() {
            return Router::new();
        }
        let origin = self
            .lookup("PUBLIC_ORIGIN")
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| "http://localhost:5173".into());
        let mut err = !origin_ok(&origin, self.secure());
        let mut key = [0u8; 32];
        match self
            .lookup("AUTH_OIDC_COOKIE_KEY")
            .filter(|s| !s.is_empty())
        {
            None => aws_lc_rs::rand::fill(&mut key).expect("random"),
            Some(k) => match B64URL.decode(k) {
                Ok(b) if b.len() == 32 => key.copy_from_slice(&b),
                _ => err = true,
            },
        }
        let key = LessSafeKey::new(UnboundKey::new(&AES_256_GCM, &key).expect("32-byte key"));
        let mut providers = Vec::new();
        let mut seen = std::collections::HashSet::new();
        for name in names.split(',').map(str::trim) {
            let valid = name.starts_with(|c: char| c.is_ascii_lowercase())
                && name
                    .chars()
                    .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_');
            if !valid || !seen.insert(name.to_owned()) {
                err = true;
                continue;
            }
            providers.push(Arc::new(self.load_provider(name, &origin)));
        }
        let state = Arc::new(OidcState {
            auth: self.clone(),
            providers,
            key,
            err,
            http,
        });
        Router::new()
            .route("/api/auth/oidc/providers", get(list))
            .route("/api/auth/oidc/{provider}/start", get(start))
            .route("/api/auth/oidc/{provider}/callback", get(callback))
            .layer(middleware::map_response(endpoint))
            .with_state(state)
    }

    fn load_provider(&self, name: &str, origin: &str) -> Provider {
        let prefix = format!("AUTH_OIDC_{}_", name.to_ascii_uppercase());
        let get = |k: &str| {
            self.lookup(&format!("{prefix}{k}"))
                .filter(|s| !s.is_empty())
        };
        let issuer = get("ISSUER").unwrap_or_default();
        let client_id = get("CLIENT_ID").unwrap_or_default();
        let err = !issuer_ok(&issuer) || client_id.is_empty();
        let mut scopes: Vec<String> = vec!["openid".into(), "email".into(), "profile".into()];
        for s in get("SCOPES")
            .unwrap_or_default()
            .replace(',', " ")
            .split_whitespace()
        {
            if !scopes.iter().any(|x| x == s) {
                scopes.push(s.to_owned());
            }
        }
        Provider {
            name: name.to_owned(),
            label: get("LABEL").unwrap_or_else(|| name.to_owned()),
            config: ProviderConfig {
                issuer,
                client_id,
                secret: get("CLIENT_SECRET"),
                redirect_uri: format!("{origin}/api/auth/oidc/{name}/callback"),
                scopes,
            },
            err,
            discovered: Mutex::new(None),
        }
    }
}

impl OidcState {
    #[allow(clippy::result_large_err)] // the error is the answer itself
    fn provider(&self, name: &str) -> Result<Arc<Provider>, Response> {
        if self.err {
            return Err(unavailable());
        }
        match self.providers.iter().find(|p| p.name == name) {
            Some(p) if p.err => Err(unavailable()),
            Some(p) => Ok(p.clone()),
            None => Err(Problem::new(
                StatusCode::NOT_FOUND,
                "The sign-in provider is unknown.\nTry: choose a configured provider.",
            )
            .into_response()),
        }
    }

    async fn discover(&self, p: &Provider) -> Result<Arc<Discovered>, OidcError> {
        let mut slot = p.discovered.lock().await;
        if let Some(d) = slot.as_ref() {
            return Ok(d.clone());
        }
        let d = Arc::new(discover(&self.http, &p.config.issuer).await?);
        *slot = Some(d.clone());
        Ok(d)
    }

    fn cookie_name(p: &Provider) -> String {
        format!("oidc_{}", p.name)
    }

    fn cookie(&self, p: &Provider, value: &str, max_age: i64) -> HeaderValue {
        let secure = if self.auth.secure() { "; Secure" } else { "" };
        HeaderValue::from_str(&format!(
            "{}={value}; Path=/api/auth/oidc/{}/; Max-Age={max_age}; HttpOnly{secure}; SameSite=Lax",
            Self::cookie_name(p),
            p.name
        ))
        .expect("cookie characters are safe")
    }

    fn seal(&self, p: &Provider, flow: &Flow) -> String {
        let mut nonce = [0u8; NONCE_LEN];
        aws_lc_rs::rand::fill(&mut nonce).expect("random");
        let mut buf = serde_json::to_vec(flow).expect("a flow encodes");
        self.key
            .seal_in_place_append_tag(
                Nonce::assume_unique_for_key(nonce),
                Aad::from(p.name.as_bytes()),
                &mut buf,
            )
            .expect("AES-GCM seal");
        let mut out = nonce.to_vec();
        out.extend(buf);
        B64URL.encode(out)
    }

    fn flow(&self, headers: &HeaderMap, p: &Provider) -> Option<Flow> {
        let name = Self::cookie_name(p);
        let value = headers
            .get_all(header::COOKIE)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .flat_map(|v| v.split(';'))
            .filter_map(|kv| kv.trim().split_once('='))
            .find(|(k, _)| *k == name)
            .map(|(_, v)| v.to_owned())?;
        if value.len() > 4096 {
            return None;
        }
        let data = B64URL.decode(value).ok()?;
        if data.len() < NONCE_LEN {
            return None;
        }
        let (n, sealed) = data.split_at(NONCE_LEN);
        let mut buf = sealed.to_vec();
        let plain = self
            .key
            .open_in_place(
                Nonce::try_assume_unique_for_key(n).ok()?,
                Aad::from(p.name.as_bytes()),
                &mut buf,
            )
            .ok()?;
        let flow: Flow = serde_json::from_slice(plain).ok()?;
        let now = self.auth.now().unix_timestamp();
        let ok = flow.provider == p.name
            && !flow.state.is_empty()
            && !flow.nonce.is_empty()
            && !flow.verifier.is_empty()
            && flow.expires > now
            && flow.expires <= now + OIDC_LIFETIME.as_secs() as i64
            && redirect_ok(&flow.next);
        ok.then_some(flow)
    }
}

async fn list(State(s): State<Arc<OidcState>>) -> Response {
    let out: Vec<_> = s
        .providers
        .iter()
        .map(|p| serde_json::json!({"name": p.name, "label": p.label}))
        .collect();
    Json(out).into_response()
}

fn query_pairs(q: Option<&str>) -> Vec<(String, String)> {
    form_urlencoded::parse(q.unwrap_or("").as_bytes())
        .into_owned()
        .collect()
}

async fn start(
    State(s): State<Arc<OidcState>>,
    Path(name): Path<String>,
    RawQuery(q): RawQuery,
) -> Response {
    let p = match s.provider(&name) {
        Ok(p) => p,
        Err(r) => return r,
    };
    let pairs = query_pairs(q.as_deref());
    let next = pairs
        .iter()
        .find(|(k, _)| k == "next")
        .map(|(_, v)| v.clone())
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| "/account".into());
    if !redirect_ok(&next) || next.len() > 1024 {
        return invalid_response();
    }
    let d = match s.discover(&p).await {
        Ok(d) => d,
        Err(_) => return unavailable(),
    };
    let flow = Flow {
        provider: p.name.clone(),
        state: random_token(),
        nonce: random_token(),
        verifier: random_token(),
        next,
        expires: s.auth.now().unix_timestamp() + OIDC_LIFETIME.as_secs() as i64,
    };
    let location = match authorize_url(&d, &p.config, &flow.state, &flow.nonce, &flow.verifier) {
        Ok(u) => u,
        Err(_) => return unavailable(),
    };
    let mut res = StatusCode::SEE_OTHER.into_response();
    let h = res.headers_mut();
    h.insert(
        header::SET_COOKIE,
        s.cookie(&p, &s.seal(&p, &flow), OIDC_LIFETIME.as_secs() as i64),
    );
    h.insert(
        header::LOCATION,
        HeaderValue::from_str(&location).unwrap_or(HeaderValue::from_static("/")),
    );
    res
}

async fn callback(
    State(s): State<Arc<OidcState>>,
    Path(name): Path<String>,
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
    res.headers_mut()
        .append(header::SET_COOKIE, s.cookie(&p, "", 0));
    res
}

async fn callback_inner(
    s: &OidcState,
    p: &Provider,
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
        return invalid_response();
    };
    if states.len() != 1
        || !bool::from(states[0].as_bytes().ct_eq(flow.state.as_bytes()))
        || errors.first().is_some_and(|e| !e.is_empty())
        || codes.len() != 1
        || codes[0].is_empty()
    {
        return invalid_response();
    }
    let d = match s.discover(p).await {
        Ok(d) => d,
        Err(_) => return unavailable(),
    };
    let id = match exchange(&s.http, &d, &p.config, codes[0], &flow.verifier).await {
        Ok(raw) => verify_id_token(&s.http, &d, &p.config, &raw, &flow.nonce).await,
        Err(e) => Err(e),
    };
    let id = match id {
        Ok(id) => id,
        Err(OidcError::Unavailable(_)) => return unavailable(),
        Err(OidcError::Invalid(why)) => {
            tracing::info!(provider = %p.name, why = %why, "auth: OIDC callback refused");
            return invalid_response();
        }
    };
    let (email, valid) = normalize_email(&id.email);
    if !valid || !id.email_verified {
        return invalid_response();
    }
    let user = match oidc_user(&s.auth, &p.name, &id.subject, &email).await {
        Ok(u) => u,
        Err(e) => return internal_error("link the OIDC identity", &e),
    };
    if let Err(e) = s.auth.start_session(session, &user).await {
        return internal_error("start the OIDC session", &e);
    }
    let mut res = StatusCode::SEE_OTHER.into_response();
    res.headers_mut().insert(
        header::LOCATION,
        HeaderValue::from_str(&flow.next).unwrap_or(HeaderValue::from_static("/")),
    );
    res
}

/// Resolves an identity before its email, so a provider cannot move an
/// identity to another account by changing the email claim. A new identity
/// with the email of an existing account takes over that account: the
/// password and the sessions of the account end.
pub async fn oidc_user(
    auth: &Auth,
    provider: &str,
    subject: &str,
    email: &str,
) -> Result<User, sqlx::Error> {
    let mut tx = queries::begin(auth.pool()).await?;
    // Serialize callbacks of this identity, also callbacks with different emails.
    sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")
        .bind(format!("{provider}:{subject}"))
        .execute(&mut *tx)
        .await?;
    if let Some(u) = sqlx::query_as!(
        User,
        "SELECT u.id, u.email FROM users u JOIN user_identities i ON i.user_id = u.id WHERE i.provider = $1 AND i.subject = $2",
        provider,
        subject
    )
    .fetch_optional(&mut *tx)
    .await?
    {
        tx.commit().await?;
        return Ok(u);
    }
    // The unique email key serializes different identities with one email.
    let inserted = sqlx::query_as!(
        User,
        "INSERT INTO users (email, password_hash) VALUES ($1, NULL) ON CONFLICT (email) DO NOTHING RETURNING id, email",
        email
    )
    .fetch_optional(&mut *tx)
    .await?;
    let user = match inserted {
        Some(u) => u,
        None => {
            let u = sqlx::query_as!(
                User,
                "SELECT id, email FROM users WHERE email = $1 FOR UPDATE",
                email
            )
            .fetch_one(&mut *tx)
            .await?;
            sqlx::query!("UPDATE users SET password_hash = NULL WHERE id = $1", u.id)
                .execute(&mut *tx)
                .await?;
            queries::delete_user_sessions(&mut tx, u.id).await?;
            u
        }
    };
    sqlx::query!(
        "INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)",
        provider,
        subject,
        user.id,
        email
    )
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(user)
}

/// The query pairs of a URL, for tests.
pub fn query_map(url: &str) -> HashMap<String, String> {
    let q = url.split_once('?').map_or("", |(_, q)| q);
    form_urlencoded::parse(q.as_bytes()).into_owned().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    // TestOIDCRedirect of oidc_test.go, unchanged.
    #[test]
    fn redirect_table() {
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
    fn origin_rules() {
        assert!(origin_ok("http://localhost:5173", false));
        assert!(!origin_ok("http://localhost:5173", true));
        assert!(origin_ok("https://app.example", true));
        assert!(!origin_ok("https://app.example/", true));
        assert!(!origin_ok("https://user@app.example", true));
    }
}
