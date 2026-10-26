//! The tests of `password_test.go` (TestPasswordDefaultParams, TestPasswordRoundTrip,
//! TestParseHashRejects, TestHashSlots). TestNormalizeEmail is in `tests/it/auth.rs`.

use super::*;

const GOOD: &str = "correct horse battery";
const TEST_PARAMS: Params = Params {
    memory: 64,
    time: 1,
    threads: 1,
    salt_len: 16,
    key_len: 32,
};

fn b64_len(s: &str, n: usize) -> bool {
    s.len() == n
        && s.bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'+' || c == b'/')
}

#[test]
fn auth_password_default_params() {
    let h = hash_password(&DEFAULT_PARAMS, GOOD);
    let parts: Vec<&str> = h.split('$').collect();
    assert_eq!(parts.len(), 6, "{h}");
    assert_eq!(&parts[1..4], ["argon2id", "v=19", "m=65536,t=3,p=4"], "{h}");
    assert!(b64_len(parts[4], 22) && b64_len(parts[5], 43), "{h}");
    assert_eq!(verify_password(&h, GOOD).unwrap(), (true, DEFAULT_PARAMS));
}

// A hash that Go wrote with golang.org/x/crypto argon2.IDKey and the kit PHC format
// (salt "saltsaltsaltsalt", test parameters). It verifies in Rust, and Rust writes the
// same string.
#[test]
fn auth_password_go_hash_verifies() {
    let go = "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$QOKF1iG70GOhZX71bkTGoINw9zqSjKFirULQQtMxBVI";
    assert_eq!(hash_with_salt(&TEST_PARAMS, GOOD, b"saltsaltsaltsalt"), go);
    assert_eq!(verify_password(go, GOOD).unwrap(), (true, TEST_PARAMS));
}

#[test]
fn auth_password_round_trip() {
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
    assert_eq!(password_length("pässwörd 🔑"), 10);
}

#[test]
fn auth_password_parse_hash_rejects() {
    let valid = hash_password(&TEST_PARAMS, GOOD);
    let parts: Vec<&str> = valid.split('$').collect();
    let with = |i: usize, v: &str| {
        let mut p = parts.clone();
        p[i] = v;
        p.join("$")
    };
    for bad in [
        String::new(),
        "plain".into(),
        format!("x{valid}"),
        with(1, "argon2i"),
        with(2, "v=16"),
        with(3, "m=64,t=1"),
        with(3, "m=064,t=1,p=1"),
        with(3, "m=4,t=1,p=1"),
        with(3, "m=64,t=0,p=1"),
        with(3, "m=64,t=1,p=0"),
        with(3, "m=99999999,t=1,p=1"),
        with(3, "m=64,t=65,p=1"),
        with(3, "m=64,t=1,p=256"),
        with(3, "m=+64,t=1,p=1"),
        with(4, "c2FsdA"),
        with(4, "!!!"),
        with(4, "c2FsdHNhbHRzYWx0c2FsdA=="),
        with(5, "a2V5"),
    ] {
        assert_eq!(parse_hash(&bad).map(|_| ()), Err(InvalidHash), "{bad}");
        assert!(verify_password(&bad, GOOD).is_err(), "{bad}");
    }
}

#[test]
fn auth_password_check_params() {
    assert!(check_params(&DEFAULT_PARAMS));
    assert!(check_params(&TEST_PARAMS));
    assert!(!check_params(&Params {
        memory: 1,
        ..TEST_PARAMS
    }));
    assert!(!check_params(&Params {
        salt_len: 4,
        ..TEST_PARAMS
    }));
    assert!(!check_params(&Params {
        key_len: 8,
        ..TEST_PARAMS
    }));
    assert!(default_hash_slots() >= 1);
}

