use spinup_kit::auth::clientip::*;
use std::net::IpAddr;

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
    let cases: Vec<Case<'_>> = vec![
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

#[test]
fn clientip_connection_and_invalid_forwarding_bytes() {
    use axum::{
        body::Body,
        extract::{ConnectInfo, connect_info::MockConnectInfo},
        http::{HeaderValue, Request},
    };
    let proxies = TrustedProxies::parse(&["private"]).unwrap();
    let mut request = Request::new(Body::empty());
    request.extensions_mut().insert(MockConnectInfo(
        "203.0.113.5:1234".parse::<std::net::SocketAddr>().unwrap(),
    ));
    request
        .headers_mut()
        .insert("x-forwarded-for", HeaderValue::from_static("198.51.100.1"));
    assert_eq!(
        client_addr(&proxies, &request),
        Client::addr(ip("203.0.113.5"))
    );
    // A real connection takes precedence over the test extension.
    request.extensions_mut().insert(ConnectInfo(
        "172.18.0.4:1234".parse::<std::net::SocketAddr>().unwrap(),
    ));
    assert_eq!(
        client_addr(&proxies, &request),
        Client::addr(ip("198.51.100.1"))
    );
    request
        .headers_mut()
        .append("x-forwarded-for", HeaderValue::from_bytes(&[0xff]).unwrap());
    assert_eq!(client_addr(&proxies, &request), Client::infra());
}
