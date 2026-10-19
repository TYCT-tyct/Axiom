package clientip

import (
	"net/http"
	"net/netip"
	"testing"
)

// TestAddr: only the connection's own address is believed unless it is a trusted proxy, and then X-Forwarded-For is
// read from its right past the proxies to the first address that is not one; without a trusted proxy the header is
// ignored whoever sends it, and a header that cannot be read is not believed.
func TestAddr(t *testing.T) {
	t.Parallel()
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.9/32"),
		netip.MustParsePrefix("2001:db8:ffff::/48"),
	}
	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		forward []string
		want    string
	}{
		{"no proxy configured", nil, "203.0.113.5:1", []string{"198.51.100.1"}, "203.0.113.5"},
		{"no proxy configured, a private peer", nil, "10.1.1.1:1", []string{"198.51.100.1"}, "10.1.1.1"},
		{"an untrusted peer spoofing the header", trusted, "203.0.113.5:1", []string{"198.51.100.1"}, "203.0.113.5"},
		{"a trusted proxy forwarding", trusted, "10.1.1.1:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"the client's claims are left of the proxy's", trusted, "10.1.1.1:1", []string{"6.6.6.6, 198.51.100.1"}, "198.51.100.1"},
		{"a chain of proxies", trusted, "10.1.1.1:1", []string{"198.51.100.1, 10.2.2.2", "192.0.2.9"}, "198.51.100.1"},
		{"a chain of nothing but proxies", trusted, "10.1.1.1:1", []string{"10.2.2.2, 10.3.3.3"}, "10.1.1.1"},
		{"no header", trusted, "10.1.1.1:1", nil, "10.1.1.1"},
		{"spaces around the hops", trusted, "10.1.1.1:1", []string{" 198.51.100.1 , 10.2.2.2 "}, "198.51.100.1"},
		{"a header that cannot be read", trusted, "10.1.1.1:1", []string{"198.51.100.1, garbage"}, "10.1.1.1"},
		{"a header of names", trusted, "10.1.1.1:1", []string{"198.51.100.1, proxy.example"}, "10.1.1.1"},
		{"IPv6 behind an IPv4 proxy", trusted, "10.1.1.1:1", []string{"2001:db8::1"}, "2001:db8::1"},
		{"an IPv6 proxy", trusted, "[2001:db8:ffff::1]:1", []string{"2001:db8::1"}, "2001:db8::1"},
		{"an IPv6 chain of proxies", trusted, "[2001:db8:ffff::1]:1", []string{"2001:db8::1, 2001:db8:ffff::2"}, "2001:db8::1"},
		{"a zone on a hop", trusted, "10.1.1.1:1", []string{"2001:db8::1%a"}, "2001:db8::1"},
		{"a zone on a trusted hop", trusted, "10.1.1.1:1", []string{"198.51.100.1, 2001:db8:ffff::2%b"}, "198.51.100.1"},
		{"v4 in v6 clothes", trusted, "[::ffff:10.1.1.1]:1", []string{"::ffff:198.51.100.1"}, "198.51.100.1"},
		{"an address that is no address", trusted, "pipe", []string{"198.51.100.1"}, ""},
		{"an address without a port", trusted, "10.1.1.1", []string{"198.51.100.1"}, ""},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{"X-Forwarded-For": c.forward}}
		got := ""
		if ip := Addr(r, c.trusted); ip.IsValid() {
			got = ip.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
