//! Registration contracts for two independent linker experiments.

#[cfg(all(feature = "linkme", feature = "inventory"))]
compile_error!("Select one registration backend");

pub struct Entry {
    pub id: &'static str,
}

#[cfg(feature = "linkme")]
#[linkme::distributed_slice]
pub static ENTRIES: [Entry];

#[cfg(feature = "inventory")]
inventory::collect!(Entry);

pub fn ids() -> Vec<&'static str> {
    #[cfg(feature = "linkme")]
    let mut ids: Vec<_> = ENTRIES.iter().map(|entry| entry.id).collect();
    #[cfg(feature = "inventory")]
    let mut ids: Vec<_> = inventory::iter::<Entry>
        .into_iter()
        .map(|entry| entry.id)
        .collect();
    #[cfg(not(any(feature = "linkme", feature = "inventory")))]
    let mut ids: Vec<&str> = Vec::new();
    ids.sort_unstable();
    ids
}
