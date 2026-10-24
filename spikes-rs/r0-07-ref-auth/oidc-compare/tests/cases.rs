//! The shared OIDC case tables against openidconnect 4. A case "passes" when
//! the sign-in result equals the Go kit result. The test prints each
//! difference and asserts the known set, so a change of the crate shows up.

use kit::auth::fake_issuer::{
    CLIENT_ID, CLIENT_SECRET, EMAIL_VERIFIED_CASES, FakeIssuer, TOKEN_CASES,
};
use oidc_compare::{Identity, Provider};

async fn sign_in(fake: &FakeIssuer) -> Result<Identity, String> {
    let p = Provider::discover(
        fake.url(),
        CLIENT_ID,
        CLIENT_SECRET,
        "http://app.test/api/auth/oidc/test/callback",
    )
    .await?;
    let verifier = "v".repeat(43);
    let url = p.authorize_url("state-1", "nonce-1", &verifier);
    let callback = fake.authorize(&url).await?;
    let q = kit::auth::oidc::query_map(&callback);
    p.sign_in(&q["code"], &verifier, "nonce-1").await
}

#[tokio::test]
async fn token_cases_match_the_go_kit() {
    for (name, mutate, want) in TOKEN_CASES {
        let fake = FakeIssuer::start().await;
        if let Some(m) = mutate {
            fake.set(*m);
        }
        let got = sign_in(&fake).await;
        let ok = got.as_ref().is_ok_and(|id| id.email_verified);
        assert_eq!(ok, *want, "{name}: {got:?}");
    }
}

#[tokio::test]
async fn email_verified_cases() {
    let mut differ = Vec::new();
    for (name, raw, want) in EMAIL_VERIFIED_CASES {
        let fake = FakeIssuer::start().await;
        let v: serde_json::Value = serde_json::from_str(raw).unwrap();
        fake.set(move |k| k.email_verified = Some(v));
        let got = sign_in(&fake).await;
        let ok = got.as_ref().is_ok_and(|id| id.email_verified);
        if ok != *want {
            println!("differs from Go: {name} ({raw}): {got:?}");
            differ.push(*name);
        }
    }
    // openidconnect reads email_verified as a JSON boolean. Without the
    // accept-string-booleans feature, the string "true" fails the whole
    // token. The Go kit accepts it (some providers send strings).
    assert_eq!(differ, ["string true"]);
}
