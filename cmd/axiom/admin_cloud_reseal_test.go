package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pgtest"
)

// TestResealedCloudCredentialKeepsItsSettingsAndTakesTheLinesExit: a credential sealed again in the row made for it is
// stored as a new row would be: with its settings (Bedrock's form, without which a node does not load it) and the exit
// its check went through, the one the line's credentials share. So the same key replaced into its own row keeps its
// form, and a key that returns to its row after the key that replaced it was given a proxy calls through that proxy.
func TestResealedCloudCredentialKeepsItsSettingsAndTakesTheLinesExit(t *testing.T) {
	t.Parallel()
	f, _ := newCloudFixture(t)
	var made adminapi.SupplierConnected
	decode(t, f.change(f.s, http.MethodPost, "/supplier-types/bedrock/suppliers", bedrockBody("us-east-1"), newKey()), &made)
	id := made.Supplier.Id
	path := "/suppliers/" + id + "/cloud-credential"
	serving := `SELECT string_agg(hint || ' ' || coalesce(settings ->> 'form', 'none') || ' ' || coalesce(egress_id, 'direct'), ',')
		FROM suppliers.credentials WHERE status = 'active' AND supplier_id = '` + id + `'`
	first := `{"fields":{"access_key_id":"` + cloudKeyID + `","secret_access_key":"fake/secret+key"},"reason":"the first key"}`
	expectAdmin(t, "the same key", f.change(f.s, http.MethodPut, path, first, newKey()), http.StatusOK, "")
	if got := f.text(serving); got != "0001 access_key direct" {
		t.Errorf("the same key sealed again is %q, want its form kept", got)
	}
	second := `{"fields":{"access_key_id":"AKIAFAKEHARBOR0002","secret_access_key":"fake/second"},"reason":"a new key"}`
	expectAdmin(t, "the second key", f.change(f.s, http.MethodPut, path, second, newKey()), http.StatusOK, "")
	proxy := newTunnelProxy(t)
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"INSERT INTO suppliers.egress_proxies (id, name, scheme, host, port) VALUES ('tunnel', 'tunnel', 'http', '127.0.0.1', "+strconv.Itoa(proxy.port(t))+")",
		"UPDATE suppliers.credentials SET egress_id = 'tunnel' WHERE status = 'active' AND supplier_id = '"+id+"'"); err != nil {
		t.Fatal(err)
	}
	expectAdmin(t, "the first key back", f.change(f.s, http.MethodPut, path, first, newKey()), http.StatusOK, "")
	if got := f.text(serving); got != "0001 access_key tunnel" || proxy.tunnels.Load() == 0 {
		t.Errorf("the first key back is %q, checked through %d tunnels; want it on the exit its check took", got, proxy.tunnels.Load())
	}
}

// TestCloudReplaceRefusesALineOfMixedExits: a line whose credentials leave through different exits has no exit a new
// credential could take without guessing, and storing it direct would send calls from an address the line was kept
// off, so the replacement is refused (409) before anything is checked, and the credentials stay as they were.
func TestCloudReplaceRefusesALineOfMixedExits(t *testing.T) {
	t.Parallel()
	f, _ := newCloudFixture(t)
	var made adminapi.SupplierConnected
	decode(t, f.change(f.s, http.MethodPost, "/supplier-types/bedrock/suppliers", bedrockBody("us-east-1", "us-east-2"), newKey()), &made)
	id := made.Supplier.Id
	if err := pgtest.Run(t.Context(), f.database.Superuser,
		"INSERT INTO suppliers.egress_proxies (id, name, scheme, host, port) VALUES ('tunnel', 'tunnel', 'http', '127.0.0.1', 9)",
		"UPDATE suppliers.credentials SET egress_id = 'tunnel' WHERE region = 'us-east-1' AND supplier_id = '"+id+"'"); err != nil {
		t.Fatal(err)
	}
	serving := `SELECT string_agg(region || ' ' || hint || ' ' || coalesce(egress_id, 'direct'), ',' ORDER BY region)
		FROM suppliers.credentials WHERE status = 'active' AND supplier_id = '` + id + `'`
	second := `{"fields":{"access_key_id":"AKIAFAKEHARBOR0002","secret_access_key":"fake/second"},"reason":"a new key"}`
	expectAdmin(t, "a line of mixed exits", f.change(f.s, http.MethodPut, "/suppliers/"+id+"/cloud-credential", second, newKey()), http.StatusConflict, codeConflict)
	if got := f.text(serving); got != "us-east-1 0001 tunnel,us-east-2 0001 direct" {
		t.Errorf("after the refusal the credentials are %q", got)
	}
}

// tunnelProxy is an HTTP proxy on loopback that opens CONNECT tunnels to the address asked for, counting them.
type tunnelProxy struct {
	*httptest.Server
	tunnels atomic.Int32
}

func newTunnelProxy(t *testing.T) *tunnelProxy {
	t.Helper()
	p := &tunnelProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		up, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if r.Method != http.MethodConnect || err != nil {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		defer up.Close() //nolint:errcheck // a fake proxy has no one to tell
		p.tunnels.Add(1)
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

func (p *tunnelProxy) port(t *testing.T) int {
	t.Helper()
	at, err := netip.ParseAddrPort(p.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return int(at.Port())
}
