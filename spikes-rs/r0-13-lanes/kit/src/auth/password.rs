//! argon2id password hashes in PHC format and the bounded hash queue, as
//! `kit/auth/password.go`.

use std::sync::Arc;
use std::time::Duration;

use argon2::{Algorithm, Argon2, Version};
use base64::Engine;
use base64::engine::general_purpose::STANDARD_NO_PAD as B64;
use subtle::ConstantTimeEq;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

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

/// The second recommended option of RFC 9106: 64 MiB, 3 passes, 4 lanes,
/// a 16-byte salt, and a 32-byte key. The Go kit uses the same values.
pub const DEFAULT_PARAMS: Params = Params {
    memory: 64 * 1024,
    time: 3,
    threads: 4,
    salt_len: 16,
    key_len: 32,
};

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

fn derive(p: &Params, password: &str, salt: &[u8]) -> Vec<u8> {
    let params = argon2::Params::new(p.memory, p.time, p.threads, Some(p.key_len))
        .expect("parameters checked by parse_hash or DEFAULT_PARAMS");
    let mut key = vec![0u8; p.key_len];
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(password.as_bytes(), salt, &mut key)
        .expect("valid salt and key length");
    key
}

/// Returns the PHC string of `password` with a random salt:
/// `$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>`.
pub fn hash_password(p: &Params, password: &str) -> String {
    let mut salt = vec![0u8; p.salt_len];
    aws_lc_rs::rand::fill(&mut salt).expect("the system random source works");
    let key = derive(p, password, &salt);
    format!(
        "$argon2id$v=19$m={},t={},p={}${}${}",
        p.memory,
        p.time,
        p.threads,
        B64.encode(&salt),
        B64.encode(&key)
    )
}

/// Reports whether `password` matches `encoded`, in constant time. It also
/// returns the parameters of `encoded`, so the caller can rehash.
pub fn verify_password(encoded: &str, password: &str) -> Result<(bool, Params), InvalidHash> {
    let (p, salt, key) = parse_hash(encoded)?;
    let other = derive(&p, password, &salt);
    Ok((bool::from(key.ct_eq(&other)), p))
}

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

/// Parses an argon2id PHC string with version 19 and checks every bound.
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
    let p = Params {
        memory: m as u32,
        time: t as u32,
        threads: l as u32,
        salt_len: salt.len(),
        key_len: key.len(),
    };
    Ok((p, salt, key))
}

/// The number of code points in `password`.
pub fn password_length(password: &str) -> usize {
    password.chars().count()
}

/// An error of the hash queue.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HashError {
    /// Every slot and queue place is taken, or the wait ended.
    Busy,
    /// The blocking task failed (a panic in the hash).
    Failed,
}

/// A place in the hash queue. Clones share the place. The place is free when
/// the last clone and the last running hash that uses it end.
#[derive(Debug, Clone)]
pub struct Admitted {
    _place: Arc<OwnedSemaphorePermit>,
}

/// The argon2id computations of the process. At most `concurrent` run at the
/// same time, on the blocking thread pool. At most `concurrent + waiting`
/// requests run or wait. A request that finds the queue full, or waits
/// longer than `wait`, gets [`HashError::Busy`], and the handlers answer 503
/// with Retry-After, as in Go.
///
/// A hash that has started finishes on its blocking thread even when the
/// request is cancelled. The hash holds its slot and its queue place until
/// it ends, so cancelled requests cannot start more hashes than the limit.
#[derive(Debug, Clone)]
pub struct HashSlots {
    active: Arc<Semaphore>,
    admitted: Arc<Semaphore>,
    wait: Duration,
}

impl HashSlots {
    /// Returns the slots.
    pub fn new(concurrent: usize, waiting: usize, wait: Duration) -> Self {
        Self {
            active: Arc::new(Semaphore::new(concurrent)),
            admitted: Arc::new(Semaphore::new(concurrent + waiting)),
            wait,
        }
    }

    /// Takes a place in the queue without waiting.
    pub fn admit(&self) -> Result<Admitted, HashError> {
        self.admitted
            .clone()
            .try_acquire_owned()
            .map(|p| Admitted {
                _place: Arc::new(p),
            })
            .map_err(|_| HashError::Busy)
    }

    /// Runs `f` in a free slot. The caller holds a place from `admit`.
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
    pub async fn run<T, F>(&self, f: F) -> Result<T, HashError>
    where
        T: Send + 'static,
        F: FnOnce() -> T + Send + 'static,
    {
        let place = self.admit()?;
        self.run_admitted(&place, f).await
    }

