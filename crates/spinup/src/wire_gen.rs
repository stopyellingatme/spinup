//! The wire list: one `use` line for each crate that defines or fills a linkme slice
//! (ADR 0020 §5). Without a line, the linker can drop a crate that nothing else
//! references, and its registrations disappear (R0.3).
//!
//! Main loop only. The registry test fails when a line is missing.

use spinup_doctor as _;
use spinup_gen as _;
use spinup_gen_app as _;
use spinup_gen_web as _;
use spinup_migrate as _;
use spinup_upgrade as _;
