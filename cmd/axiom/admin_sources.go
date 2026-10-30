package main

import (
	"net/http"
	"net/netip"

	"axiom/internal/platform/clientip"
)

// restrictSources answers only requests from the admin listener's allowed networks. The address is read the same way
// the console and the gateway read it (platform/clientip): a proxy is believed only when AXIOM_TRUSTED_PROXIES names
// it, so a client cannot choose its own answer through X-Forwarded-For. A denied request gets a bare 403: it is not
// the admin API's answer, and its body must not tell a scanner which networks are allowed.
func restrictSources(next http.Handler, sources, trusted []netip.Prefix) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := clientip.Addr(r, trusted)
		for _, source := range sources {
			if source.Contains(addr) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
	})
}
