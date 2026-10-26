//! The resource part contract: [`Spec`], [`PartPlan`], [`Part`], and the
//! [`RESOURCE_PARTS`] slice (Go `internal/gen/resource` Spec, Part, RegisterPart; ADR 0021 §1).
//!
//! Contract file from the R2 pre-seed. Main loop only: units do not edit it.
//! R2.12 registers the `pages` part. R2.13 runs the parts in name order and fails on an
//! empty or duplicate name with a `Try:` line (Go panicked).
//!
//! ```ignore
//! #[linkme::distributed_slice(spinup_gen::part::RESOURCE_PARTS)]
//! static PAGES: Part = Part { name: "pages", plan: plan_pages, paths: Some(page_paths) };
//! ```

use std::path::Path;

use spinup_manifest::lock::Lock;
use spinup_vfs::plan::Plan;

use crate::contract::PostStep;
use crate::fields::{Field, Name};

/// The resource that the generator and its parts plan.
#[derive(Debug, Clone)]
pub struct Spec<'a> {
    /// The project root.
    pub root: &'a Path,
    /// The project lock (packs and answers, such as `web`, `auth`, and `frontend_mode`).
    pub lock: &'a Lock,
    /// The resource name.
    pub name: Name,
    /// The writable fields, in argument order.
    pub fields: Vec<Field>,
    /// Each row belongs to its signed-in user (ADR 0015).
    pub owned: bool,
    /// Auth protects a table that all signed-in users share.
    pub shared: bool,
}

/// The files of one part and the post-steps that they need.
#[derive(Debug, Clone, Default)]
pub struct PartPlan {
    /// The files of the part.
    pub plan: Plan,
    /// The post-steps, such as [`PostStep::Client`].
    pub post_steps: Vec<PostStep>,
}

/// Plans extra files for each resource, such as the web pages.
pub struct Part {
    /// The part name. Parts run in name order. Names are unique and not empty.
    pub name: &'static str,
    /// Plans the files of the part. It must be deterministic for the same spec.
    pub plan: fn(&Spec<'_>) -> anyhow::Result<PartPlan>,
    /// The files that the part owns for a resource, relative to the project root, for
    /// `destroy` and `resource_core::paths`. `None` when the part owns no file.
    pub paths: Option<fn(&Name) -> Vec<String>>,
}

impl std::fmt::Debug for Part {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Part")
            .field("name", &self.name)
            .finish_non_exhaustive()
    }
}

/// Every registered resource part, in link order.
#[linkme::distributed_slice]
pub static RESOURCE_PARTS: [Part];
