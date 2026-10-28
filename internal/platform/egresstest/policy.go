// Package egresstest grants narrowly scoped network access to local tests.
package egresstest

import (
	"crypto/x509"
	"net/netip"
	"testing"
)

// Policy grants nothing at its zero value; callers cannot forge its private capability.
type Policy struct {
	loopback bool
	roots    *x509.CertPool
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
