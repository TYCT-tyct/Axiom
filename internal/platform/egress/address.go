package egress

import (
	"errors"
	"net/netip"
	"strings"

	"axiom/internal/platform/egresstest"
)

// ErrAddressDenied includes empty DNS answers: absence of a safe target must fail closed.
var ErrAddressDenied = errors.New("egress: target address is not public")

// The IANA registries include globally reachable exceptions inside special-use ranges.
// Denying the whole aggregates avoids depending on those exceptions for upstream access.
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
// netip's predicates cover private, loopback, link-local and multicast ranges separately, and with them the metadata
// endpoints at 169.254.169.254 and fd00:ec2::254; 100.100.100.200 and 192.0.0.192 fall in the ranges below.
// https://learn.microsoft.com/azure/virtual-network/what-is-ip-address-168-63-129-16
var specialUse = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),         // RFC 1122: this network
	netip.MustParsePrefix("100.64.0.0/10"),     // RFC 6598: shared address space
	netip.MustParsePrefix("192.0.0.0/24"),      // RFC 6890: protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),      // RFC 5737: documentation
	netip.MustParsePrefix("192.31.196.0/24"),   // RFC 7535: AS112
	netip.MustParsePrefix("192.52.193.0/24"),   // RFC 7450: automatic multicast tunneling
	netip.MustParsePrefix("192.88.99.0/24"),    // RFC 7526: deprecated 6to4 relay
	netip.MustParsePrefix("192.175.48.0/24"),   // RFC 7534: AS112
	netip.MustParsePrefix("198.18.0.0/15"),     // RFC 2544: benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),   // RFC 5737: documentation
	netip.MustParsePrefix("203.0.113.0/24"),    // RFC 5737: documentation
	netip.MustParsePrefix("240.0.0.0/4"),       // RFC 1112: reserved, including limited broadcast
	netip.MustParsePrefix("168.63.129.16/32"),  // Azure WireServer, the one metadata host in public space
	netip.MustParsePrefix("2001::/23"),         // RFC 2928: protocol assignments, including Teredo/ORCHID
	netip.MustParsePrefix("2001:db8::/32"),     // RFC 3849: documentation
	netip.MustParsePrefix("2002::/16"),         // RFC 3056: 6to4 embeds an IPv4 destination
	netip.MustParsePrefix("2620:4f:8000::/48"), // RFC 7534: AS112
	netip.MustParsePrefix("3fff::/20"),         // RFC 9637: documentation
}

// RFC 4291/IANA IPv6 unicast allocations: fail closed outside current global unicast.
// This also excludes translation, discard, site-local and segment-routing ranges.
var globalIPv6 = netip.MustParsePrefix("2000::/3")

func allowedAddress(ip netip.Addr, policy egresstest.Policy) bool {
	if policy.Allows(ip) {
		return true
	}
	// Today IsGlobalUnicast and the 2000::/3 check below already refuse zones, mapped addresses and loopback; the
	// explicit checks keep refusing them if that prefix is ever widened (for example for DNS64).
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() ||
		ip.IsPrivate() || ip.IsLoopback() {
		return false
	}
	if ip.Is6() && !globalIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range specialUse {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Numeric host spellings are interpreted differently by OS resolvers; only netip's canonical form is safe.
func numericHost(host string) bool {
	for label := range strings.SplitSeq(strings.TrimSuffix(host, "."), ".") {
		digits := "0123456789"
		if strings.HasPrefix(strings.ToLower(label), "0x") {
			label, digits = label[2:], "0123456789abcdefABCDEF"
		}
		if label == "" || strings.Trim(label, digits) != "" {
			return false
		}
	}
	return true
}
