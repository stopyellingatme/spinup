//! Extractors that turn every request error into a problem answer with the status and the
//! text of the Go API (Go `httpx.DecodeJSON`, `httpx.RequireJSONFields`, and the
//! oapi-codegen parameter errors).

use axum::body::Bytes;
use axum::extract::path::ErrorKind;
use axum::extract::rejection::PathRejection;
use axum::extract::{FromRequest, FromRequestParts, RawPathParams, Request};
use axum::http::StatusCode;
use axum::http::request::Parts;
use serde::de::DeserializeOwned;
use utoipa::openapi::schema::AdditionalProperties;
use utoipa::openapi::{RefOr, Schema};
use utoipa::{PartialSchema, ToSchema};

use super::problem::{ApiError, FieldError, Problem};

/// The detail of an empty body.
pub const ONE_VALUE: &str = "The request body must contain one JSON value.";
/// The detail of a body with more than one JSON value.
pub const ONLY_ONE_VALUE: &str = "The request body must contain only one JSON value.";
/// The detail of bad JSON or an unknown property.
pub const BAD_JSON: &str =
    "The request body must contain valid JSON with only known fields and correct value types.";
/// The detail of a body above the size limit.
pub const TOO_LARGE: &str = "The request body exceeds the size limit.";
/// The detail of a body that is not a JSON object.
pub const NOT_OBJECT: &str = "The request body must be a JSON object.";

fn bad_param(name: &str) -> ApiError {
    Problem::new(
        StatusCode::BAD_REQUEST,
        "A parameter has an invalid format.",
    )
    .with_errors(vec![FieldError::new(name, "has an invalid format")])
    .into()
}

/// The required properties of a request body type, from its OpenAPI schema. The schema is
/// the only list, so the check and the spec cannot drift.
pub fn required_fields<T: PartialSchema>() -> Vec<String> {
    match T::schema() {
        RefOr::T(Schema::Object(obj)) => obj.required,
        _ => Vec::new(),
    }
}

/// The known properties of a request body type, or `None` when the schema allows every
/// property (no object schema, no properties, or `additionalProperties`).
fn known_fields<T: PartialSchema>() -> Option<Vec<String>> {
    match T::schema() {
        RefOr::T(Schema::Object(obj)) => {
            let open = match obj.additional_properties.as_deref() {
                None | Some(AdditionalProperties::FreeForm(false)) => false,
                Some(_) => true,
            };
            if open || obj.properties.is_empty() {
                None
            } else {
                Some(obj.properties.keys().cloned().collect())
            }
        }
        _ => None,
    }
}

/// A JSON request body with the rules of the Go API:
///
/// - an empty body, bad JSON, or more than one JSON value is a 400;
/// - a body above the size limit ([`super::max_body`]) is a 413;
/// - a value that is not an object (or `null`) is a 422;
/// - a missing or `null` required property is a 422 with field errors sorted by name;
/// - a property that the schema of `T` does not name is a 400 (Go `DisallowUnknownFields`);
/// - a wrong type is a 422 with the field path, and a bad enum value names the variants.
#[derive(Debug, Clone, Copy, Default)]
pub struct ValidJson<T>(pub T);

impl<T, S> FromRequest<S> for ValidJson<T>
where
    T: DeserializeOwned + ToSchema,
    S: Send + Sync,
{
    type Rejection = ApiError;

    async fn from_request(req: Request, state: &S) -> Result<Self, Self::Rejection> {
        let bytes = Bytes::from_request(req, state).await.map_err(|rej| {
            if rej.status() == StatusCode::PAYLOAD_TOO_LARGE {
                ApiError::new(StatusCode::PAYLOAD_TOO_LARGE, TOO_LARGE)
            } else {
                ApiError::bad_request(ONE_VALUE)
            }
        })?;
        decode::<T>(&bytes).map(ValidJson)
    }
}

/// Decodes a JSON body with the rules of [`ValidJson`].
pub fn decode<T: DeserializeOwned + PartialSchema>(bytes: &[u8]) -> Result<T, ApiError> {
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Err(ApiError::bad_request(ONE_VALUE));
    }
    let mut stream = serde_json::Deserializer::from_slice(bytes).into_iter::<serde_json::Value>();
    let value = match stream.next() {
        Some(Ok(v)) => v,
        None => return Err(ApiError::bad_request(ONE_VALUE)),
        Some(Err(_)) => return Err(ApiError::bad_request(BAD_JSON)),
    };
    match stream.next() {
        None => {}
        Some(Ok(_)) => return Err(ApiError::bad_request(ONLY_ONE_VALUE)),
        Some(Err(_)) => return Err(ApiError::bad_request(BAD_JSON)),
    }
    let empty = serde_json::Map::new();
    let object = match &value {
        serde_json::Value::Object(map) => map,
        serde_json::Value::Null => &empty,
        _ => {
            return Err(ApiError::new(StatusCode::UNPROCESSABLE_ENTITY, NOT_OBJECT));
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
    if let Some(known) = known_fields::<T>()
        && object.keys().any(|k| !known.contains(k))
    {
        return Err(ApiError::bad_request(BAD_JSON));
    }
    let value = if value.is_null() {
        serde_json::Value::Object(empty)
    } else {
        value
    };
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

/// Turns the serde text "`x`, expected one of `a`, `b`, `c`" (or "expected `a` or `b`")
/// into "must be one of: a, b, c", the text of the Go enum check.
fn one_of(serde_text: &str) -> String {
    let list = serde_text
        .split_once("expected")
        .map(|(_, list)| list)
        .unwrap_or("");
    let variants: Vec<&str> = list.split('`').skip(1).step_by(2).collect();
    format!("must be one of: {}", variants.join(", "))
}

/// Path parameters. A value that does not parse is a 400 that names the parameter.
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

/// Query parameters. A value that does not parse is a 400 that names the parameter. Range
/// checks belong to the handler, which answers 422.
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
        let de = serde_urlencoded::Deserializer::new(form_urlencoded::parse(query.as_bytes()));
        serde_path_to_error::deserialize::<_, T>(de)
            .map(Query)
            .map_err(|err| bad_param(&err.path().to_string()))
    }
}

/// The page parameters of a list operation, with the Go defaults and limits.
pub mod page {
    use super::{ApiError, FieldError};

    /// The default page size.
    pub const DEFAULT_LIMIT: i32 = 50;
    /// The largest page size.
    pub const MAX_LIMIT: i32 = 200;

    /// Checks `limit` and `offset` and applies their defaults. It returns a 422 problem with
    /// one field error for each bad value.
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
