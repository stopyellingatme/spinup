//! argon2id password hashes in PHC format and the bounded hash queue (Go
//! `kit/auth/password.go`).
//!
//! The Rust `argon2` crate computes the lanes of one hash on one thread, so one hash
//! slot uses one CPU. [`default_hash_slots`] gives 40% of the CPUs to the hashes, the
//! share that the Go load test found (see the `pg_auth_load_hash_slots` test).

use std::sync::Arc;
use std::time::Duration;

use argon2::{Algorithm, Argon2, Version};
use base64::Engine;
use base64::engine::general_purpose::STANDARD_NO_PAD as B64;
use subtle::ConstantTimeEq;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

#[cfg(test)]
mod tests;

/// The shortest password that registration accepts, in code points.
pub const MIN_PASSWORD_LENGTH: usize = 12;
/// The longest password that registration and login accept, in code points.
pub const MAX_PASSWORD_LENGTH: usize = 512;

/// The argon2id cost parameters of one hash.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Params {
    /// Memory in KiB.
    pub memory: u32,
    /// Passes.
    pub time: u32,
    /// Lanes.
    pub threads: u32,
    /// Salt length in bytes.
    pub salt_len: usize,
    /// Key length in bytes.
    pub key_len: usize,
}

/// The second recommended option of RFC 9106: 64 MiB, 3 passes, 4 lanes, a 16-byte salt,
/// and a 32-byte key. The Go kit uses the same values.
pub const DEFAULT_PARAMS: Params = Params {
    memory: 64 * 1024,
    time: 3,
    threads: 4,
    salt_len: 16,
    key_len: 32,
};

/// Limits for parsed hashes, so a damaged or hostile row cannot exhaust memory.
const MAX_MEMORY: u64 = 1 << 21;
const MAX_TIME: u64 = 64;
const MIN_SALT_LEN: usize = 8;
const MAX_SALT_LEN: usize = 64;
const MIN_KEY_LEN: usize = 16;
const MAX_KEY_LEN: usize = 64;

/// The hash is not a supported argon2id PHC string.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct InvalidHash;

impl std::fmt::Display for InvalidHash {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("auth: the password hash is not a supported argon2id PHC string")
    }
}

impl std::error::Error for InvalidHash {}

/// Computes the argon2id key. `p` comes from [`parse_hash`] or from the options, which
/// [`check_params`] checked.
pub(crate) fn derive(p: &Params, password: &str, salt: &[u8]) -> Vec<u8> {
    let mut key = vec![0u8; p.key_len];
    let params = argon2::Params::new(p.memory, p.time, p.threads, Some(p.key_len));
    if let Ok(params) = params
        && Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
            .hash_password_into(password.as_bytes(), salt, &mut key)
            .is_ok()
    {
        return key;
    }
    // Parameters that argon2 refuses give a key of zero bytes, which matches no hash.
    Vec::new()
}

/// Reports whether argon2 accepts `p`. `Auth::new` refuses other parameters.
pub fn check_params(p: &Params) -> bool {
    argon2::Params::new(p.memory, p.time, p.threads, Some(p.key_len)).is_ok()
        && (MIN_SALT_LEN..=MAX_SALT_LEN).contains(&p.salt_len)
        && (MIN_KEY_LEN..=MAX_KEY_LEN).contains(&p.key_len)
}

/// Returns the PHC string of `password` with a random salt:
/// `$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>` (unpadded standard base64).
pub fn hash_password(p: &Params, password: &str) -> String {
    let mut salt = vec![0u8; p.salt_len];
    if aws_lc_rs::rand::fill(&mut salt).is_err() {
        // The system random source does not fail on a supported OS. Go crashes here.
        panic!("auth: the system random source failed");
    }
    hash_with_salt(p, password, &salt)
}

/// Returns the PHC string of `password` with `salt`.
pub(crate) fn hash_with_salt(p: &Params, password: &str, salt: &[u8]) -> String {
    let key = derive(p, password, salt);
    format!(
        "$argon2id$v=19$m={},t={},p={}${}${}",
        p.memory,
        p.time,
        p.threads,
        B64.encode(salt),
        B64.encode(&key)
    )
}

