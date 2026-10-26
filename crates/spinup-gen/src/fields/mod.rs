//! Field types and naming rules of the generators (Go `internal/gen/fields`).
//!
//! Seam placeholder from the R2 pre-seed (ADR 0021 §1). Unit R2.11 owns this module. It
//! replaces the placeholders and keeps the paths of [`Name`] and [`Field`] and the derives
//! `Debug`, `Clone`, `PartialEq`, and `Eq`.

/// A resource or field name and its case forms. Placeholder.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Name {
    _placeholder: (),
}

/// One writable field of a resource. Placeholder.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Field {
    _placeholder: (),
}
