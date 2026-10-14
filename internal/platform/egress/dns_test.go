package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
)

// Through the real net.Resolver, the "ip" query returns both families and its answers pass the policy; the fake
// lookups in the other tests cannot show either.
func TestResolverReturnsBothFamilies(t *testing.T) {
	d := dialer{lookup: localResolver(t, []string{"8.8.8.8", "2606:4700::1111"}).LookupNetIP}
	ips, err := d.resolve(t.Context(), "upstream.test.")
	want := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700::1111")}
	if err != nil || !slices.Equal(slices.SortedFunc(slices.Values(ips), netip.Addr.Compare), want) {
		t.Fatalf("public DNS records: %v, %v", ips, err)
	}
}

func localResolver(t *testing.T, addresses []string) *net.Resolver {
	t.Helper()
	conn, err := new(net.ListenConfig).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { mustClose(t, conn); <-done })
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, from, err := conn.ReadFrom(buffer)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := conn.WriteTo(dnsAnswer(buffer[:n], addresses), from); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
}

// The local responder only needs the uncompressed question emitted by net.Resolver.
func dnsAnswer(query []byte, addresses []string) []byte {
	end := 12
	for query[end] != 0 {
		end += int(query[end]) + 1
	}
	end++
	qtype := query[end+1]
	answer := []byte{query[0], query[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}
	answer = append(answer, query[12:end+4]...)
	for _, address := range addresses {
		ip := netip.MustParseAddr(address)
		kind, size := byte(28), byte(16)
		if ip.Is4() {
			kind, size = 1, 4
		}
		if kind == qtype {
			answer[7]++
			answer = append(answer, 0xc0, 0x0c, 0, kind, 0, 1, 0, 0, 0, 0, 0, size)
			answer = append(answer, ip.AsSlice()...)
		}
	}
	return answer
}
