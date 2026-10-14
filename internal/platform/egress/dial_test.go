package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func fixedLookup(addresses ...string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(context.Context, string, string) ([]netip.Addr, error) {
		result := make([]netip.Addr, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, netip.MustParseAddr(address))
		}
		return result, nil
	}
}

func TestDNSMustValidateEveryAnswerBeforeDial(t *testing.T) {
	for _, addresses := range [][]string{
		{"8.8.8.8", "127.0.0.1"},
		{"10.0.0.1", "8.8.8.8"},
		{"2606:4700::1111", "fd00:ec2::254"},
		{"8.8.8.8", "::ffff:127.0.0.1"},
		{},
	} {
		d := dialer{
			lookup: fixedLookup(addresses...),
			connect: func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("dial started before the whole DNS answer was accepted")
				return nil, errors.New("unexpected connection")
			},
		}
		if _, err := d.dialContext(t.Context(), "tcp", "upstream.test:443"); !errors.Is(err, ErrAddressDenied) {
			t.Errorf("addresses %v: %v", addresses, err)
		}
	}
}

func TestDialPinsAddressesAndBoundsResolution(t *testing.T) {
	lookups := 0
	var dialed []string
	failed := errors.New("connection failed")
	d := dialer{
		lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			lookups++
			if _, ok := ctx.Deadline(); !ok || network != "ip" || host != "upstream.test" {
				t.Fatalf("unbounded or incorrect DNS query: %s %s", network, host)
			}
			// Go's pure resolver returns /etc/hosts IPv4 entries IPv4-mapped; they are dialed as IPv4.
			return []netip.Addr{netip.MustParseAddr("::ffff:8.8.8.8"), netip.MustParseAddr("2606:4700::1111")}, nil
		},
		connect: func(ctx context.Context, network, address string) (net.Conn, error) {
			if _, ok := ctx.Deadline(); !ok || network != "tcp" {
				t.Fatal("unbounded or incorrect connection")
			}
			dialed = append(dialed, address)
			return nil, failed
		},
	}
	if _, err := d.dialContext(t.Context(), "tcp", "upstream.test:443"); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if lookups != 1 || !slices.Equal(dialed, []string{"8.8.8.8:443", "[2606:4700::1111]:443"}) {
		t.Fatalf("lookups=%d, dialed=%v", lookups, dialed)
	}
}

func TestBlackholedAddressLeavesTimeForTheNext(t *testing.T) {
	d := dialer{
		lookup: fixedLookup("2606:4700::1111", "8.8.8.8"),
		connect: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address == "[2606:4700::1111]:443" {
				<-ctx.Done() // a blackholed route never answers
				return nil, ctx.Err()
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			client, server := net.Pipe()
			mustClose(t, server)
			return client, nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	conn, err := d.dialContext(ctx, "tcp", "upstream.test:443")
	if err != nil {
		t.Fatalf("the reachable second address was not tried in time: %v", err)
	}
	mustClose(t, conn)
}
