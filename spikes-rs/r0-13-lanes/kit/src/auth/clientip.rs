//! The client address of a request (ADR 0016 §3), as `kit/auth/clientip.go`.

use std::net::{IpAddr, Ipv4Addr, SocketAddr};

use super::AuthError;

/// The number of X-Forwarded-For entries that the API reads, counted from
/// the right. The entries on the left are written by the client.
pub const MAX_FORWARDED: usize = 16;

/// The meaning of the word `private` in AUTH_TRUSTED_PROXIES: loopback,
/// RFC 1918, unique local addresses, carrier-grade NAT, and link-local.
const PRIVATE_RANGES: &[&str] = &[
    "127.0.0.0/8",
    "::1/128",
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "fc00::/7",
    "100.64.0.0/10",
    "169.254.0.0/16",
    "fe80::/10",
];

const LOOPBACK_RANGES: &[&str] = &["127.0.0.0/8", "::1/128"];

/// An IP prefix. The address has its host bits set to zero.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Prefix {
    addr: IpAddr,
    bits: u8,
}

impl Prefix {
    /// Parses `a.b.c.d/n` or `x::y/n`, as Go `netip.ParsePrefix` (no zone).
    pub fn parse(s: &str) -> Option<Self> {
        let (a, b) = s.split_once('/')?;
        if b.is_empty() || b.len() > 3 || !b.bytes().all(|c| c.is_ascii_digit()) {
            return None;
        }
        if b.len() > 1 && b.starts_with('0') {
            return None;
        }
        let addr: IpAddr = a.parse().ok()?;
        let bits: u8 = b.parse().ok()?;
        let max = if addr.is_ipv4() { 32 } else { 128 };
        if bits > max {
            return None;
        }
        Some(Self {
            addr: mask(addr, bits),
            bits,
        })
    }

    fn host(addr: IpAddr) -> Self {
        let bits = if addr.is_ipv4() { 32 } else { 128 };
        Self { addr, bits }
    }

    /// Reports whether the prefix holds `a`. An IPv4 prefix never holds an
    /// IPv6 address, as in Go.
    pub fn contains(&self, a: IpAddr) -> bool {
        match (self.addr, a) {
            (IpAddr::V4(_), IpAddr::V4(_)) | (IpAddr::V6(_), IpAddr::V6(_)) => {
                mask(a, self.bits) == self.addr
            }
            _ => false,
        }
    }
}

/// Returns the first `bits` bits of `a`, with the other bits zero.
pub fn mask(a: IpAddr, bits: u8) -> IpAddr {
    match a {
        IpAddr::V4(v4) => {
            let n = u32::from(v4);
            let m = if bits == 0 {
                0
            } else {
                u32::MAX << (32 - u32::from(bits.min(32)))
            };
            IpAddr::V4((n & m).into())
        }
        IpAddr::V6(v6) => {
            let n = u128::from(v6);
            let m = if bits == 0 {
                0
            } else {
                u128::MAX << (128 - u32::from(bits.min(128)))
            };
            IpAddr::V6((n & m).into())
        }
    }
}

/// Parses an address as Go `netip.ParseAddr` does. An IPv6 zone is dropped.
/// A mapped IPv4 address becomes IPv4 (`Unmap`).
pub fn parse_addr(s: &str) -> Option<IpAddr> {
    let s = match s.split_once('%') {
        Some((a, zone)) if a.contains(':') && !zone.is_empty() => a,
        Some(_) => return None,
        None => s,
    };
    s.parse::<IpAddr>().ok().map(|a| a.to_canonical())
}

/// Parses `host:port` as Go `netip.ParseAddrPort` does.
fn parse_addr_port(s: &str) -> Option<IpAddr> {
    if let Ok(sa) = s.parse::<SocketAddr>() {
        return Some(sa.ip().to_canonical());
    }
    // [fe80::1%eth0]:443: drop the zone, which SocketAddr does not read.
    let rest = s.strip_prefix('[')?;
    let (inner, port) = rest.split_once("]:")?;
    if port.is_empty() || port.parse::<u16>().is_err() {
        return None;
    }
    let addr = parse_addr(inner)?;
    addr.is_ipv6().then_some(addr)
}

/// The set of peer addresses that may write X-Forwarded-For.
#[derive(Debug, Clone, Default)]
pub struct ProxySet {
    prefixes: Vec<Prefix>,
}

