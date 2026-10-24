//! Extractors that turn every request error into a problem response with
//! the same status and text as the Go generated API.

use axum::body::Bytes;
use axum::extract::path::ErrorKind;
use axum::extract::rejection::PathRejection;
use axum::extract::{FromRequest, FromRequestParts, RawPathParams, Request};
use axum::http::StatusCode;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use utoipa::openapi::{RefOr, Schema};
use utoipa::{PartialSchema, ToSchema};

use crate::problem::{ApiError, FieldError, Problem};

fn bad_request(detail: &str) -> ApiError {
    ApiError::new(StatusCode::BAD_REQUEST, detail)
}

fn bad_param(name: &str) -> ApiError {
    Problem::new(
        StatusCode::BAD_REQUEST,
        "A parameter has an invalid format.",
    )
    .with_errors(vec![FieldError::new(name, "has an invalid format")])
    .into()
}

/// The required properties of a request body type, from its OpenAPI schema.
/// The schema is the only list, so the check and the spec cannot drift.
pub fn required_fields<T: PartialSchema>() -> Vec<String> {
    match T::schema() {
        RefOr::T(Schema::Object(obj)) => obj.required,
        _ => Vec::new(),
    }
}

/// A JSON request body. It works as the Go `RequireFields` middleware plus
/// the strict decoder of oapi-codegen:
///
/// - an empty body or bad JSON is a 400;
/// - a value that is not an object is a 422;
/// - a missing or null required property is a 422 with sorted field errors;
/// - a wrong type is a 422 with the field path.
#[derive(Debug, Clone, Copy, Default)]
pub struct Json<T>(pub T);

impl<T, S> FromRequest<S> for Json<T>
where
    T: DeserializeOwned + ToSchema,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request(req: Request, state: &S) -> Result<Self, Self::Rejection> {
        let bytes = Bytes::from_request(req, state).await.map_err(|rej| {
            if rej.status() == StatusCode::PAYLOAD_TOO_LARGE {
                ApiError::new(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "The request body exceeds the size limit.",
                )
            } else {
                bad_request("The request body must contain one JSON value.")
            }
        })?;
        decode::<T>(&bytes).map(Json)
    }
}

/// Decodes a JSON body with the rules of [`Json`].
pub fn decode<T: DeserializeOwned + ToSchema>(bytes: &[u8]) -> Result<T, ApiError> {
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Err(bad_request("The request body must contain one JSON value."));
    }
    let value: serde_json::Value = serde_json::from_slice(bytes).map_err(|_| {
        bad_request(
            "The request body must contain valid JSON with only known fields and correct value types.",
        )
    })?;
    let empty = serde_json::Map::new();
    let object = match &value {
        serde_json::Value::Object(map) => map,
        serde_json::Value::Null => &empty,
        _ => {
            return Err(ApiError::new(
                StatusCode::UNPROCESSABLE_ENTITY,
                "The request body must be a JSON object.",
            ));
        }
    };
    let mut missing: Vec<FieldError> = required_fields::<T>()
        .into_iter()
        .filter(|k| object.get(k).is_none_or(serde_json::Value::is_null))
        .map(|k| FieldError::new(k, "is required"))
        .collect();
    if !missing.is_empty() {
        missing.sort_by(|a, b| a.field.cmp(&b.field));
        return Err(ApiError::invalid(missing));
    }
    serde_path_to_error::deserialize::<_, T>(value).map_err(|err| {
        let field = err.path().to_string();
        let text = err.inner().to_string();
        if text.starts_with("invalid type") {
            ApiError::invalid(vec![FieldError::new(field, "has the wrong type")])
        } else if let Some(rest) = text.strip_prefix("unknown variant") {
            ApiError::invalid(vec![FieldError::new(field, one_of(rest))])
        } else {
            ApiError::from(
                Problem::new(
                    StatusCode::UNPROCESSABLE_ENTITY,
                    "The request has a value with an invalid format.",
                )
                .with_errors(vec![FieldError::new(field, "has an invalid format")]),
            )
        }
    })
}

/// Turns the serde text "`x`, expected one of `a`, `b`, `c`" (or "expected
/// `a` or `b`") into "must be one of: a, b, c", the text of the Go enum check.
fn one_of(serde_text: &str) -> String {
    let list = serde_text
        .split_once("expected")
        .map(|(_, list)| list)
        .unwrap_or("");
    let variants: Vec<&str> = list.split('`').skip(1).step_by(2).collect();
    format!("must be one of: {}", variants.join(", "))
}

/// Path parameters. A value that does not parse is a 400 that names the
/// parameter.
#[derive(Debug, Clone, Copy, Default)]
pub struct Path<T>(pub T);