/// Reports whether `password` matches `encoded`. It compares the keys in constant time.
/// It also returns the parameters of `encoded`, so the caller can rehash with newer
/// parameters.
///
/// # Errors
///
/// [`InvalidHash`] when `encoded` is not a supported argon2id PHC string.
pub fn verify_password(encoded: &str, password: &str) -> Result<(bool, Params), InvalidHash> {
    let (p, salt, key) = parse_hash(encoded)?;
    let other = derive(&p, password, &salt);
    Ok((bool::from(key.ct_eq(&other)), p))
}

/// Parses `<prefix><decimal>` with a value from 0 to `limit`.
fn phc_uint(field: &str, prefix: &str, limit: u64) -> Option<u64> {
    let digits = field.strip_prefix(prefix)?;
    if digits.is_empty()
        || (digits.len() > 1 && digits.starts_with('0'))
        || !digits.bytes().all(|c| c.is_ascii_digit())
    {
        return None;
    }
    let v: u32 = digits.parse().ok()?;
    (u64::from(v) <= limit).then_some(u64::from(v))
}

/// Parses an argon2id PHC string with version 19 and checks every bound. It returns the
/// parameters, the salt, and the key.
///
/// # Errors
///
/// [`InvalidHash`] when a part is missing or out of bounds.
pub fn parse_hash(encoded: &str) -> Result<(Params, Vec<u8>, Vec<u8>), InvalidHash> {
    let parts: Vec<&str> = encoded.split('$').collect();
    if parts.len() != 6 || !parts[0].is_empty() || parts[1] != "argon2id" || parts[2] != "v=19" {
        return Err(InvalidHash);
    }
    let fields: Vec<&str> = parts[3].split(',').collect();
    if fields.len() != 3 {
        return Err(InvalidHash);
    }
    let (Some(m), Some(t), Some(l)) = (
        phc_uint(fields[0], "m=", MAX_MEMORY),
        phc_uint(fields[1], "t=", MAX_TIME),
        phc_uint(fields[2], "p=", 255),
    ) else {
        return Err(InvalidHash);
    };
    if t < 1 || l < 1 || m < 8 * l {
        return Err(InvalidHash);
    }
    let salt = B64.decode(parts[4]).map_err(|_| InvalidHash)?;
    if !(MIN_SALT_LEN..=MAX_SALT_LEN).contains(&salt.len()) {
        return Err(InvalidHash);
    }
    let key = B64.decode(parts[5]).map_err(|_| InvalidHash)?;
    if !(MIN_KEY_LEN..=MAX_KEY_LEN).contains(&key.len()) {
        return Err(InvalidHash);
    }
    // The bounds above keep each value inside u32.
    let p = Params {
        memory: u32::try_from(m).map_err(|_| InvalidHash)?,
        time: u32::try_from(t).map_err(|_| InvalidHash)?,
        threads: u32::try_from(l).map_err(|_| InvalidHash)?,
        salt_len: salt.len(),
        key_len: key.len(),
    };
    Ok((p, salt, key))
}

/// The number of code points in `password`.
pub fn password_length(password: &str) -> usize {
    password.chars().count()
}

/// The default number of hash slots: 40% of the CPUs, at least 1.
///
/// The Go kit uses `GOMAXPROCS/10` slots, and each Go hash keeps 4 goroutines busy, so
/// the hashes take about 40% of the CPUs. The Rust crate computes the 4 lanes on one
/// thread, so one Rust slot uses one CPU. The `#[ignore]` test
/// `auth::pg_auth_load_hash_slots` measures other requests while logins fill the slots.
/// On 10 CPUs (2026-10-25, release build, a shared machine), the `/api/session` p99 was
/// 8 ms without logins, and 10, 21, 38, and 91 ms with 4, 6, 8, and 10 slots. The login
/// rate was 43, 60, 72, and 73 per second. 4 slots (40%) keep the other requests fast.
pub fn default_hash_slots() -> usize {
    let procs = std::thread::available_parallelism().map_or(1, std::num::NonZero::get);
    (procs * 4 / 10).max(1)
}

/// An error of the hash queue.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HashError {
    /// Every slot and queue place is taken, or the wait ended.
    Busy,
    /// The blocking task failed (a panic in the hash).
    Failed,
}

