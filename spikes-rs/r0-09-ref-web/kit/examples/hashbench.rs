//! Times one argon2id hash with the kit default parameters (64 MiB, t=3,
//! p=4), and the throughput of N parallel hashes. Run it in release mode:
//! `cargo run --release --example hashbench -p spinup-kit`.

use std::time::{Duration, Instant};

use kit::auth::password::{DEFAULT_PARAMS, hash_password};

fn main() {
    let mut ds: Vec<Duration> = (0..10)
        .map(|_| {
            let t = Instant::now();
            hash_password(&DEFAULT_PARAMS, "correct horse battery");
            t.elapsed()
        })
        .collect();
    ds.sort();
    println!(
        "rust argon2id m=65536 t=3 p=4: median {:?} min {:?} max {:?}",
        ds[5], ds[0], ds[9]
    );
    for n in [1usize, 4] {
        let t = Instant::now();
        std::thread::scope(|s| {
            for _ in 0..n {
                s.spawn(|| {
                    for _ in 0..4 {
                        hash_password(&DEFAULT_PARAMS, "correct horse battery");
                    }
                });
            }
        });
        let per = t.elapsed() / (4 * n as u32);
        println!("{n} threads: {:?} wall per hash", per);
    }
}
