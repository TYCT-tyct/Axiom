package egress

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/egresstest"
)

// TestViaReachesTheTargetThroughTheProxy: through an HTTP CONNECT proxy and a SOCKS5 one, a request reaches its target
// with the proxy's credentials, and a target the policy refuses is refused before the proxy hears of it.
func TestViaReachesTheTargetThroughTheProxy(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached") //nolint:errcheck // the client sees a short body
	}))
	t.Cleanup(target.Close)
	for scheme, handshake := range map[string]func(net.Conn) asked{"http": connectHandshake, "socks5": socksHandshake} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			fake := listenProxy(t, handshake)
			p := Proxy{Scheme: scheme, Host: "127.0.0.1", Port: int(fake.port), Username: "operator", Password: "s3cret"}
			want := []asked{{target: target.Listener.Addr().String(), auth: "operator:s3cret"}}
			if body := getThrough(t, egresstest.Loopback(t), p, target.URL); body != "reached" || !slices.Equal(fake.requests(), want) {
				t.Fatalf("the body is %q and the proxy was asked %v, want %v", body, fake.requests(), want)
			}

			// The proxy's own loopback address is the operator's choice; a loopback target is not.
			public, err := NewForTesting(egresstest.Policy{}).Via(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := requestFailure(t, public, request(t, target.URL)); !errors.Is(err, ErrAddressDenied) {
				t.Fatalf("a loopback target through the proxy: %v, want ErrAddressDenied", err)
			}
			if got := fake.requests(); len(got) != 1 {
				t.Fatalf("the proxy was asked %v, want only the first request", got)
			}
		})
	}
	if _, err := NewDirect().Via(Proxy{Scheme: "ftp", Host: "proxy.example", Port: 21}); !errors.Is(err, ErrInvalidProxy) {
		t.Fatalf("an ftp proxy: %v", err)
	}
}

// getThrough is the body of a GET of target through p by a client of policy.
func getThrough(t *testing.T, policy egresstest.Policy, p Proxy, target string) string {
	t.Helper()
	through, err := NewForTesting(policy).Via(p)
	if err != nil {
		t.Fatal(err)
	}
	defer through.CloseIdleConnections()
	resp, err := through.Do(request(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// fakeProxy is a proxy on loopback that records what each connection asked for before it dials the target, so a test
// that got an answer through it finds the request recorded.
type fakeProxy struct {
	port  uint16
	mu    sync.Mutex
	asked []asked
}

// asked is one connection's request: the target, and the credentials it came with as user:password.
type asked struct{ target, auth string }

func (f *fakeProxy) requests() []asked {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// listenProxy serves each connection with handshake, which reads the proxy's request and answers it, and then copies
// between the client and the target it named.
func listenProxy(t *testing.T, handshake func(net.Conn) asked) *fakeProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) //nolint:errcheck // the test is over
	f := &fakeProxy{port: netip.MustParseAddrPort(ln.Addr().String()).Port()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck // a fake proxy has no one to tell
				a := handshake(conn)
				f.mu.Lock()
				f.asked = append(f.asked, a)
				f.mu.Unlock()
				up, err := (&net.Dialer{Timeout: time.Second}).Dial("tcp", a.target)
				if err != nil {
					return
				}
				defer up.Close()                         //nolint:errcheck // as above
				go func() { _, _ = io.Copy(up, conn) }() //nolint:errcheck // either side ends the tunnel
				_, _ = io.Copy(conn, up)                 //nolint:errcheck // as above
			}()
		}
	}()
	return f
}

// connectHandshake answers an HTTP CONNECT; the credentials are its Basic Proxy-Authorization.
func connectHandshake(conn net.Conn) asked {
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil || req.Method != http.MethodConnect {
		return asked{}
	}
	a := asked{target: req.Host}
	basic := &http.Request{Header: http.Header{"Authorization": req.Header.Values("Proxy-Authorization")}}
	if user, pass, ok := basic.BasicAuth(); ok {
		a.auth = user + ":" + pass
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n") //nolint:errcheck // the client sees a failure
	return a
}

// socksHandshake answers a SOCKS5 CONNECT that authenticates with a username and password (RFC 1928, RFC 1929). A
// short read leaves zeros, which name no target.
func socksHandshake(conn net.Conn) asked {
	r := bufio.NewReader(conn)
	next := func(n int) []byte {
		b := make([]byte, n)
		_, _ = io.ReadFull(r, b) //nolint:errcheck // see above
		return b
	}
	next(int(next(2)[1]))           // version and the methods offered
	_, _ = conn.Write([]byte{5, 2}) //nolint:errcheck // username and password; the client sees a failure
	next(1)                         // the subnegotiation's version
	user := string(next(int(next(1)[0])))
	pass := string(next(int(next(1)[0])))
	_, _ = conn.Write([]byte{1, 0}) //nolint:errcheck // accepted
	var host string
	switch next(4)[3] { // version, CONNECT, reserved, address type
	case 1:
		host = netip.AddrFrom4([4]byte(next(4))).String()
	case 3:
		host = string(next(int(next(1)[0])))
	case 4:
		host = netip.AddrFrom16([16]byte(next(16))).String()
	}
	port := binary.BigEndian.Uint16(next(2))
	_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}) //nolint:errcheck // succeeded
	return asked{target: net.JoinHostPort(host, strconv.Itoa(int(port))), auth: user + ":" + pass}
}
