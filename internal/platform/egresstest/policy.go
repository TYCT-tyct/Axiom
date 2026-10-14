// Package egresstest grants narrowly scoped network access to local tests.
package egresstest

import (
	"net/netip"
	"testing"
)

// Policy grants nothing at its zero value; callers cannot forge its private capability.
type Policy struct{ loopback bool }

// Loopback requires a test binary so importing this package cannot enable a production bypass.
func Loopback(t testing.TB) Policy {
	if !testing.Testing() {
		panic("egresstest: loopback access requires a test binary")
	}
	t.Helper()
	return Policy{loopback: true}
}

// Allows deliberately excludes mapped addresses and zones even in tests.
func (p Policy) Allows(ip netip.Addr) bool {
	return p.loopback && ip.Zone() == "" && !ip.Is4In6() && ip.IsLoopback()
}