impl ProxySet {
    /// Reads the items of AUTH_TRUSTED_PROXIES or `with_trusted_proxies`.
    pub fn parse<S: AsRef<str>>(items: &[S]) -> Result<Self, AuthError> {
        let mut set = Self::default();
        let builtin = |ranges: &[&str]| -> Vec<Prefix> {
            ranges
                .iter()
                .map(|r| Prefix::parse(r).expect("valid built-in range"))
                .collect()
        };
        for item in items {
            let item = item.as_ref().trim();
            match item.to_ascii_lowercase().as_str() {
                "" | "none" => {}
                "private" => set.prefixes.extend(builtin(PRIVATE_RANGES)),
                "loopback" => set.prefixes.extend(builtin(LOOPBACK_RANGES)),
                _ => {
                    if let Some(p) = Prefix::parse(item) {
                        set.prefixes.push(p);
                    } else if let Some(a) = parse_addr(item) {
                        set.prefixes.push(Prefix::host(a));
                    } else {
                        return Err(AuthError(format!(
                            "auth: cannot start: AUTH_TRUSTED_PROXIES item {} is not a CIDR, an address, or one of private, loopback, none\nTry: use items such as private, 10.0.0.0/8, or 203.0.113.7",
                            go_quote(item)
                        )));
                    }
                }
            }
        }
        Ok(set)
    }

    /// Reports whether the set trusts `a`.
    pub fn trusts(&self, a: IpAddr) -> bool {
        let a = a.to_canonical();
        self.prefixes.iter().any(|p| p.contains(a))
    }

    /// Finds the client of a request from the peer address (`remote`, in the
    /// form of Go's `RemoteAddr`) and the X-Forwarded-For header lines.
    ///
    /// - A peer outside the trusted set is the client. The API ignores every
    ///   forwarding header.
    /// - A trusted peer is a proxy. The client is the first entry from the
    ///   right that is not trusted. Entries to its left are not read.
    /// - Without such an entry, a loopback peer is exempt. Any other trusted
    ///   peer goes into the shared infrastructure bucket.
    pub fn resolve(&self, remote: &str, xff: &[&str]) -> Client {
        let peer = match peer_addr(remote) {
            Some(p) => p,
            None if !remote.is_empty() && remote != "@" => {
                // An unknown listener wrapper: do not guess, and do not exempt it.
                return Client::infra();
            }
            // A unix socket has no IP address. Its peer is a local process.
            None => IpAddr::V4(Ipv4Addr::LOCALHOST),
        };
        if !self.trusts(peer) {
            return Client::addr(peer);
        }
        for entry in forwarded_entries(xff) {
            let Some(addr) = parse_forwarded_entry(&entry) else {
                break; // garbage: do not guess
            };
            if !self.trusts(addr) {
                return Client::addr(addr);
            }
        }
        if peer.is_loopback() {
            Client {
                kind: ClientKind::Exempt,
                addr: None,
            }
        } else {
            Client::infra()
        }
    }
}

/// The kind of a client.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ClientKind {
    /// A client with an address of its own.
    Addr,
    /// A local process, such as Vite, curl, or a health check.
    Exempt,
    /// A trusted proxy that did not tell the address of its client.
    Infra,
}

/// The result of [`ProxySet::resolve`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Client {
    /// The kind.
    pub kind: ClientKind,
    /// The address of an `Addr` client.
    pub addr: Option<IpAddr>,
}

impl Client {
    /// A client with an address.
    pub fn addr(a: IpAddr) -> Self {
        Self {
            kind: ClientKind::Addr,
            addr: Some(a.to_canonical()),
        }
    }

    /// The infrastructure client.
    pub fn infra() -> Self {
        Self {
            kind: ClientKind::Infra,
            addr: None,
        }
    }
}

fn peer_addr(remote: &str) -> Option<IpAddr> {
    parse_addr_port(remote).or_else(|| parse_addr(remote))
}

