//! The protocol steps of the OIDC sign-in: discovery, the authorization URL, the code
//! exchange (`oauth2` 5), and the ID token check (`jsonwebtoken` 11 with aws-lc-rs).
//! Go uses `go-oidc` and `golang.org/x/oauth2` for the same steps.

use jsonwebtoken::jwk::{Jwk, JwkSet};
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
use tokio::sync::RwLock;

/// A failure of one protocol step. The routes answer 503 for `Unavailable` and 400 for
/// `Invalid`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum OidcError {
    /// The provider cannot be reached, or its metadata is not valid.
    Unavailable(String),
    /// The answer of the provider is not valid.
    Invalid(String),
}

fn invalid(s: impl std::fmt::Display) -> OidcError {
    OidcError::Invalid(s.to_string())
}

/// The settings of one provider that the steps use.
#[derive(Debug, Clone)]
pub(super) struct Client<'a> {
    pub client_id: &'a str,
    pub secret: &'a str,
    pub redirect_uri: &'a str,
    pub scopes: &'a [String],
}

/// The discovered metadata of a provider. The keys load at the first ID token, as
/// `go-oidc` does.
#[derive(Debug)]
pub(super) struct Discovered {
    issuer: String,
    auth_url: String,
    token_url: String,
    jwks_uri: String,
    algs: Vec<Algorithm>,
    jwks: RwLock<Option<JwkSet>>,
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

/// The asymmetric algorithms that an ID token may use. HMAC is never allowed: the client
/// secret is not a verification key here.
fn asymmetric(alg: &str) -> Option<Algorithm> {
    Some(match alg {
        "RS256" => Algorithm::RS256,
        "RS384" => Algorithm::RS384,
        "RS512" => Algorithm::RS512,
        "PS256" => Algorithm::PS256,
        "PS384" => Algorithm::PS384,
        "PS512" => Algorithm::PS512,
        "ES256" => Algorithm::ES256,
        "ES384" => Algorithm::ES384,
        "EdDSA" => Algorithm::EdDSA,
        _ => return None,
    })
}

async fn get_json<T: serde::de::DeserializeOwned>(
    http: &reqwest::Client,
    url: &str,
) -> Result<T, String> {
    let res = http.get(url).send().await.map_err(|e| e.to_string())?;
    if !res.status().is_success() {
        return Err(format!("{url}: {}", res.status()));
    }
    res.json().await.map_err(|e| e.to_string())
}

/// Reads the discovery document of `issuer`. The stated issuer must equal `issuer`.
pub(super) async fn discover(
    http: &reqwest::Client,
    issuer: &str,
) -> Result<Discovered, OidcError> {
    let url = format!(
        "{}/.well-known/openid-configuration",
        issuer.trim_end_matches('/')
    );
    let m: Metadata = get_json(http, &url).await.map_err(OidcError::Unavailable)?;
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
    Ok(Discovered {
        issuer: m.issuer,
        auth_url: m.authorization_endpoint,
        token_url: m.token_endpoint,
        jwks_uri: m.jwks_uri,
        algs,
        jwks: RwLock::new(None),
    })
}

/// The ID token field of the token answer.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub(super) struct IdTokenField {
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

fn client(d: &Discovered, c: &Client<'_>) -> Result<OAuthClient, OidcError> {
    let bad = |e: url::ParseError| OidcError::Unavailable(e.to_string());
    let o = oauth2::Client::new(ClientId::new(c.client_id.to_owned()))
        .set_auth_uri(AuthUrl::new(d.auth_url.clone()).map_err(bad)?)
        .set_token_uri(TokenUrl::new(d.token_url.clone()).map_err(bad)?)
        .set_redirect_uri(RedirectUrl::new(c.redirect_uri.to_owned()).map_err(bad)?);
    Ok(if c.secret.is_empty() {
        o
    } else {
        o.set_client_secret(ClientSecret::new(c.secret.to_owned()))
    })
}

/// The authorization URL with `state`, `nonce`, and the S256 challenge of `verifier`.
pub(super) fn authorize_url(
    d: &Discovered,
    c: &Client<'_>,
    state: &str,
    nonce: &str,
    verifier: &str,
) -> Result<String, OidcError> {
    let state = state.to_owned();
    let (url, _) = client(d, c)?
        .authorize_url(move || CsrfToken::new(state))
        .add_scopes(c.scopes.iter().cloned().map(Scope::new))
        .add_extra_param("nonce", nonce)
        .set_pkce_challenge(PkceCodeChallenge::from_code_verifier_sha256(
            &PkceCodeVerifier::new(verifier.to_owned()),
        ))
        .url();
    Ok(url.to_string())
}

/// An HTTP error of the oauth2 adapter.
#[derive(Debug)]
pub(super) struct HttpError(String);

impl std::fmt::Display for HttpError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for HttpError {}

/// Sends an oauth2 request with reqwest 0.13. oauth2 5 has a built-in client only for
/// reqwest 0.12, so this adapter keeps one reqwest version.
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
pub(super) async fn exchange(
    http: &reqwest::Client,
    d: &Discovered,
    c: &Client<'_>,
    code: &str,
    verifier: &str,
) -> Result<String, OidcError> {
    let h = http.clone();
    let call = move |req: oauth2::HttpRequest| send(h.clone(), req);
    let token = client(d, c)?
        .exchange_code(AuthorizationCode::new(code.to_owned()))
        .set_pkce_verifier(PkceCodeVerifier::new(verifier.to_owned()))
        .request_async(&call)
        .await
        .map_err(invalid)?;
    token
        .extra_fields()
        .id_token
        .clone()
        .ok_or_else(|| invalid("the token answer has no id_token"))
}

/// The identity of a verified ID token.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct Identity {
    /// The `sub` claim.
    pub subject: String,
    /// The `email` claim.
    pub email: String,
    /// `email_verified`: the boolean `true`, or the string `"true"`, as the Go kit
    /// accepts (R0.7). Every other value is false.
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

/// The Go rule for `email_verified`: the boolean `true` or exactly the string `"true"`.
pub(super) fn email_verified(v: Option<&serde_json::Value>) -> bool {
    match v {
        Some(serde_json::Value::Bool(b)) => *b,
        Some(serde_json::Value::String(s)) => s == "true",
        _ => false,
    }
}

async fn load_keys(http: &reqwest::Client, d: &Discovered) -> Result<JwkSet, OidcError> {
    get_json(http, &d.jwks_uri).await.map_err(invalid)
}

/// Verifies the signature, the issuer, the audience, the expiry, and the nonce of `raw`.
/// The keys load at the first token. An unknown key ID loads them again once, because the
/// provider may have rotated its keys.
pub(super) async fn verify_id_token(
    http: &reqwest::Client,
    d: &Discovered,
    c: &Client<'_>,
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
    let find = |set: &JwkSet| -> Option<Jwk> {
        match &header.kid {
            Some(kid) => set.find(kid).cloned(),
            None if set.keys.len() == 1 => set.keys.first().cloned(),
            None => None,
        }
    };
    let cached = d.jwks.read().await.as_ref().map(find);
    let jwk = match cached {
        Some(Some(jwk)) => jwk,
        _ => {
            let fresh = load_keys(http, d).await?;
            let jwk = find(&fresh);
            *d.jwks.write().await = Some(fresh);
            jwk.ok_or_else(|| invalid("no key matches the token"))?
        }
    };
    let key = DecodingKey::from_jwk(&jwk).map_err(invalid)?;
    let mut v = Validation::new(header.alg);
    v.set_audience(&[c.client_id]);
    v.set_issuer(&[&d.issuer]);
    v.set_required_spec_claims(&["exp", "iss", "aud", "sub"]);
    // go-oidc checks the expiry without leeway.
    v.leeway = 0;
    let data = jsonwebtoken::decode::<Claims>(raw, &key, &v).map_err(invalid)?;
    let claims = data.claims;
    let nonce_ok = claims
        .nonce
        .as_deref()
        .is_some_and(|n| bool::from(n.as_bytes().ct_eq(nonce.as_bytes())));
    if !nonce_ok || claims.sub.is_empty() {
        return Err(invalid("the nonce or the subject is not valid"));
    }
    Ok(Identity {
        email_verified: email_verified(claims.email_verified.as_ref()),
        subject: claims.sub,
        email: claims.email.unwrap_or_default(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    // The rule of TestOIDCEmailVerifiedClaim (R0.7).
    #[test]
    fn oidc_email_verified_rule() {
        for (raw, want) in [
            ("true", true),
            (r#""true""#, true),
            ("false", false),
            (r#""false""#, false),
            (r#""TRUE""#, false),
            (r#"" true ""#, false),
            (r#""""#, false),
            ("1", false),
            ("null", false),
            ("{}", false),
            ("[true]", false),
        ] {
            let v: serde_json::Value = serde_json::from_str(raw).unwrap();
            assert_eq!(email_verified(Some(&v)), want, "{raw}");
        }
        assert!(!email_verified(None));
    }
}
