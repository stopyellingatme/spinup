//! The application jobs. spinup owns this file.

/// Registers each application job in name order.
pub fn register(worker: &mut kit::jobs::Worker, pool: &kit::db::PgPool) -> anyhow::Result<()> {
    let _ = (worker, pool);
    Ok(())
}