// TestHashSlots "cancel": a request that stops waiting frees its place.
#[tokio::test]
async fn auth_hash_slots_cancel() {
    let slots = HashSlots::new(1, 1, Duration::from_secs(60));
    let held = slots.hold().unwrap();
    let ran = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let r = ran.clone();
    let waited = tokio::time::timeout(
        Duration::from_millis(10),
        slots.run(move || r.store(true, std::sync::atomic::Ordering::SeqCst)),
    )
    .await;
    assert!(waited.is_err(), "the run did not wait for the slot");
    assert!(!ran.load(std::sync::atomic::Ordering::SeqCst));
    drop(held);
    let r = ran.clone();
    slots
        .run(move || r.store(true, std::sync::atomic::Ordering::SeqCst))
        .await
        .unwrap();
    assert!(ran.load(std::sync::atomic::Ordering::SeqCst));
    assert_eq!((slots.free_slots(), slots.free_places()), (1, 2));
}

// TestHashSlots "full queue".
#[tokio::test]
async fn auth_hash_slots_full_queue() {
    let slots = HashSlots::new(1, 1, Duration::from_secs(60));
    let (tx, rx) = std::sync::mpsc::channel::<()>();
    let s = slots.clone();
    let running = tokio::spawn(async move {
        s.run(move || {
            let _ = rx.recv();
        })
        .await
    });
    while slots.free_slots() != 0 {
        tokio::task::yield_now().await;
    }
    let s = slots.clone();
    let waiting = tokio::spawn(async move { s.run(|| ()).await });
    while slots.free_places() != 0 {
        tokio::task::yield_now().await;
    }
    assert_eq!(slots.admit().unwrap_err(), HashError::Busy);
    let started = std::time::Instant::now();
    assert_eq!(slots.run(|| ()).await.unwrap_err(), HashError::Busy);
    assert!(
        started.elapsed() < Duration::from_secs(1),
        "a full queue waited"
    );
    tx.send(()).unwrap();
    running.await.unwrap().unwrap();
    waiting.await.unwrap().unwrap();
}

// TestHashSlots "admit".
#[tokio::test]
async fn auth_hash_slots_admit() {
    let slots = HashSlots::new(1, 0, Duration::from_secs(60));
    let place = slots.admit().unwrap();
    assert_eq!(slots.admit().unwrap_err(), HashError::Busy);
    slots.run_admitted(&place, || ()).await.unwrap();
    // The rehash of a login runs in the same place.
    slots.run_admitted(&place, || ()).await.unwrap();
    drop(place);
    assert_eq!((slots.free_slots(), slots.free_places()), (1, 1));
}

// TestHashSlots "wait ends".
#[tokio::test]
async fn auth_hash_slots_wait_ends() {
    let slots = HashSlots::new(1, 1, Duration::from_millis(10));
    let _held = slots.hold().unwrap();
    let started = std::time::Instant::now();
    assert_eq!(slots.run(|| ()).await.unwrap_err(), HashError::Busy);
    assert!(started.elapsed() >= Duration::from_millis(10));
    assert_eq!(slots.free_places(), 2, "a busy run kept its place");
}

#[tokio::test]
async fn auth_hash_slots_a_cancelled_request_keeps_its_place_until_the_hash_ends() {
    let slots = HashSlots::new(1, 0, Duration::from_secs(1));
    let (tx, rx) = std::sync::mpsc::channel::<()>();
    let s = slots.clone();
    let req = tokio::spawn(async move {
        s.run(move || {
            let _ = rx.recv();
        })
        .await
    });
    while slots.free_slots() != 0 {
        tokio::task::yield_now().await;
    }
    req.abort();
    let _ = req.await;
    assert_eq!(slots.free_places(), 0, "the hash still runs");
    tx.send(()).unwrap();
    for _ in 0..200 {
        if slots.free_places() == 1 {
            return;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    panic!("the place did not return after the hash ended");
}

#[tokio::test]
async fn auth_hash_slots_a_panic_is_failed() {
    let slots = HashSlots::new(1, 0, Duration::from_secs(1));
    let r: Result<(), HashError> = slots.run(|| panic!("boom")).await;
    assert_eq!(r.unwrap_err(), HashError::Failed);
    assert_eq!((slots.free_slots(), slots.free_places()), (1, 1));
}
