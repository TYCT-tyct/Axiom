// Package egresstest grants narrowly scoped network access to local tests.
package egresstest

import (
	"crypto/x509"
	"maps"
	"net/netip"
	"testing"
)

// Policy grants nothing at its zero value; callers cannot forge its private capability.
type Policy struct {
	loopback bool
	roots    *x509.CertPool
	routes   map[string]netip.AddrPort
}

// Loopback requires a test binary; optional certificates trust only the test servers named by the caller.
func Loopback(t testing.TB, certificates ...*x509.Certificate) Policy {
	if !testing.Testing() {
		panic("egresstest: loopback access requires a test binary")
	}
	t.Helper()
	p := Policy{loopback: true}
	if len(certificates) > 0 {
		p.roots = x509.NewCertPool()
		for _, cert := range certificates {
			p.roots.AddCert(cert)
		}
	}
	return p
}

// RootCAs returns an independent copy of test-only trust roots; zero Policy preserves system trust.
func (p Policy) RootCAs() *x509.CertPool {
	if p.roots == nil {
		return nil
	}
	return p.roots.Clone()
}

// Allows deliberately excludes mapped addresses and zones even in tests.
func (p Policy) Allows(ip netip.Addr) bool {
	return p.loopback && ip.Zone() == "" && !ip.Is4In6() && ip.IsLoopback()
}

// Route sends every connection to host, on any port, to at instead of where host resolves, so that a test reaches its
// fake of a fixed public address (a cloud platform's) by that address's own name and certificate. The dial still
// applies the address policy to at, so a route works only on a Loopback policy.
func (p Policy) Route(host string, at netip.AddrPort) Policy {
	routes := maps.Clone(p.routes)
	if routes == nil {
		routes = map[string]netip.AddrPort{}
	}
	routes[host] = at
	p.routes = routes
	return p
}

// Routed is where Route sends host; false for a host it does not route.
func (p Policy) Routed(host string) (netip.AddrPort, bool) {
	at, ok := p.routes[host]
	return at, ok
}
