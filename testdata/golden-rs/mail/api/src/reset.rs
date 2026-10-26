//! Password reset routes and the mail templates of the application.

use std::sync::Arc;

/// Returns the reset routes with the trusted public origin and application mailer.
pub fn routes(
    auth: &kit::auth::Auth,
    mailers: &crate::mail::Mailers,
    public_origin: &str,
) -> anyhow::Result<axum::Router> {
    let templates = kit::mail::Templates::new([
        (
            "password_reset.subject.tmpl",
            include_str!("../templates/mail/password_reset.subject.tmpl"),
        ),
        (
            "password_reset.txt.tmpl",
            include_str!("../templates/mail/password_reset.txt.tmpl"),
        ),
        (
            "password_reset.html.tmpl",
            include_str!("../templates/mail/password_reset.html.tmpl"),
        ),
    ]);
    Ok(kit::auth::reset::routes(
        auth,
        kit::auth::reset::ResetOptions {
            mailer: Some(Arc::new(mailers.clone())),
            from: mailers.from.clone(),
            base_url: format!("{}/reset", public_origin.trim_end_matches('/')),
            templates: Some(templates),
        },
    )?)
}
