// Package clientip reads the address a request came from: the console and the model API judge requests by the same
// address, so a node behind a reverse proxy charges, limits and checks its keys against the client and not the proxy.
package clientip

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Group is the source address a rate budget counts: mapped IPv4 is IPv4, and an IPv6 allocation shares its /64.
func Group(ip netip.Addr) netip.Addr {
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().Addr()
	}
	return ip
}

// Addr is the address r came from. Only the connection's own address is believed, unless it is a trusted proxy
// (AXIOM_TRUSTED_PROXIES): then X-Forwarded-For is read from its right, past the proxies, to the first address that is
// not one (a client can put anything at the left of the header, but not past the proxy that appends to it). trusted is
// the deployment's list, empty when nothing is trusted, and a peer the list does not name cannot speak for anyone.
// A header that cannot be read is not believed at all.
func Addr(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	// A zone names a local interface, not a client; kept, it would make one client many (a new map key per suffix)
	// and never match a trusted prefix.
	addr := remote.Addr().Unmap().WithZone("")
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(addr) {
		return addr
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for hop := range strings.SplitSeq(header, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	for _, text := range slices.Backward(hops) {
		hop, err := netip.ParseAddr(text)
		if err != nil {
			return addr // a header we cannot read is not believed
		}
		if hop = hop.Unmap().WithZone(""); !isTrusted(hop) {
			return hop
		}
	}
	return addr
}
