package gateway

import (
	"net/http"
	"net/netip"

	"axiom/internal/platform/clientip"
)

// clientIP is the address the request came from, read through the reverse proxies the deployment trusts
// (AXIOM_TRUSTED_PROXIES, internal/platform/clientip): the IP limit and the key's allow-list judge the client, not the
// proxy in front of it.
func (h *Handler) clientIP(r *http.Request) netip.Addr {
	return clientip.Addr(r, h.Proxies)
}
