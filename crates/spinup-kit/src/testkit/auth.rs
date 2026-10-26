//! Signed-in test users (Go `kit/auth/authtest`, ADR 0015 §1).
//!
//! Wrap the routes of a handler test with [`as_user`]. The handlers then read the user
//! with [`crate::auth::AuthUser`], without a session or a database.
//!
//! ```
//! # tokio::runtime::Runtime::new().unwrap().block_on(async {
//! use axum::Router;
//! use axum::routing::get;
//! use spinup_kit::auth::AuthUser;
//! use spinup_kit::testkit::auth::as_user;
//! use spinup_kit::testkit::http::{Req, send};
//!
//! let routes = Router::new().route("/me", get(|AuthUser(u): AuthUser| async move { u.id.to_string() }));
//! let answer = send(&as_user(routes, 42), Req::get("/me")).await;
//! assert_eq!(answer.body, "42");
//! # });
//! ```

use axum::Router;
use axum::extract::Request;
use axum::middleware::{self, Next};
use axum::response::Response;

use crate::auth::User;

/// Serves `router` with the user `user_id` signed in (Go `authtest.As`). The user has an
/// empty email.
pub fn as_user<S>(router: Router<S>, user_id: i64) -> Router<S>
where
    S: Clone + Send + Sync + 'static,
{
    as_user_with(
        router,
        User {
            id: user_id,
            email: String::new(),
        },
    )
}

/// Serves `router` with `user` signed in.
pub fn as_user_with<S>(router: Router<S>, user: User) -> Router<S>
where
    S: Clone + Send + Sync + 'static,
{
    router.layer(middleware::from_fn(move |mut req: Request, next: Next| {
        let user = user.clone();
        async move {
            req.extensions_mut().insert(user);
            let res: Response = next.run(req).await;
            res
        }
    }))
}
