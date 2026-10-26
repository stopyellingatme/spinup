//! The session of one request (Go scs `sessionData`). The auth layer loads it before the
//! handler and commits it after the handler. Handlers change it with plain methods, so
//! no handler waits for the store.

use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use axum::extract::{FromRequestParts, OptionalFromRequestParts};
use axum::http::request::Parts;
use axum::response::Response;
use serde::de::DeserializeOwned;
use time::OffsetDateTime;

use super::{Record, new_token};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Status {
    Unmodified,
    Modified,
    Destroyed,
}

#[derive(Debug)]
struct State {
    /// The token of the cookie. `None` for a new session until the commit.
    token: Option<String>,
    /// Tokens whose store rows the commit deletes (after a renew or a destroy).
    stale: Vec<String>,
    record: Record,
    status: Status,
    lifetime: Duration,
}

/// The session of a request. The auth layer puts it in the request extensions. As an
/// extractor it answers 500 when the auth layer is missing. Clones share one session.
#[derive(Debug, Clone)]
pub struct Session(Arc<Mutex<State>>);

/// What the commit of a session must do.
#[derive(Debug, Clone, PartialEq)]
pub enum SessionChange {
    /// Nothing changed: no store write and no cookie.
    None,
    /// Write the record under the token, then send the cookie.
    Save {
        /// The cookie token.
        token: String,
        /// The session data.
        record: Record,
        /// Store rows to delete first.
        stale: Vec<String>,
    },
    /// Delete the rows, then send an expired cookie.
    Destroy {
        /// Store rows to delete.
        stale: Vec<String>,
    },
}

fn deadline_after(lifetime: Duration) -> OffsetDateTime {
    let lifetime = time::Duration::try_from(lifetime).unwrap_or(time::Duration::WEEK);
    OffsetDateTime::now_utc().saturating_add(lifetime)
}

impl Session {
    /// Returns a new empty session with a deadline of now plus `lifetime`.
    pub fn new(lifetime: Duration) -> Self {
        Self::with_state(None, None, lifetime)
    }

    /// Returns the session that the store holds for `token`. With an idle timeout, the
    /// commit writes it again, so its expiry moves (Go scs `Load`).
    pub fn loaded(token: String, record: Record, lifetime: Duration, idle: bool) -> Self {
        let s = Self::with_state(Some(token), Some(record), lifetime);
        if idle {
            s.lock().status = Status::Modified;
        }
        s
    }

    fn with_state(token: Option<String>, record: Option<Record>, lifetime: Duration) -> Self {
        let record = record.unwrap_or_else(|| Record {
            deadline: deadline_after(lifetime),
            values: serde_json::Map::new(),
        });
        Self(Arc::new(Mutex::new(State {
            token,
            stale: Vec::new(),
            record,
            status: Status::Unmodified,
            lifetime,
        })))
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        // A panic while the lock is held leaves plain data, which stays usable.
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    /// The value of `key`, or `None` when it is missing or has another type.
    pub fn get<T: DeserializeOwned>(&self, key: &str) -> Option<T> {
        let v = self.lock().record.values.get(key).cloned()?;
        serde_json::from_value(v).ok()
    }

    /// Sets `key` to `value`.
    pub fn insert(&self, key: &str, value: impl Into<serde_json::Value>) {
        let mut s = self.lock();
        s.record.values.insert(key.to_owned(), value.into());
        s.status = Status::Modified;
    }

    /// Removes `key` and returns its value.
    pub fn remove(&self, key: &str) -> Option<serde_json::Value> {
        let mut s = self.lock();
        let old = s.record.values.remove(key);
        if old.is_some() {
            s.status = Status::Modified;
        }
        old
    }

    /// Removes every value. The token stays.
    pub fn clear(&self) {
        let mut s = self.lock();
        if !s.record.values.is_empty() {
            s.record.values.clear();
            s.status = Status::Modified;
        }
    }

    /// Gives the session a new token and a new lifetime (Go scs `RenewToken`). The old
    /// token stops working, which prevents session fixation.
    pub fn renew(&self) {
        let mut s = self.lock();
        if let Some(old) = s.token.take() {
            s.stale.push(old);
        }
        s.token = Some(new_token());
        s.record.deadline = deadline_after(s.lifetime);
        s.status = Status::Modified;
    }

    /// Ends the session: the commit deletes its row and expires the cookie (Go scs
    /// `Destroy`).
    pub fn destroy(&self) {
        let mut s = self.lock();
        if let Some(old) = s.token.take() {
            s.stale.push(old);
        }
        s.record.values.clear();
        s.status = Status::Destroyed;
    }

    /// The user ID of the session.
    pub fn user_id(&self) -> Option<i64> {
        self.lock().record.user_id()
    }

    /// The cookie token, or `None` for a session that the store does not hold yet.
    pub fn token(&self) -> Option<String> {
        self.lock().token.clone()
    }

    /// The end of the absolute lifetime.
    pub fn deadline(&self) -> OffsetDateTime {
        self.lock().record.deadline
    }

    /// Returns what the commit must do. A new session that changed gets its token here.
    pub fn change(&self) -> SessionChange {
        let mut s = self.lock();
        match s.status {
            Status::Unmodified => SessionChange::None,
            Status::Destroyed => SessionChange::Destroy {
                stale: std::mem::take(&mut s.stale),
            },
            Status::Modified => {
                let token = s.token.get_or_insert_with(new_token).clone();
                SessionChange::Save {
                    token,
                    record: s.record.clone(),
                    stale: std::mem::take(&mut s.stale),
                }
            }
        }
    }
}

impl<S: Send + Sync> FromRequestParts<S> for Session {
    type Rejection = Response;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Self, Self::Rejection> {
        parts
            .extensions
            .get::<Session>()
            .cloned()
            .ok_or_else(|| crate::auth::required::not_installed(parts))
    }
}

impl<S: Send + Sync> OptionalFromRequestParts<S> for Session {
    type Rejection = std::convert::Infallible;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Option<Self>, Self::Rejection> {
        Ok(parts.extensions.get::<Session>().cloned())
    }
}
