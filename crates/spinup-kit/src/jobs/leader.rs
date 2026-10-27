//! The leader: one worker, elected through `kit_leader` with a TTL. The leader runs the
//! rescuer, the cleaner, and the periodic jobs.

use std::sync::Arc;
use std::time::Instant;

use super::db_error;
use super::periodic::{self, Schedule};
use super::rescuer;
use super::worker::Inner;

/// The leader loop: elect, rescue, clean, and insert periodic jobs, once each
/// `maintenance_interval`. It runs until the worker aborts it.
pub(super) async fn maintain(inner: Arc<Inner>) {
    let cfg = &inner.config;
    let mut leader = false;
    let mut schedule = Schedule::default();
    let mut next_clean = Instant::now();
    let mut tick = tokio::time::interval(cfg.maintenance_interval);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        tick.tick().await;
        let elected = match elect(&inner).await {
            Ok(elected) => elected,
            Err(e) => {
                tracing::warn!(error = %e, "jobs: leader election failed");
                false
            }
        };
        if elected && !leader {
            tracing::info!(worker = %cfg.id, "jobs: this worker is the leader");
            schedule.start(&inner.periodic, Instant::now());
        }
        leader = elected;
        if !leader {
            continue;
        }
        match rescuer::rescue(&inner).await {
            Ok(0) => {}
            Ok(n) => tracing::warn!(jobs = n, "jobs: rescued stuck jobs"),
            Err(e) => tracing::warn!(error = %e, "jobs: the rescuer failed"),
        }
        if Instant::now() >= next_clean {
            next_clean = Instant::now() + cfg.clean_interval;
            match rescuer::clean(&inner).await {
                Ok(0) => {}
                Ok(n) => tracing::debug!(jobs = n, "jobs: deleted old jobs"),
                Err(e) => tracing::warn!(error = %e, "jobs: the cleaner failed"),
            }
        }
        periodic::insert_due(&inner, &mut schedule).await;
    }
}

/// Takes or keeps the lead. Returns true when this worker is the leader.
async fn elect(inner: &Inner) -> anyhow::Result<bool> {
    let row: Option<(String,)> = sqlx::query_as(
        "INSERT INTO kit_leader (leader_id, elected_at, expires_at)
        VALUES ($1, now(), now() + make_interval(secs => $2))
        ON CONFLICT (name) DO UPDATE SET
            leader_id = EXCLUDED.leader_id,
            elected_at = CASE WHEN kit_leader.leader_id = EXCLUDED.leader_id
                THEN kit_leader.elected_at ELSE now() END,
            expires_at = EXCLUDED.expires_at
        WHERE kit_leader.leader_id = EXCLUDED.leader_id OR kit_leader.expires_at < now()
        RETURNING leader_id",
    )
    .bind(&inner.config.id)
    .bind(inner.config.leader_ttl.as_secs_f64())
    .fetch_optional(&inner.pool)
    .await
    .map_err(|e| db_error("elect the leader", &e))?;
    Ok(row.is_some())
}

/// Gives up the lead, so another worker takes it at once. The worker calls it when it
/// stops.
pub(super) async fn resign(inner: &Inner) {
    let resign = sqlx::query("DELETE FROM kit_leader WHERE leader_id = $1")
        .bind(&inner.config.id)
        .execute(&inner.pool)
        .await;
    if let Err(e) = resign {
        tracing::warn!(error = %e, "jobs: cannot give up the lead");
    }
}