impl std::fmt::Display for HashError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::Busy => "auth: all password hash slots are busy",
            Self::Failed => "auth: the password hash task failed",
        })
    }
}

impl std::error::Error for HashError {}

/// A place in the hash queue. Clones share the place. The place is free when the last
/// clone and the last running hash that uses it end.
#[derive(Debug, Clone)]
pub struct Admitted {
    _place: Arc<OwnedSemaphorePermit>,
}

/// One hash slot that a caller holds without a hash, from [`HashSlots::hold`]. Drop it
/// to free the slot.
#[derive(Debug)]
pub struct HeldSlot {
    _slot: OwnedSemaphorePermit,
}

/// The argon2id computations of the process (Go `hashSlots`). At most `concurrent` run
/// at the same time, on the blocking thread pool. At most `concurrent + waiting` requests
/// run or wait. A request that finds the queue full, or waits longer than `wait`, gets
/// [`HashError::Busy`], and the handlers answer 503 with Retry-After.
///
/// A hash that has started finishes on its blocking thread, also when the request is
/// cancelled. The hash holds its slot and its queue place until it ends, so cancelled
/// requests cannot start more hashes than the limit.
#[derive(Debug, Clone)]
pub struct HashSlots {
    active: Arc<Semaphore>,
    admitted: Arc<Semaphore>,
    wait: Duration,
}

impl HashSlots {
    /// Returns slots for `concurrent` hashes, with a queue of `waiting` requests that wait
    /// at most `wait`.
    pub fn new(concurrent: usize, waiting: usize, wait: Duration) -> Self {
        Self {
            active: Arc::new(Semaphore::new(concurrent)),
            admitted: Arc::new(Semaphore::new(concurrent.saturating_add(waiting))),
            wait,
        }
    }

    /// Takes a place in the queue without waiting. A login admits itself before its
    /// database work, so a busy answer never takes a pool connection.
    ///
    /// # Errors
    ///
    /// [`HashError::Busy`] when the queue is full.
    pub fn admit(&self) -> Result<Admitted, HashError> {
        self.admitted
            .clone()
            .try_acquire_owned()
            .map(|p| Admitted {
                _place: Arc::new(p),
            })
            .map_err(|_| HashError::Busy)
    }

    /// Runs `f` in a free slot. The caller holds a place from [`HashSlots::admit`].
    ///
    /// # Errors
    ///
    /// [`HashError::Busy`] when the wait ends, and [`HashError::Failed`] when `f` panics.
    pub async fn run_admitted<T, F>(&self, place: &Admitted, f: F) -> Result<T, HashError>
    where
        T: Send + 'static,
        F: FnOnce() -> T + Send + 'static,
    {
        let slot = tokio::time::timeout(self.wait, self.active.clone().acquire_owned())
            .await
            .map_err(|_| HashError::Busy)?
            .map_err(|_| HashError::Busy)?;
        let place = place.clone();
        tokio::task::spawn_blocking(move || {
            let out = f();
            drop(slot);
            drop(place);
            out
        })
        .await
        .map_err(|_| HashError::Failed)
    }

    /// Admits itself, then runs `f` in a free slot.
    ///
    /// # Errors
    ///
    /// See [`HashSlots::admit`] and [`HashSlots::run_admitted`].
    pub async fn run<T, F>(&self, f: F) -> Result<T, HashError>
    where
        T: Send + 'static,
        F: FnOnce() -> T + Send + 'static,
    {
        let place = self.admit()?;
        self.run_admitted(&place, f).await
    }

    /// Takes one free hash slot without a queue place, or `None` when every slot runs.
    /// Tests and load checks use it to keep the slots busy.
    pub fn hold(&self) -> Option<HeldSlot> {
        self.active
            .clone()
            .try_acquire_owned()
            .ok()
            .map(|p| HeldSlot { _slot: p })
    }

    /// The number of free queue places.
    pub fn free_places(&self) -> usize {
        self.admitted.available_permits()
    }

    /// The number of free hash slots.
    pub fn free_slots(&self) -> usize {
        self.active.available_permits()
    }
}
