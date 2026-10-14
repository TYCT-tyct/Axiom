package egress

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"axiom/internal/platform/egresstest"
)

func TestAddressPolicy(t *testing.T) {
	denied := []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.127.255.255",
		"127.0.0.1", "169.254.1.1", "169.254.169.254", "172.16.0.1", "172.31.255.255",
		"192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1",
		"192.168.1.1", "192.175.48.1", "198.18.0.1", "198.19.255.255", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::127.0.0.1", "::ffff:127.0.0.1", "::ffff:10.1.2.3", "::ffff:8.8.8.8",
		"64:ff9b::7f00:1", "64:ff9b:1::a00:1", "100::1", "100:0:0:1::1",
		"2001::1", "2001:2::1", "2001:3::1", "2001:4:112::1", "2001:10::1", "2001:20::1",
		"2001:30::1", "2001:db8::1", "2002:7f00:1::1", "2620:4f:8000::1", "3fff::1",
		"5f00::1", "fc00::1", "fd00:ec2::254", "fe80::1", "fe80::1%eth0", "fec0::1", "ff02::1",
		"2606:4700:4700::1111%eth0",
		"168.63.129.16", "100.100.100.200", "192.0.0.192",
	}
	for _, input := range denied {
		t.Run(input, func(t *testing.T) {
			if allowedAddress(netip.MustParseAddr(input), egresstest.Policy{}) {
				t.Fatalf("allowed special-use address %s", input)
			}
		})
	}
	for _, input := range []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.0", "172.32.0.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !allowedAddress(netip.MustParseAddr(input), egresstest.Policy{}) {
			t.Errorf("denied public address %s", input)
		}
	}
	if allowedAddress(netip.Addr{}, egresstest.Policy{}) {
		t.Error("allowed invalid address")
	}
}

func TestHostParsing(t *testing.T) {
	for _, host := range []string{"0x7f.0.0.1", "0177.0.0.1", "127.1", "2130706433", "0x7f000001", "127.0.0.1.", "::ffff:127.0.0.1", "::ffff:8.8.8.8", "fe80::1%eth0"} {
		t.Run(host, func(t *testing.T) {
			d := dialer{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				t.Fatal("non-canonical IP reached DNS")
				return nil, nil
			}}
			if _, err := d.resolve(t.Context(), host); !errors.Is(err, ErrAddressDenied) {
				t.Fatalf("resolve(%q) = %v, want denied", host, err)
			}
		})
	}
}
