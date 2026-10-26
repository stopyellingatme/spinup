//! The application mailers. spinup owns this file.

mod mailers;
#[cfg(feature = "jobs")]
mod worker;

pub use mailers::Mailers;
