package auth

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// maxForwarded is the number of X-Forwarded-For entries that the API reads,
// counted from the right. The entries on the left are written by the client.
const maxForwarded = 16

// privateRanges is the meaning of the word "private" in AUTH_TRUSTED_PROXIES:
// loopback, RFC 1918, unique local addresses (fc00::/7), carrier-grade NAT, and
// link-local addresses. Docker networks, Vite, and Node use these addresses.
var privateRanges = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	"100.64.0.0/10", "169.254.0.0/16", "fe80::/10",
}

var loopbackRanges = []string{"127.0.0.0/8", "::1/128"}

// proxySet is the set of peer addresses that may write X-Forwarded-For.
type proxySet struct {
	prefixes []netip.Prefix
}

// parseProxySet reads the items of AUTH_TRUSTED_PROXIES or auth.WithTrustedProxies.
func parseProxySet(items []string) (proxySet, error) {
	var set proxySet
	add := func(ranges []string) {
		for _, s := range ranges {
			set.prefixes = append(set.prefixes, netip.MustParsePrefix(s))
		}
	}
	for _, item := range items {
		item = strings.TrimSpace(item)
		switch strings.ToLower(item) {
		case "":
		case "private":
			add(privateRanges)
		case "loopback":
			add(loopbackRanges)
		case "none":
		default:
			if p, err := netip.ParsePrefix(item); err == nil {
				set.prefixes = append(set.prefixes, p.Masked())
			} else if a, err := netip.ParseAddr(item); err == nil {
				a = a.Unmap().WithZone("")
				set.prefixes = append(set.prefixes, netip.PrefixFrom(a, a.BitLen()))
			} else {
				return set, fmt.Errorf("auth: cannot start: AUTH_TRUSTED_PROXIES item %q is not a CIDR, an address, or one of private, loopback, none\nTry: use items such as private, 10.0.0.0/8, or 203.0.113.7", item)
			}
		}
	}
	return set, nil
}

func (p proxySet) trusts(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	for _, prefix := range p.prefixes {
		if prefix.Contains(a) {
			return true
		}
	}
	return false
}

type clientKind uint8

const (
	// kindAddr is a client with an address of its own.
	kindAddr clientKind = iota
	// kindExempt is a local process, such as Vite, curl, or a health check.
	kindExempt
	// kindInfra is a trusted proxy that did not tell the address of its client.
	kindInfra
)

// client is the result of resolve.
type client struct {
	kind clientKind
	addr netip.Addr
}

// resolve finds the client of a request.
//
//   - A peer outside the trusted set is the client. The API ignores every forwarding header.
//   - A trusted peer is a proxy. The client is the first X-Forwarded-For entry
//     from the right that is not in the trusted set. A client cannot choose that
//     entry, because the proxy writes it. Entries to its left are not read.
//   - Without such an entry, a loopback peer is exempt. Any other trusted peer
//     goes into the shared infrastructure bucket. It is never unlimited.
//
// Loopback is exempt only when the trusted set holds it, so
// AUTH_TRUSTED_PROXIES=none limits local requests too.
func (p proxySet) resolve(r *http.Request) client {
	peer, ok := peerAddr(r.RemoteAddr)
	if !ok {
		// A unix socket has no IP address. Its peer is a local process.
		peer = netip.MustParseAddr("127.0.0.1")
	}
	if !p.trusts(peer) {
		return client{kind: kindAddr, addr: peer}
	}
	for _, entry := range forwardedEntries(r.Header.Values("X-Forwarded-For")) {
		addr, ok := parseForwardedEntry(entry)
		if !ok {
			break // garbage: do not guess
		}
		if !p.trusts(addr) {
			return client{kind: kindAddr, addr: addr}
		}
	}
	if peer.IsLoopback() {
		return client{kind: kindExempt}
	}
	return client{kind: kindInfra}
}

func peerAddr(remote string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		a, err := netip.ParseAddr(remote)
		if err != nil {
			return netip.Addr{}, false
		}
		return a.Unmap().WithZone(""), true
	}
	return ap.Addr().Unmap().WithZone(""), true
}

// forwardedEntries returns the last maxForwarded entries of the X-Forwarded-For
// header lines, the newest (rightmost) first. It reads from the right, so a long
// header costs no more than a short one.
func forwardedEntries(lines []string) []string {
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < maxForwarded; i-- {
		line := lines[i]
		for len(line) > 0 && len(out) < maxForwarded {
			cut := strings.LastIndexByte(line, ',')
			out = append(out, strings.TrimSpace(line[cut+1:]))
			if cut < 0 {
				break
			}
			line = line[:cut]
		}
	}
	return out
}

// parseForwardedEntry reads "1.2.3.4", "1.2.3.4:5", "[::1]:5", "[::1]", and "::1".
func parseForwardedEntry(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if t, ok := strings.CutPrefix(s, "["); ok {
		if t, ok = strings.CutSuffix(t, "]"); ok {
			if a, err := netip.ParseAddr(t); err == nil {
				return a.Unmap().WithZone(""), true
			}
		}
	}
	return netip.Addr{}, false
}