/// Returns the last [`MAX_FORWARDED`] entries of the header lines, the
/// newest (rightmost) first. It reads from the right, so a long header costs
/// no more than a short one.
pub fn forwarded_entries(lines: &[&str]) -> Vec<String> {
    let mut out = Vec::new();
    for line in lines.iter().rev() {
        let mut line = *line;
        while !line.is_empty() && out.len() < MAX_FORWARDED {
            match line.rfind(',') {
                Some(cut) => {
                    out.push(line[cut + 1..].trim().to_owned());
                    line = &line[..cut];
                }
                None => {
                    out.push(line.trim().to_owned());
                    break;
                }
            }
        }
        if out.len() >= MAX_FORWARDED {
            break;
        }
    }
    out
}

/// Reads `1.2.3.4`, `1.2.3.4:5`, `[::1]:5`, `[::1]`, and `::1`.
fn parse_forwarded_entry(s: &str) -> Option<IpAddr> {
    let s = s.trim();
    parse_addr(s)
        .or_else(|| parse_addr_port(s))
        .or_else(|| parse_addr(s.strip_prefix('[')?.strip_suffix(']')?))
}

/// Quotes a string as Go's `%q` does for printable text.
pub fn go_quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c if c.is_control() => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ip(s: &str) -> IpAddr {
        parse_addr(s).unwrap()
    }

    // TestParseProxySet of clientip_test.go, unchanged.
    #[test]
    fn parse_proxy_set() {
        let cases: &[(&[&str], &[&str], &[&str])] = &[
            (
                &["private"],
                &[
                    "127.0.0.1",
                    "::1",
                    "10.1.2.3",
                    "172.18.0.4",
                    "192.168.1.1",
                    "fd00::1",
                    "100.64.0.1",
                    "169.254.1.1",
                    "fe80::1",
                    "::ffff:10.0.0.1",
                ],
                &["198.51.100.7", "172.32.0.1", "2001:db8::1", "100.128.0.1"],
            ),
            (&["loopback"], &["127.0.0.1", "::1"], &["10.0.0.1"]),
            (&["none"], &[], &["127.0.0.1", "10.0.0.1"]),
            (&[], &[], &["127.0.0.1"]),
            (
                &["private", "203.0.113.0/24", " 198.51.100.9 "],
                &["203.0.113.77", "198.51.100.9", "10.0.0.1"],
                &["198.51.100.10"],
            ),
            (
                &["172.16.0.0/12"],
                &["172.20.0.1"],
                &["192.168.1.1", "127.0.0.1"],
            ),
        ];
        for (items, trusted, not) in cases {
            let set = ProxySet::parse(items).unwrap();
            for s in *trusted {
                assert!(set.trusts(ip(s)), "{items:?} does not trust {s}");
            }
            for s in *not {
                assert!(!set.trusts(ip(s)), "{items:?} trusts {s}");
            }
        }
        for bad in ["10.0.0.0/33", "proxy", "10.0.0.256"] {
            let err = ProxySet::parse(&[bad]).unwrap_err().to_string();
            assert!(
                err.contains("\nTry: ") && err.contains(bad),
                "item {bad:?}: error = {err}, want the item and a Try line"
            );
        }
    }

    /// name, set, peer, X-Forwarded-For lines, kind, address.
    type Case<'a> = (
        &'a str,
        &'a ProxySet,
        &'a str,
        Vec<&'a str>,
        ClientKind,
        &'a str,
    );

    // TestResolveClient of clientip_test.go, unchanged.
    #[test]
    fn resolve_client() {
        use ClientKind::*;
        let private = ProxySet::parse(&["private"]).unwrap();
        let none = ProxySet::parse(&["none"]).unwrap();
        let many = vec!["10.0.0.1"; 20].join(", ");
        let many_line = format!("203.0.113.5, {many}");
        let cases: Vec<Case> = vec![
            (
                "direct client ignores a forged header",
                &private,
                "203.0.113.5:4000",
                vec!["198.51.100.1"],
                Addr,
                "203.0.113.5",
            ),
            (
                "direct client ignores a forged private header",
                &private,
                "203.0.113.5:4000",
                vec!["10.0.0.9"],
                Addr,
                "203.0.113.5",
            ),
            (
                "proxy writes the client",
                &private,
                "172.18.0.4:9000",
                vec!["198.51.100.1"],
                Addr,
                "198.51.100.1",
            ),
            (
                "forged private entry on the left",
                &private,
                "172.18.0.4:9000",
                vec!["10.0.0.9, 203.0.113.5"],
                Addr,
                "203.0.113.5",
            ),
            (
                "forged public entry on the left",
                &private,
                "172.18.0.4:9000",
                vec!["198.51.100.1, 203.0.113.5"],
                Addr,
                "203.0.113.5",
            ),
            (
                "another proxy on the right",
                &private,
                "172.18.0.4:9000",
                vec!["203.0.113.5, 10.0.0.2"],
                Addr,
                "203.0.113.5",
            ),
            (
                "garbage on the right",
                &private,
                "172.18.0.4:9000",
                vec!["203.0.113.5, garbage"],
                Infra,
                "",
            ),
            (
                "all entries trusted",
                &private,
                "172.18.0.4:9000",
                vec!["10.0.0.2, 10.0.0.3"],
                Infra,
                "",
            ),
            (
                "no header from a proxy",
                &private,
                "172.18.0.4:9000",
                vec![],
                Infra,
                "",
            ),
            (
                "no header from loopback",
                &private,
                "127.0.0.1:9000",
                vec![],
                Exempt,
                "",
            ),
            (
                "loopback peer with loopback entry",
                &private,
                "[::1]:9000",
                vec!["127.0.0.1"],
                Exempt,
                "",
            ),
            (
                "loopback peer with garbage",
                &private,
                "127.0.0.1:9000",
                vec!["garbage"],
                Exempt,
                "",
            ),
            (
                "loopback peer with a public entry",
                &private,
                "127.0.0.1:9000",
                vec!["198.51.100.1"],
                Addr,
                "198.51.100.1",
            ),
            (
                "proxy that says loopback is not exempt",
                &private,
                "172.18.0.4:9000",
                vec!["127.0.0.1"],
                Infra,
                "",
            ),
            (
                "port in the entry",
                &private,
                "10.0.0.2:1",
                vec!["198.51.100.1:5555"],
                Addr,
                "198.51.100.1",
            ),
            (
                "ipv6 with brackets and port",
                &private,
                "10.0.0.2:1",
                vec!["[2001:db8::1]:443"],
                Addr,
                "2001:db8::1",
            ),
            (
                "ipv6 with brackets",
                &private,
                "10.0.0.2:1",
                vec!["[2001:db8::1]"],
                Addr,
                "2001:db8::1",
            ),
            (
                "mapped address",
                &private,
                "10.0.0.2:1",
                vec!["::ffff:203.0.113.5"],
                Addr,
                "203.0.113.5",
            ),
            (
                "zone is dropped",
                &private,
                "10.0.0.2:1",
                vec!["2001:db8::1%eth0"],
                Addr,
                "2001:db8::1",
            ),
            (
                "several header lines",
                &private,
                "10.0.0.2:1",
                vec!["198.51.100.1", "203.0.113.5"],
                Addr,
                "203.0.113.5",
            ),
            (
                "more than 16 entries",
                &private,
                "10.0.0.2:1",
                vec![many_line.as_str()],
                Infra,
                "",
            ),
            (
                "none limits loopback",
                &none,
                "127.0.0.1:9000",
                vec!["198.51.100.1"],
                Addr,
                "127.0.0.1",
            ),
            (
                "unix socket peer is loopback",
                &private,
                "@",
                vec![],
                Exempt,
                "",
            ),
            (
                "empty peer is a unix socket",
                &private,
                "",
                vec![],
                Exempt,
                "",
            ),
            (
                "unparsable peer is not exempt",
                &private,
                "proxy.internal",
                vec![],
                Infra,
                "",
            ),
        ];
        for (name, set, remote, xff, kind, addr) in cases {
            let got = set.resolve(remote, &xff);
            assert_eq!(got.kind, kind, "{name}: {got:?}");
            if !addr.is_empty() {
                assert_eq!(got.addr, Some(ip(addr)), "{name}");
            }
        }
    }

    // TestForwardedEntriesReadsFromTheRight of clientip_test.go, unchanged.
    #[test]
    fn forwarded_entries_read_from_the_right() {
        let long = format!("{}y", "x,".repeat(100));
        let got = forwarded_entries(&["a, b", "c", &long]);
        assert!(
            got.len() == MAX_FORWARDED && got[0] == "y" && got[1] == "x",
            "{got:?}"
        );
        let got = forwarded_entries(&["a, b", "c"]);
        assert!(got.len() == 3 && got[0] == "c" && got[2] == "a", "{got:?}");
    }
}
