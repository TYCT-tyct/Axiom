package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"axiom/internal/platform/egresstest"
)

func TestTLSKeepsHostnameAndVerifiesCertificate(t *testing.T) {
	identity := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		identity <- r.Host + "/" + r.TLS.ServerName
	}))
	defer server.Close()
	name := server.Certificate().DNSNames[0]
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(egresstest.Loopback(t), func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host != name {
			t.Errorf("resolved %q, want %q", host, name)
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	defer client.CloseIdleConnections()
	target := "https://" + net.JoinHostPort(name, port)
	var untrusted x509.UnknownAuthorityError
	if err := requestFailure(t, client, request(t, target)); !errors.As(err, &untrusted) {
		t.Fatalf("expected certificate verification failure: %v", err)
	}
	trusted := &Client{transport: client.transport.Clone(), tracer: client.tracer}
	defer trusted.CloseIdleConnections()
	trust(trusted, server.Certificate())
	resp, err := trusted.Do(request(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body)
	if got, want := <-identity, net.JoinHostPort(name, port)+"/"+name; got != want {
		t.Errorf("Host/SNI = %q, want %q", got, want)
	}
}

// trust must run before the client's first request. Only the trust roots differ from production; the dialer and the
// rest of the transport, including ForceAttemptHTTP2, are the real ones.
func trust(client *Client, cert *x509.Certificate) {
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	client.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

// serve starts handler over plain HTTP/1.1 or, for major 2, over TLS negotiating HTTP/2 as production upstreams do.
func serve(t *testing.T, major int, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != major {
			t.Errorf("request used HTTP/%d, want HTTP/%d", r.ProtoMajor, major)
		}
		handler(w, r)
	}))
	client := NewForTesting(egresstest.Loopback(t))
	if major == 2 {
		server.EnableHTTP2 = true
		server.StartTLS()
		trust(client, server.Certificate())
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	t.Cleanup(client.CloseIdleConnections)
	return client, server.URL
}

func TestResponseHeaderSizeLimit(t *testing.T) {
	for _, major := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP/%d", major), func(t *testing.T) {
			// HTTP/2 counts 32 bytes per field on top of Go's room for ten such fields, so exceed the limit by more. Two
			// fields keep each one short enough for the HPACK decoder, so the list limit, not a string limit, refuses them.
			half := strings.Repeat("a", maxResponseHeaderBytes/2+1<<9)
			client, url := serve(t, major, func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["X-Padding"] = []string{half, half}
			})
			if err := requestFailure(t, client, request(t, url)); !strings.Contains(err.Error(), "header") {
				t.Fatalf("oversized response header accepted: %v", err)
			}
		})
	}
}

func TestTLSHandshakeTimeout(t *testing.T) {
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done, finished := make(chan struct{}), make(chan struct{})
	// Closing the listener first ends Accept even if the client never connected, so a failure cannot hang the test.
	defer func() { mustClose(t, listener); close(done); <-finished }()
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return // the client's failure is reported by the assertion below
		}
		if err != nil {
			t.Error(err)
			return
		}
		defer mustClose(t, conn)
		<-done
	}()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	client.transport.TLSHandshakeTimeout /= 1000
	req := request(t, "https://"+listener.Addr().String())
	err = requestFailure(t, client, req)
	assertPhaseTimeout(t, req.Context(), err)
}

func assertPhaseTimeout(t *testing.T, ctx context.Context, err error) {
	t.Helper()
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
		t.Fatalf("phase timeout = %v, request context = %v", err, ctx.Err())
	}
}

func TestIgnoresProxyEnvironment(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request used an environment proxy")
	}))
	defer proxy.Close()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	client := newClient(egresstest.Loopback(t), fixedLookup("127.0.0.1"))
	defer client.CloseIdleConnections()
	target := strings.Replace(server.URL, "127.0.0.1", "upstream.test", 1)
	resp, err := client.Do(request(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body)
}

func TestLoopbackIPv6(t *testing.T) {
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mustClose(t, server.Listener)
	server.Listener = listener
	server.Start()
	defer server.Close()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	resp, err := client.Do(request(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body)
}

// Go pools connections by host and port today. This guards a future change that reuses an HTTP/2 connection for
// another host its certificate also covers, which would skip validating that host's addresses.
func TestHTTP2ConnectionIsNotReusedForAnotherHost(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "a.example.com:") {
			t.Errorf("request for %s reached the server", r.Host)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS() // the test certificate covers example.com and *.example.com
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(egresstest.Loopback(t), func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "a.example.com" {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	})
	trust(client, server.Certificate())
	defer client.CloseIdleConnections()
	resp, err := client.Do(request(t, "https://"+net.JoinHostPort("a.example.com", port)))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body) // HTTP/2 pools the connection for further streams while this one is open
	if resp.ProtoMajor != 2 {
		t.Fatalf("negotiated HTTP/%d, want HTTP/2", resp.ProtoMajor)
	}
	err = requestFailure(t, client, request(t, "https://"+net.JoinHostPort("b.example.com", port)))
	if !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("host resolving to a private address was not refused: %v", err)
	}
}
