package gateway

import (
	"net/http"
	"net/netip"
)

// clientIP is the address the connection came from. M1 trusts no proxy header: the node is reached directly or
// through a CDN whose addresses AXIOM_TRUSTED_PROXIES will name when a deployment needs it (internal/accounts/README.md
// 「登录前的资源」).
func clientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// ipKey is what the per-address limits count by. An IPv6 customer holds a whole /64, so counting its addresses one by
// one would let a single host take as many shares as it has addresses.
func ipKey(a netip.Addr) string {
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}
