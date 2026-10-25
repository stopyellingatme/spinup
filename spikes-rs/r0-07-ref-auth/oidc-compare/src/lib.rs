//! The openidconnect 4 candidate of the R0.7 OIDC comparison. It does the
//! same steps as `kit::auth::oidc` (discovery, authorization URL with PKCE
//! and nonce, code exchange, ID token verification), so the tests can run
//! the shared case tables of `kit::auth::fake_issuer` against both crates.

use openidconnect::core::{CoreAuthenticationFlow, CoreClient, CoreProviderMetadata};
use openidconnect::reqwest;
use openidconnect::{
    AuthorizationCode, ClientId, ClientSecret, CsrfToken, IssuerUrl, Nonce, PkceCodeChallenge,
    PkceCodeVerifier, RedirectUrl, Scope, TokenResponse,
};

/// The identity of a verified ID token.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Identity {
    /// `sub`.
    pub subject: String,
    /// `email`.
    pub email: String,
    /// `email_verified`. openidconnect reads it as a JSON boolean only.
    pub email_verified: bool,
}

/// One provider for the comparison.
pub struct Provider {
    client: CoreClient<
        openidconnect::EndpointSet,
        openidconnect::EndpointNotSet,
        openidconnect::EndpointNotSet,
        openidconnect::EndpointNotSet,
        openidconnect::EndpointMaybeSet,
        openidconnect::EndpointMaybeSet,
    >,
    http: reqwest::Client,
}

impl Provider {
    /// Discovers `issuer`. openidconnect uses reqwest 0.12 through oauth2.
    pub async fn discover(
        issuer: &str,
        client_id: &str,
        secret: &str,
        redirect: &str,
    ) -> Result<Self, String> {
        let http = reqwest::ClientBuilder::new()
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .map_err(|e| e.to_string())?;
        let meta = CoreProviderMetadata::discover_async(
            IssuerUrl::new(issuer.into()).map_err(|e| e.to_string())?,
            &http,
        )
        .await
        .map_err(|e| e.to_string())?;
        let client = CoreClient::from_provider_metadata(
            meta,
            ClientId::new(client_id.into()),
            Some(ClientSecret::new(secret.into())),
        )
        .set_redirect_uri(RedirectUrl::new(redirect.into()).map_err(|e| e.to_string())?);
        Ok(Self { client, http })
    }

    /// The authorization URL with `state`, `nonce`, and the S256 challenge.
    pub fn authorize_url(&self, state: &str, nonce: &str, verifier: &str) -> String {
        let (state, nonce) = (state.to_owned(), nonce.to_owned());
        let (url, _, _) = self
            .client
            .authorize_url(
                CoreAuthenticationFlow::AuthorizationCode,
                move || CsrfToken::new(state),
                move || Nonce::new(nonce),
            )
            .add_scope(Scope::new("email".into()))
            .add_scope(Scope::new("profile".into()))
            .set_pkce_challenge(PkceCodeChallenge::from_code_verifier_sha256(
                &PkceCodeVerifier::new(verifier.into()),
            ))
            .url();
        url.to_string()
    }

    /// Exchanges the code and verifies the ID token.
    pub async fn sign_in(
        &self,
        code: &str,
        verifier: &str,
        nonce: &str,
    ) -> Result<Identity, String> {
        let token = self
            .client
            .exchange_code(AuthorizationCode::new(code.into()))
            .map_err(|e| e.to_string())?
            .set_pkce_verifier(PkceCodeVerifier::new(verifier.into()))
            .request_async(&self.http)
            .await
            .map_err(|e| format!("{e:?}"))?;
        let id = token.id_token().ok_or("no id_token")?;
        let claims = id
            .claims(&self.client.id_token_verifier(), &Nonce::new(nonce.into()))
            .map_err(|e| e.to_string())?;
        Ok(Identity {
            subject: claims.subject().to_string(),
            email: claims.email().map(|e| e.to_string()).unwrap_or_default(),
            email_verified: claims.email_verified().unwrap_or(false),
        })
    }
}
