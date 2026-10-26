//! Periodic jobs (Go `Worker.Periodic`). Only the leader inserts them, so more workers do
//! not insert more jobs.

use std::time::{Duration, Instant};

use serde_json::Value;

use super::client::{InsertOpts, insert_raw};
use super::db_error;
use super::worker::Inner;

/// One periodic job: insert `args` of `kind` every `every`.
#[derive(Debug, Clone)]
pub(super) struct PeriodicJob {
    pub(super) every: Duration,
    pub(super) kind: &'static str,
    pub(super) args: Value,
}

/// The next insert time of each periodic job, while this worker is the leader.
#[derive(Debug, Default)]
pub(super) struct Schedule {
    due: Vec<Instant>,
}

impl Schedule {
    /// Starts the schedule when this worker becomes the leader: each job is due one period
    /// after `now`.
    pub(super) fn start(&mut self, jobs: &[PeriodicJob], now: Instant) {
        self.due = jobs.iter().map(|p| now + p.every).collect();
    }

    /// Returns the indexes of the jobs that are due at `now`, and moves their next time
    /// past `now`. A job that missed periods is due once.
    pub(super) fn take_due(&mut self, jobs: &[PeriodicJob], now: Instant) -> Vec<usize> {
        let mut out = Vec::new();
        for (i, (p, next)) in jobs.iter().zip(self.due.iter_mut()).enumerate() {
            if *next > now {
                continue;
            }
            while *next <= now {
                *next += p.every;
            }
            out.push(i);
        }
        out
    }
}

/// Inserts the due periodic jobs as unique jobs on the queue of the worker. A unique job
/// that still waits blocks the next insert, so a slow job does not pile up.
pub(super) async fn insert_due(inner: &Inner, schedule: &mut Schedule) {
    for i in schedule.take_due(&inner.periodic, Instant::now()) {
        let p = &inner.periodic[i];
        let opts = InsertOpts {
            queue: Some(inner.config.queue.clone()),
            unique_by_args: true,
            ..InsertOpts::default()
        };
        let inserted = match inner.pool.acquire().await {
            Ok(mut conn) => insert_raw(&mut conn, p.kind, &p.args, &opts).await,
            Err(e) => Err(db_error("insert a periodic job", &e)),
        };
        if let Err(e) = inserted {
            tracing::warn!(error = %e, kind = p.kind, "jobs: cannot insert a periodic job");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn job(ms: u64) -> PeriodicJob {
        PeriodicJob {
            every: Duration::from_millis(ms),
            kind: "tick",
            args: Value::Null,
        }
    }

    #[test]
    fn jobs_periodic_schedule() {
        let jobs = [job(100), job(250)];
        let t0 = Instant::now();
        let mut s = Schedule::default();
        // Not started: nothing is due.
        assert!(s.take_due(&jobs, t0).is_empty());
        s.start(&jobs, t0);
        assert!(s.take_due(&jobs, t0).is_empty());
        assert_eq!(s.take_due(&jobs, t0 + Duration::from_millis(100)), [0]);
        assert!(
            s.take_due(&jobs, t0 + Duration::from_millis(150))
                .is_empty()
        );
        // A missed period gives one insert, not three.
        assert_eq!(s.take_due(&jobs, t0 + Duration::from_millis(500)), [0, 1]);
        assert!(
            s.take_due(&jobs, t0 + Duration::from_millis(550))
                .is_empty()
        );
        assert_eq!(s.take_due(&jobs, t0 + Duration::from_millis(600)), [0]);
    }
}
