//! The `spinup` binary. It holds no logic: it calls `spinup_cli::root::execute`.

use std::process::ExitCode;

mod wire_gen;

fn main() -> ExitCode {
    // The registry test (tests/it/registry.rs) reads the registered IDs from the real
    // binary, so the test harness cannot link a crate that the binary drops (R0.3).
    if std::env::var_os("SPINUP_PRINT_REGISTRY").is_some_and(|value| value == "1") {
        for id in spinup_cli::registry::ids() {
            println!("{id}");
        }
        return ExitCode::SUCCESS;
    }
    spinup_cli::root::execute(std::env::args_os().collect())
}