    /// The number of free queue places, for tests.
    pub fn free_places(&self) -> usize {
        self.admitted.available_permits()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const GOOD: &str = "correct horse battery";
    const TEST_PARAMS: Params = Params {
        memory: 64,
        time: 1,
        threads: 1,
        salt_len: 16,
        key_len: 32,
    };

    fn phc_shape(h: &str) -> bool {
        let parts: Vec<&str> = h.split('$').collect();
        let b64 = |s: &str, n: usize| {
            s.len() == n
                && s.bytes()
                    .all(|c| c.is_ascii_alphanumeric() || c == b'+' || c == b'/')
        };
        parts.len() == 6
            && parts[1] == "argon2id"
            && parts[2] == "v=19"
            && parts[3] == "m=65536,t=3,p=4"
            && b64(parts[4], 22)
            && b64(parts[5], 43)
    }

    #[test]
    fn default_params_and_format() {
        let h = hash_password(&DEFAULT_PARAMS, GOOD);
        assert!(phc_shape(&h), "{h}");
        assert_eq!(verify_password(&h, GOOD).unwrap(), (true, DEFAULT_PARAMS));
    }

    // A hash that Go wrote with golang.org/x/crypto v0.58.0 argon2.IDKey and
    // the kit PHC format (salt "saltsaltsaltsalt", test parameters). It
    // verifies in Rust, and Rust writes the same string.
    #[test]
    fn go_hash_verifies() {
        let go = "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$QOKF1iG70GOhZX71bkTGoINw9zqSjKFirULQQtMxBVI";
        let rust = format!(
            "$argon2id$v=19$m=64,t=1,p=1${}${}",
            B64.encode(b"saltsaltsaltsalt"),
            B64.encode(derive(&TEST_PARAMS, GOOD, b"saltsaltsaltsalt"))
        );
        assert_eq!(rust, go, "the Rust key differs from the Go key");
        assert_eq!(verify_password(go, GOOD).unwrap(), (true, TEST_PARAMS));
    }

    #[test]
    fn round_trip() {
        let first = hash_password(&TEST_PARAMS, GOOD);
        assert_ne!(
            first,
            hash_password(&TEST_PARAMS, GOOD),
            "the salt is not random"
        );
        for (pw, want) in [
            (GOOD, true),
            ("correct horse battery ", false),
            ("Correct horse battery", false),
            ("", false),
        ] {
            assert_eq!(
                verify_password(&first, pw).unwrap(),
                (want, TEST_PARAMS),
                "{pw:?}"
            );
        }
        let u = hash_password(&TEST_PARAMS, "pässwörd 🔑 long enough");
        assert!(verify_password(&u, "pässwörd 🔑 long enough").unwrap().0);
    }

    #[test]
    fn parse_rejects() {
        let valid = hash_password(&TEST_PARAMS, GOOD);
        let parts: Vec<&str> = valid.split('$').collect();
        let with = |i: usize, v: &str| {
            let mut p = parts.clone();
            p[i] = v;
            p.join("$")
        };
        for bad in [
            String::new(),
            "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5".into(),
            with(1, "argon2i"),
            with(2, "v=16"),
            with(3, "m=64,t=1"),
            with(3, "m=064,t=1,p=1"),
            with(3, "m=4,t=1,p=1"),
            with(3, "m=64,t=0,p=1"),
            with(3, "m=99999999,t=1,p=1"),
            with(3, "m=64,t=65,p=1"),
            with(4, "c2FsdA"),
            with(4, "!!!"),
            with(5, "a2V5"),
        ] {
            assert_eq!(parse_hash(&bad).map(|_| ()), Err(InvalidHash), "{bad}");
        }
    }

    #[tokio::test]
    async fn hash_slots_full_queue_and_wait() {
        let slots = HashSlots::new(1, 1, Duration::from_millis(50));
        let a = slots.admit().unwrap();
        let _b = slots.admit().unwrap();
        assert_eq!(slots.admit().unwrap_err(), HashError::Busy, "full queue");
        // Hold the only slot. The second request waits, then gets Busy.
        let (tx, rx) = std::sync::mpsc::channel::<()>();
        let slots2 = slots.clone();
        let a2 = a.clone();
        let running = tokio::spawn(async move {
            slots2
                .run_admitted(&a2, move || {
                    let _ = rx.recv();
                })
                .await
        });
        tokio::time::sleep(Duration::from_millis(10)).await;
        let started = std::time::Instant::now();
        assert_eq!(
            slots.run_admitted(&_b, || ()).await.unwrap_err(),
            HashError::Busy
        );
        assert!(started.elapsed() >= Duration::from_millis(50));
        tx.send(()).unwrap();
        running.await.unwrap().unwrap();
        drop(a);
        drop(_b);
        assert_eq!(slots.free_places(), 2);
    }

    #[tokio::test]
    async fn a_cancelled_request_keeps_its_place_until_the_hash_ends() {
        let slots = HashSlots::new(1, 0, Duration::from_secs(1));
        let (tx, rx) = std::sync::mpsc::channel::<()>();
        let s = slots.clone();
        let req = tokio::spawn(async move {
            s.run(move || {
                let _ = rx.recv();
            })
            .await
        });
        tokio::time::sleep(Duration::from_millis(20)).await;
        req.abort();
        let _ = req.await;
        assert_eq!(slots.free_places(), 0, "the hash still runs");
        tx.send(()).unwrap();
        for _ in 0..100 {
            if slots.free_places() == 1 {
                return;
            }
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
        panic!("the place did not return after the hash ended");
    }
}
