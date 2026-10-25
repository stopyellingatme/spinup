//! The IDs of every registered item, for the registry test of the `spinup` binary
//! (`crates/spinup/tests/it/registry.rs`, ADR 0020 §5).
//!
//! Main loop only. When a merge adds a slice, the main loop adds it here. When a merge adds
//! a registered item, the main loop adds its ID to `crates/spinup/tests/expected_registry.txt`.

/// Returns one line for each registered item, as `<kind> <id>`, sorted.
pub fn ids() -> Vec<String> {
    let mut ids: Vec<String> = Vec::new();
    ids.extend(
        spinup_gen::contract::GENERATORS
            .iter()
            .map(|generator| format!("generator {}", generator.name)),
    );
    ids.extend(
        spinup_doctor::registry::CHECKS
            .iter()
            .map(|check| format!("doctor-check {}", check.id)),
    );
    ids.extend(
        spinup_gen::run::POST_STEPS
            .iter()
            .map(|runner| format!("post-step {}", spinup_gen::run::step_name(runner.step))),
    );
    ids.extend(
        spinup_migrate::contract::MIGRATIONS
            .iter()
            .map(|migration| format!("migration {}", migration.id)),
    );
    ids.extend(
        spinup_upgrade::contract::EXTENSIONS
            .iter()
            .map(|extension| format!("upgrade-extension {}", extension.name)),
    );
    ids.sort();
    ids
}