impl<T, S> FromRequestParts<S> for Path<T>
where
    T: DeserializeOwned + Send,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        match axum::extract::Path::<T>::from_request_parts(parts, state).await {
            Ok(axum::extract::Path(v)) => Ok(Path(v)),
            Err(rej) => {
                let key = match &rej {
                    PathRejection::FailedToDeserializePathParams(e) => match e.kind() {
                        ErrorKind::ParseErrorAtKey { key, .. }
                        | ErrorKind::DeserializeError { key, .. }
                        | ErrorKind::InvalidUtf8InPathParam { key } => Some(key.clone()),
                        _ => None,
                    },
                    _ => None,
                };
                let key = match key {
                    Some(k) => k,
                    None => RawPathParams::from_request_parts(parts, state)
                        .await
                        .ok()
                        .and_then(|p| p.iter().next().map(|(k, _)| k.to_string()))
                        .unwrap_or_default(),
                };
                Err(bad_param(&key))
            }
        }
    }
}

/// Query parameters. A value that does not parse is a 400 that names the
/// parameter. Range checks belong to the handler, which answers 422.
#[derive(Debug, Clone, Copy, Default)]
pub struct Query<T>(pub T);

impl<T, S> FromRequestParts<S> for Query<T>
where
    T: DeserializeOwned,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request_parts(parts: &mut Parts, _state: &S) -> Result<Self, Self::Rejection> {
        let query = parts.uri.query().unwrap_or_default();
        let de = serde_urlencoded::Deserializer::new(form_urlencoded_parse(query));
        serde_path_to_error::deserialize::<_, T>(de)
            .map(Query)
            .map_err(|err| bad_param(&err.path().to_string()))
    }
}

fn form_urlencoded_parse(query: &str) -> form_urlencoded::Parse<'_> {
    form_urlencoded::parse(query.as_bytes())
}

/// The page parameters of a list operation, with the Go defaults and limits.
pub mod page {
    use super::*;

    /// The default page size.
    pub const DEFAULT_LIMIT: i32 = 50;
    /// The largest page size.
    pub const MAX_LIMIT: i32 = 200;

    /// Validates `limit` and `offset` and applies their defaults. It returns a
    /// 422 problem with one field error for each bad value.
    pub fn resolve(limit: Option<i32>, offset: Option<i32>) -> Result<(i64, i64), ApiError> {
        let mut errs = Vec::new();
        let l = limit.unwrap_or(DEFAULT_LIMIT);
        if !(1..=MAX_LIMIT).contains(&l) {
            errs.push(FieldError::new("limit", "must be from 1 to 200"));
        }
        let o = offset.unwrap_or(0);
        if o < 0 {
            errs.push(FieldError::new("offset", "must not be negative"));
        }
        if errs.is_empty() {
            Ok((l.into(), o.into()))
        } else {
            Err(ApiError::invalid(errs))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Debug, Deserialize, ToSchema)]
    #[allow(dead_code)]
    struct Input {
        title: String,
        done: bool,
        #[serde(default)]
        note: Option<String>,
        kind: Kind,
    }

    #[derive(Debug, Deserialize, ToSchema)]
    #[serde(rename_all = "lowercase")]
    enum Kind {
        Low,
        High,
    }

    fn status_and_errors(body: &str) -> (u16, Vec<String>) {
        let err = decode::<Input>(body.as_bytes()).unwrap_err().0;
        (
            err.status,
            err.errors
                .iter()
                .map(|e| format!("{}: {}", e.field, e.message))
                .collect(),
        )
    }

    #[test]
    fn required_comes_from_the_schema() {
        assert_eq!(required_fields::<Input>(), ["title", "done", "kind"]);
    }

    #[test]
    fn errors_match_go() {
        assert_eq!(status_and_errors("").0, 400);
        assert_eq!(status_and_errors("{").0, 400);
        assert_eq!(status_and_errors("42").0, 422);
        assert_eq!(
            status_and_errors("null"),
            (
                422,
                vec![
                    "done: is required".into(),
                    "kind: is required".into(),
                    "title: is required".into()
                ]
            )
        );
        assert_eq!(
            status_and_errors(r#"{"title": 42, "done": true, "kind": "low"}"#),
            (422, vec!["title: has the wrong type".into()])
        );
        assert_eq!(
            status_and_errors(r#"{"title": "a", "done": true, "kind": "mid"}"#),
            (422, vec!["kind: must be one of: low, high".into()])
        );
    }

    #[test]
    fn page_limits() {
        assert_eq!(page::resolve(None, None).unwrap(), (50, 0));
        assert_eq!(
            page::resolve(Some(201), Some(-1))
                .unwrap_err()
                .0
                .errors
                .len(),
            2
        );
    }
}
