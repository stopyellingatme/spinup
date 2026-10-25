//! `sqlx::migrate!` embeds `migrations/` at compile time. A new migration
//! file must rebuild the crate, so the build script watches the directory.
fn main() {
    println!("cargo:rerun-if-changed=migrations");
}
