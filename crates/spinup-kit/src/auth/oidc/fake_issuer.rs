//! A local OIDC provider for tests, as `oidcTestProvider` in `kit/auth/oidc_test.go`. It
//! serves real discovery, JWKS, authorization, and token endpoints on a 127.0.0.1 port,
//! and signs RS256 ID tokens with a key that it makes at start. Tests verify real
//! signatures. The ID token has the subject `subject-1` and the email
//! `person@example.com`, unless the [`Knobs`] change them.
//!
//! ```no_run
//! # async fn demo() {
//! use spinup_kit::auth::oidc::fake_issuer::{CLIENT_ID, CLIENT_SECRET, FakeIssuer};
//!
//! let fake = FakeIssuer::start().await;
//! let env = [
//!     ("AUTH_OIDC_PROVIDERS", "test".to_owned()),
//!     ("AUTH_OIDC_TEST_ISSUER", fake.url().to_owned()),
//!     ("AUTH_OIDC_TEST_CLIENT_ID", CLIENT_ID.to_owned()),
//!     ("AUTH_OIDC_TEST_CLIENT_SECRET", CLIENT_SECRET.to_owned()),
//! ];
//! # let _ = env;
//! fake.set(|k| k.unverified = true);
//! # }
//! ```

use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use aws_lc_rs::rand::SystemRandom;
use aws_lc_rs::rsa::KeySize;
use aws_lc_rs::signature::{KeyPair, RSA_PKCS1_SHA256, RsaKeyPair};
use axum::extract::{Form, RawQuery, State};
use axum::http::{StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use tokio::task::JoinHandle;

/// The client ID that the fake provider expects.
pub const CLIENT_ID: &str = "client";
/// The client secret.
pub const CLIENT_SECRET: &str = "secret";
/// The default subject of the ID tokens.
pub const SUBJECT: &str = "subject-1";
/// The default email of the ID tokens.
pub const EMAIL: &str = "person@example.com";

/// The knobs of a test case. They change the next ID token.
#[derive(Debug, Clone, Default)]
pub struct Knobs {
    /// A nonce that replaces the nonce of the request.
    pub nonce: Option<String>,
    /// An issuer that replaces the real one.
    pub issuer: Option<String>,
    /// An audience that replaces the client ID.
    pub audience: Option<String>,
    /// A raw `email_verified` value.
    pub email_verified: Option<Value>,
    /// The token expired an hour ago.
    pub expired: bool,
    /// `email_verified` is false.
    pub unverified: bool,
    /// The token response has no `id_token`.
    pub missing_token: bool,
    /// One bit of the signature is flipped.
    pub bad_signature: bool,
    /// A subject that replaces `subject-1`.
    pub subject: Option<String>,
    /// An email that replaces `person@example.com`.
    pub email: Option<String>,
}

struct Inner {
    url: String,
    key: RsaKeyPair,
    knobs: Mutex<Knobs>,
    grants: Mutex<HashMap<String, HashMap<String, String>>>,
    discovery: AtomicUsize,
    tokens: AtomicUsize,
}

/// The fake provider.
pub struct FakeIssuer {
    inner: Arc<Inner>,
    task: JoinHandle<()>,
}

impl FakeIssuer {
    /// Starts the provider on a free loopback port.
    pub async fn start() -> Self {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind");
        let url = format!("http://{}", listener.local_addr().expect("addr"));
        let key = RsaKeyPair::generate(KeySize::Rsa2048).expect("RSA key");
        let inner = Arc::new(Inner {
            url,
            key,
            knobs: Mutex::default(),
            grants: Mutex::default(),
            discovery: AtomicUsize::new(0),
            tokens: AtomicUsize::new(0),
        });
        let app = Router::new()
            .route("/.well-known/openid-configuration", get(discovery))
            .route("/jwks", get(jwks))
            .route("/authorize", get(authorize))
            .route("/token", post(token))
            .with_state(inner.clone());
        let task = tokio::spawn(async move {
            let _ = axum::serve(listener, app).await;
        });
        Self { inner, task }
    }

    /// The issuer URL.
    pub fn url(&self) -> &str {
        &self.inner.url
    }

    /// Changes the knobs.
    pub fn set(&self, f: impl FnOnce(&mut Knobs)) {
        f(&mut self.inner.knobs.lock().expect("knobs"));
    }

    /// The number of discovery requests.
    pub fn discoveries(&self) -> usize {
        self.inner.discovery.load(Ordering::SeqCst)
    }

    /// The number of token requests.
    pub fn token_requests(&self) -> usize {
        self.inner.tokens.load(Ordering::SeqCst)
    }

    /// Stops the provider. Later requests fail to connect.
    pub fn close(&self) {
        self.task.abort();
    }

    /// Follows an authorization URL as a browser would, and returns the
    /// callback URL with `code` and `state`.
    pub async fn authorize(&self, auth_url: &str) -> Result<String, String> {
        let http = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .map_err(|e| e.to_string())?;
        let res = http.get(auth_url).send().await.map_err(|e| e.to_string())?;
        if res.status() != StatusCode::SEE_OTHER {
            return Err(format!("authorize: {}", res.status()));
        }
        res.headers()
            .get(header::LOCATION)
            .and_then(|v| v.to_str().ok())
            .map(str::to_owned)
            .ok_or_else(|| "authorize: no Location".into())
    }
}

impl Drop for FakeIssuer {
    fn drop(&mut self) {
        self.task.abort();
    }
}

async fn discovery(State(s): State<Arc<Inner>>) -> Json<Value> {
    s.discovery.fetch_add(1, Ordering::SeqCst);
    let u = &s.url;
    Json(json!({
        "issuer": u,
        "authorization_endpoint": format!("{u}/authorize"),
        "token_endpoint": format!("{u}/token"),
        "jwks_uri": format!("{u}/jwks"),
        "response_types_supported": ["code"],
        "subject_types_supported": ["public"],
        "id_token_signing_alg_values_supported": ["RS256"],
    }))
}

async fn jwks(State(s): State<Arc<Inner>>) -> Json<Value> {
    let (n, e) = rsa_public_components(s.key.public_key().as_ref());
    Json(json!({"keys": [{
        "kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
        "n": B64URL.encode(n), "e": B64URL.encode(e),
    }]}))
}

/// Reads n and e from a DER RSAPublicKey (RFC 8017): SEQUENCE { INTEGER n,
/// INTEGER e }. The aws-lc-rs accessors need the `ring-io` feature, which
/// needs a crate that is not in the offline cache.
fn rsa_public_components(der: &[u8]) -> (&[u8], &[u8]) {
    fn read(b: &[u8], tag: u8) -> (&[u8], &[u8]) {
        assert_eq!(b[0], tag, "DER tag");
        let (len, start) = if b[1] & 0x80 == 0 {
            (usize::from(b[1]), 2)
        } else {
            let n = usize::from(b[1] & 0x7f);
            (
                b[2..2 + n].iter().fold(0, |a, &x| a << 8 | usize::from(x)),
                2 + n,
            )
        };
        (&b[start..start + len], &b[start + len..])
    }
    let (seq, _) = read(der, 0x30);
    let (n, rest) = read(seq, 0x02);
    let (e, _) = read(rest, 0x02);
    (n.strip_prefix(&[0]).unwrap_or(n), e)
}

fn random_code() -> String {
    let mut b = [0u8; 24];
    aws_lc_rs::rand::fill(&mut b).expect("random");
    B64URL.encode(b)
}

async fn authorize(State(s): State<Arc<Inner>>, RawQuery(q): RawQuery) -> Response {
    let q: HashMap<String, String> = form_urlencoded::parse(q.unwrap_or_default().as_bytes())
        .into_owned()
        .collect();
    let has = |k: &str| q.get(k).is_some_and(|v| !v.is_empty());
    if q.get("code_challenge_method").map(String::as_str) != Some("S256")
        || !has("code_challenge")
        || !has("nonce")
        || !has("state")
        || q.get("response_type").map(String::as_str) != Some("code")
    {
        return (StatusCode::BAD_REQUEST, "missing OIDC safeguards").into_response();
    }
    let code = random_code();
    let mut callback = url::Url::parse(&q["redirect_uri"]).expect("redirect_uri");
    callback
        .query_pairs_mut()
        .clear()
        .append_pair("code", &code)
        .append_pair("state", &q["state"]);
    s.grants.lock().expect("grants").insert(code, q);
    (
        StatusCode::SEE_OTHER,
        [(header::LOCATION, callback.to_string())],
    )
        .into_response()
}

async fn token(State(s): State<Arc<Inner>>, Form(f): Form<HashMap<String, String>>) -> Response {
    s.tokens.fetch_add(1, Ordering::SeqCst);
    let code = f.get("code").cloned().unwrap_or_default();
    let Some(grant) = s.grants.lock().expect("grants").remove(&code) else {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({"error": "invalid_grant"})),
        )
            .into_response();
    };
    let verifier = f.get("code_verifier").cloned().unwrap_or_default();
    let challenge = B64URL.encode(Sha256::digest(verifier.as_bytes()));
    if Some(&challenge) != grant.get("code_challenge")
        || f.get("redirect_uri") != grant.get("redirect_uri")
    {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({"error": "invalid_grant"})),
        )
            .into_response();
    }
    let k = s.knobs.lock().expect("knobs").clone();
    let now = time::OffsetDateTime::now_utc().unix_timestamp();
    let verified = k
        .email_verified
        .clone()
        .unwrap_or(Value::Bool(!k.unverified));
    let claims = json!({
        "iss": k.issuer.clone().unwrap_or_else(|| s.url.clone()),
        "aud": k.audience.clone().unwrap_or_else(|| CLIENT_ID.into()),
        "sub": k.subject.clone().unwrap_or_else(|| SUBJECT.into()),
        "exp": if k.expired { now - 3600 } else { now + 3600 },
        "iat": now,
        "nonce": k.nonce.clone().unwrap_or_else(|| grant["nonce"].clone()),
        "email": k.email.clone().unwrap_or_else(|| EMAIL.into()),
        "email_verified": verified,
    });
    let head = B64URL.encode(br#"{"alg":"RS256","kid":"test","typ":"JWT"}"#);
    let body = B64URL.encode(claims.to_string());
    let signing_input = format!("{head}.{body}");
    let mut sig = vec![0u8; s.key.public_modulus_len()];
    s.key
        .sign(
            &RSA_PKCS1_SHA256,
            &SystemRandom::new(),
            signing_input.as_bytes(),
            &mut sig,
        )
        .expect("sign");
    if k.bad_signature {
        sig[0] ^= 1;
    }
    let mut out = json!({"access_token": "access", "token_type": "Bearer", "expires_in": 3600});
    if !k.missing_token {
        out["id_token"] = Value::String(format!("{signing_input}.{}", B64URL.encode(&sig)));
    }
    Json(out).into_response()
}
