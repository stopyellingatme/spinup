use clap::Parser;
use std::sync::Arc;

#[derive(Parser)]
#[command(name = "spinup", version = option_env!("SPINUP_VERSION").unwrap_or(env!("CARGO_PKG_VERSION")))]
struct Cli {
    /// Exercise the linked TLS provider and zstd without network access.
    #[arg(long)]
    self_check: bool,
}

fn self_check() -> Result<(), Box<dyn std::error::Error>> {
    let provider = rustls::crypto::ring::default_provider();
    let mut random = [0; 32];
    provider
        .secure_random
        .fill(&mut random)
        .map_err(|_| std::io::Error::other("ring random generation failed"))?;
    let _agent: ureq::Agent = ureq::Agent::config_builder()
        .tls_config(
            ureq::tls::TlsConfig::builder()
                .unversioned_rustls_crypto_provider(Arc::new(provider))
                .build(),
        )
        .build()
        .into();
    let payload = b"spinup release spike";
    let compressed = zstd::stream::encode_all(payload.as_slice(), 1)?;
    let decoded = zstd::stream::decode_all(compressed.as_slice())?;
    if decoded != payload {
        return Err("zstd round trip differs".into());
    }
    println!("TLS=rustls provider=ring HTTP=ureq compression=zstd self-check=ok");
    Ok(())
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    if Cli::parse().self_check {
        self_check()?;
    }
    Ok(())
}
