//! Stable paths of generated Rust resources.

use spinup_gen::fields::Name;

/// The routes of all generated resources.
pub const ROUTES_GEN: &str = "api/src/routes_gen.rs";
/// The handler module list.
pub const HANDLERS_MOD: &str = "api/src/handlers/mod.rs";
/// The model module list.
pub const MODELS_MOD: &str = "api/src/models/mod.rs";

/// Returns the handler path of a resource.
pub fn handlers_path(name: &Name) -> String {
    format!("api/src/handlers/{}.rs", name.plural_snake())
}

/// Returns the handler test path of a resource.
pub fn handler_tests_path(name: &Name) -> String {
    format!("api/src/handlers/{}_tests.rs", name.plural_snake())
}

/// Returns the model path of a resource.
pub fn model_path(name: &Name) -> String {
    format!("api/src/models/{}.rs", name.snake())
}
