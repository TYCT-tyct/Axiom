package suppliers

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// TestACredentialCallsThroughItsProxy: a credential naming a proxy calls through it, with the proxy's credentials, and
// a hangup there is the egress's; while the proxy is disabled its calls go through the fallback; a proxy whose password
// does not open leaves the supplier out of the snapshot rather than calling direct. Each receipt names the exit taken.
func TestACredentialCallsThroughItsProxy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	server := newSupplierServer(t)
	primary, fallback := newConnectProxy(t), newConnectProxy(t)
	admin, who := w.adminOf(), audit.System("test")
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		for _, p := range []EgressProxySpec{
			{ID: "backup", Scheme: "http", Host: "127.0.0.1", Port: fallback.port(), Set: EgressProxyChange{Name: "backup", Status: "active"}},
			{ID: "primary", Scheme: "http", Host: "127.0.0.1", Port: primary.port(), Set: EgressProxyChange{
				Name: "primary", FallbackID: "backup", Status: "active", Auth: &ProxyAuth{Username: "operator", Password: new(secret.New("s3cret"))},
			}},
		} {
			if _, err := admin.CreateEgressProxy(ctx, tx, who, p, "r"); err != nil {
				return err
			}
		}
		_, err := admin.SetCredentialEgress(ctx, tx, who, w.supplier, w.credential, "primary", "r")
		return err
	})
	w.pointAt(server.URL)
	w.sendOK("direct", "direct")
	w.reload()
	w.pointAt(server.URL)
	w.sendOK("through the proxy", "primary")
	if n, auth := primary.seen(); n != 1 || auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:s3cret")) {
		t.Fatalf("the proxy opened %d tunnels with %q", n, auth)
	}
	if _, f, _ := w.send(protocol.Chat, "hangup"); f == nil || f.Scope != adapter.ScopeEgress || f.Cause != (adapter.Transport{}) || f.Executed == adapter.NotExecuted {
		t.Fatalf("a hangup through the proxy: %+v, want the egress's, maybe run", f)
	}

	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := admin.UpdateEgressProxy(ctx, tx, who, "primary", EgressProxyChange{Name: "primary", FallbackID: "backup", Status: "disabled"}, "r")
		return err
	})
	w.reload()
	w.pointAt(server.URL)
	w.sendOK("through the fallback", "backup")
	if n, _ := fallback.seen(); n != 1 {
		t.Fatalf("the fallback opened %d tunnels, want 1", n)
	}

	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.egress_proxies SET status = 'active', ciphertext = 'not the sealed password' WHERE id = 'primary'")
		return err
	})
	w.reload()
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("a proxy whose password does not open: %v, want the supplier left out", err)
	}
}

// sendOK sends a call the supplier answers, and fails the test unless it came back answered through egress.
func (w *world) sendOK(what, egress string) {
	w.t.Helper()
	if status, f, receipt := w.send(protocol.Chat, "ok"); f != nil || status != http.StatusOK || receipt.Egress() != egress {
		w.t.Fatalf("%s: %d, %+v, through %q", what, status, f, receipt.Egress())
	}
}

// reload is the node's next Reload.
func (w *world) reload() {
	w.t.Helper()
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

// connectProxy is an HTTP proxy on loopback that opens CONNECT tunnels, counting them, with the Proxy-Authorization
// of the last.
type connectProxy struct {
	*httptest.Server
	mu      sync.Mutex
	tunnels int
	auth    string
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	p := &connectProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		up, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if r.Method != http.MethodConnect || err != nil {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		defer up.Close() //nolint:errcheck // a fake proxy has no one to tell
		p.mu.Lock()
		p.tunnels, p.auth = p.tunnels+1, r.Header.Get("Proxy-Authorization")
		p.mu.Unlock()
		conn, _, err := rw.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()                                     //nolint:errcheck // as above
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\n") //nolint:errcheck // the client sees a failure
		go func() { _, _ = io.Copy(up, conn) }()               //nolint:errcheck // either side ends the tunnel
		_, _ = io.Copy(conn, up)                               //nolint:errcheck // as above
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *connectProxy) port() int {
	return int(netip.MustParseAddrPort(p.Listener.Addr().String()).Port())
}

func (p *connectProxy) seen() (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tunnels, p.auth
}
