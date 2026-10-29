package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"axiom/internal/platform/egresstest"
)

func TestTCPRevalidatesDNSAndPinsTheConnection(t *testing.T) {
	t.Parallel()
	lookups := 0
	dialed := ""
	probeErr := errors.New("fixture reached the validated IP")
	c := newClient(egresstest.Policy{}, func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups <= 2 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	c.dialer.connect = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, probeErr
	}
	if err := c.Check(t.Context(), "rebind.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DialContext(t.Context(), "rebind.example:6379"); !errors.Is(err, probeErr) || dialed != "8.8.8.8:6379" {
		t.Fatalf("unpinned TCP: %v dialed=%s", err, dialed)
	}
	dialed = ""
	if _, err := c.DialContext(t.Context(), "rebind.example:6379"); !errors.Is(err, ErrAddressDenied) || dialed != "" {
		t.Fatalf("rebound DNS: %v dialed=%s", err, dialed)
	}
	for _, host := range []string{"127.0.0.1", "10.0.0.5", "169.254.169.254", "[::1]"} {
		if _, err := NewDirect().DialContext(t.Context(), host+":6379"); !errors.Is(err, ErrAddressDenied) {
			t.Fatalf("%s: %v", host, err)
		}
	}
}
