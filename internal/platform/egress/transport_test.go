package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	// Only the trust roots differ from production; the dialer and the rest of the transport are the real ones.
	trusted := &Client{transport: client.transport.Clone()}
	defer trusted.CloseIdleConnections()
	trusted.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	resp, err := trusted.Do(request(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, resp.Body)
	if got, want := <-identity, net.JoinHostPort(name, port)+"/"+name; got != want {
		t.Errorf("Host/SNI = %q, want %q", got, want)
	}
}

func TestResponseHeaderSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Padding", strings.Repeat("a", maxResponseHeaderBytes))
	}))
	defer server.Close()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	if err := requestFailure(t, client, request(t, server.URL)); !strings.Contains(err.Error(), "header") {
		t.Fatalf("oversized response header accepted: %v", err)
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
