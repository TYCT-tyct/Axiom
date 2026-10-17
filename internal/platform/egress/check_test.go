package egress

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"axiom/internal/platform/egresstest"
)

// TestCheckAppliesTheDialPolicy: Check refuses what a dial would: a host that resolves to anything but public
// addresses, to nothing, or is written as a number, and passes a host whose every address is public.
func TestCheckAppliesTheDialPolicy(t *testing.T) {
	t.Parallel()
	answers := map[string][]string{
		"public.example":  {"8.8.8.8", "2606:4700::1111"},
		"private.example": {"10.0.0.1"},
		"mixed.example":   {"8.8.8.8", "169.254.169.254"},
		"empty.example":   nil,
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		list, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var ips []netip.Addr
		for _, a := range list {
			ips = append(ips, netip.MustParseAddr(a))
		}
		return ips, nil
	}
	c := newClient(egresstest.Policy{}, lookup)
	for host, denied := range map[string]bool{
		"public.example": false, "private.example": true, "mixed.example": true, "empty.example": true,
		"8.8.8.8": false, "127.0.0.1": true, "10.1.2.3": true, "0x7f.1": true, "2130706433": true,
	} {
		if err := c.Check(t.Context(), host); errors.Is(err, ErrAddressDenied) != denied || err != nil && !denied {
			t.Errorf("Check(%q) = %v, want denied=%v", host, err, denied)
		}
	}
	if err := c.Check(t.Context(), "nowhere.example"); err == nil || errors.Is(err, ErrAddressDenied) {
		t.Errorf("a host that does not resolve: %v, want the resolver's error", err)
	}
}
