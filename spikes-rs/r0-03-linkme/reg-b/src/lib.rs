//! This crate exports no callable symbols.

#[cfg(feature = "linkme")]
#[linkme::distributed_slice(registry::ENTRIES)]
static ENTRY: registry::Entry = registry::Entry { id: "reg-b" };

#[cfg(feature = "inventory")]
inventory::submit! { registry::Entry { id: "reg-b" } }
