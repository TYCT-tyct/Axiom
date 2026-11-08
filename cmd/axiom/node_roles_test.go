package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/nodetls"
)

func TestParseRoles(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		value string
		want  string
		ok    bool
	}{
		{"core", "core", true},
		{"ingress", "ingress", true},
		{"ops", "ops", true},
		{"core,ops", "core,ops", true},
		{" ingress , ops ", "ingress,ops", true},
		{"", "", false},
		{"edge", "", false},
		{"core,core", "", false},
		{"core,", "", false},
	} {
		roles, err := parseRoles(c.value)
		if c.ok {
			if err != nil || roles.String() != c.want {
				t.Errorf("parseRoles(%q) = %v, %v; want %q", c.value, roles, err, c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseRoles(%q) accepted the value", c.value)
		}
	}
}

// coreEnviron is a core node's settings without touching a database or a key file: readSettings only checks their
// presence and shapes.
func coreEnviron(extra ...string) []string {
	return append([]string{
		"AXIOM_DATABASE_URL=postgres://user:password@127.0.0.1:1/axiom",
		"AXIOM_MASTER_KEY_FILE=/keys/master", "AXIOM_KEY_HASH_FILE=/keys/hash", "AXIOM_IDENTITY_KEY_FILE=/keys/identity",
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
	}, extra...)
}

// TestRoleConfigurationRefusesContradictions holds a node to the role it was given: the admin settings belong to the
// core, an ingress needs a core and certificates, and an ops node opens no public listener.
func TestRoleConfigurationRefusesContradictions(t *testing.T) {
	t.Parallel()
	tlsDir := t.TempDir()
	ingress := func(extra ...string) []string {
		return append([]string{
			"AXIOM_NODE_ROLES=ingress", "AXIOM_CORE_URL=https://core.internal:8083", "AXIOM_NODE_TLS_DIR=" + tlsDir,
			"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0",
		}, extra...)
	}
	bridge := func(extra ...string) []string {
		environ := []string{
			"AXIOM_NODE_ROLES=ops", "AXIOM_MCP_ADDR=127.0.0.1:0", "AXIOM_OPERATIONS_URL=https://core.internal:8084",
			"AXIOM_OPERATIONS_TOKEN_FILE=/keys/operations-token", "AXIOM_MCP_TOKEN_FILE=/run/mcp/token",
		}
		for _, e := range extra { // a later value of the same variable replaces the default here
			name, _, _ := strings.Cut(e, "=")
			environ = slices.DeleteFunc(environ, func(d string) bool { return strings.HasPrefix(d, name+"=") })
		}
		return append(environ, extra...)
	}
	cases := []struct {
		name    string
		environ []string
		want    string
	}{
		{"an unknown role", []string{"AXIOM_NODE_ROLES=edge"}, "AXIOM_NODE_ROLES: want core, ingress or ops"},
		{"a role twice", []string{"AXIOM_NODE_ROLES=core,core"}, "core appears twice"},
		{"core and ingress together", []string{"AXIOM_NODE_ROLES=core,ingress"}, "core and ingress cannot be on one node"},
		{"an ingress without a core", []string{"AXIOM_NODE_ROLES=ingress"}, "AXIOM_CORE_URL: required"},
		{"an ingress without certificates", []string{
			"AXIOM_NODE_ROLES=ingress", "AXIOM_CORE_URL=https://core.internal:8083", "AXIOM_PUBLIC_ADDR=127.0.0.1:0",
		}, "AXIOM_NODE_TLS_DIR: required"},
		{"an admin listener on an ingress", ingress("AXIOM_ADMIN_ADDR=127.0.0.1:0"), "AXIOM_ADMIN_ADDR: not used"},
		{"admin sources on an ingress", ingress("AXIOM_ADMIN_SOURCES=10.0.0.0/8"), "AXIOM_ADMIN_SOURCES: not used"},
		{"a node listener on an ingress", ingress("AXIOM_NODE_ADDR=127.0.0.1:0"), "AXIOM_NODE_ADDR: not used"},
		{"a public listener on an ops node", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_PUBLIC_ADDR=127.0.0.1:0"}, "AXIOM_PUBLIC_ADDR: not used"},
		{"an ingress given the database", ingress("AXIOM_DATABASE_URL=postgres://user:password@127.0.0.1:1/axiom"), "AXIOM_DATABASE_URL: unknown variable"},
		{"an ingress given a key file", ingress("AXIOM_MASTER_KEY_FILE=/keys/master"), "AXIOM_MASTER_KEY_FILE: unknown variable"},
		{"an ingress given shared frontend assets", ingress("AXIOM_WEB_ASSETS_DIR=/srv/web-assets"), "AXIOM_WEB_ASSETS_DIR: unknown variable"},
		{"a core URL on an ops node", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_CORE_URL=https://core.internal"}, "AXIOM_CORE_URL: not used"},
		{"a core that forwards", coreEnviron("AXIOM_CORE_URL=https://core.internal"), "AXIOM_CORE_URL: only an ingress forwards to a core"},
		{"an admin listener without sources on every address", coreEnviron("AXIOM_ADMIN_ADDR=:8082"), "AXIOM_ADMIN_SOURCES: required"},
		{"certificates without the listener", coreEnviron("AXIOM_NODE_TLS_DIR=" + tlsDir), "AXIOM_NODE_TLS_DIR: set without AXIOM_NODE_ADDR"},
		{"an operations listener on an ops node", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_OPERATIONS_ADDR=127.0.0.1:0"}, "AXIOM_OPERATIONS_ADDR: not used"},
		{"an operations listener without sources on every address", coreEnviron("AXIOM_OPERATIONS_ADDR=:8084",
			"AXIOM_NODE_TLS_DIR="+filepath.Join(t.TempDir(), "absent")), "AXIOM_OPERATIONS_SOURCES: required"},
		{"operations sources without the listener", coreEnviron("AXIOM_OPERATIONS_SOURCES=10.0.0.0/8"), "AXIOM_OPERATIONS_SOURCES: set without AXIOM_OPERATIONS_ADDR"},
		{"an operations listener without certificates", coreEnviron("AXIOM_OPERATIONS_ADDR=127.0.0.1:0",
			"AXIOM_NODE_TLS_DIR="+filepath.Join(t.TempDir(), "absent")), "node.crt: file not found"},
		{"a node listener without certificates", coreEnviron("AXIOM_NODE_ADDR=127.0.0.1:0",
			"AXIOM_NODE_TLS_DIR="+filepath.Join(t.TempDir(), "absent")), "node.crt: file not found"},
		{"an MCP bridge on a core", coreEnviron("AXIOM_MCP_ADDR=127.0.0.1:0"), "AXIOM_MCP_ADDR: unknown variable"},
		{"an MCP bridge open beyond the machine", bridge("AXIOM_MCP_ADDR=0.0.0.0:8090"), "AXIOM_MCP_ADDR: want a loopback address"},
		{"an MCP bridge without its credential", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_MCP_ADDR=127.0.0.1:0"}, "AXIOM_OPERATIONS_TOKEN_FILE: required"},
		{"an MCP bridge without certificates", bridge("AXIOM_NODE_TLS_DIR=" + filepath.Join(t.TempDir(), "absent")), "node.crt: file not found"},
		{"an operations URL without the bridge", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_OPERATIONS_URL=https://core.internal:8084"}, "AXIOM_OPERATIONS_URL: set without AXIOM_MCP_ADDR"},
		{"certificates on an ops node without the bridge", []string{"AXIOM_NODE_ROLES=ops", "AXIOM_NODE_TLS_DIR=" + tlsDir}, "AXIOM_NODE_TLS_DIR: not used"},
		{"an operations URL with a path", bridge("AXIOM_OPERATIONS_URL=https://core.internal:8084/api"), "AXIOM_OPERATIONS_URL: want an https URL"},
		{"half an audit replica", coreEnviron("AXIOM_AUDIT_REPLICA_ENDPOINT=https://s3.example.com"), "AXIOM_AUDIT_REPLICA_BUCKET: required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := readSettings(c.environ)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("readSettings = %v, want %q", err, c.want)
			}
		})
	}
	settings, err := readSettings(coreEnviron("AXIOM_ADMIN_ADDR=127.0.0.1:0"))
	if err != nil {
		t.Fatalf("a core node with a loopback admin listener was refused: %v", err)
	}
	if len(settings.adminSources) != 2 {
		t.Fatalf("a loopback admin listener got sources %v, want both loopback networks", settings.adminSources)
	}
	settings, err = readSettings(coreEnviron("AXIOM_ADMIN_ADDR=:8082", "AXIOM_ADMIN_SOURCES=10.0.0.0/8,2001:db8::/32"))
	if err != nil {
		t.Fatalf("a core node with explicit admin sources was refused: %v", err)
	}
	if len(settings.adminSources) != 2 {
		t.Fatalf("explicit admin sources are %v, want both networks", settings.adminSources)
	}
	settings, err = readSettings(coreEnviron("AXIOM_WEB_ASSETS_DIR=/srv/web-assets"))
	if err != nil {
		t.Fatalf("a core node with shared frontend assets was refused: %v", err)
	}
	if settings.webAssetsDir != "/srv/web-assets" {
		t.Fatalf("shared frontend assets are %q", settings.webAssetsDir)
	}
	settings, err = readSettings(coreEnviron("AXIOM_AUDIT_REPLICA_ENDPOINT=https://s3.example.com", "AXIOM_AUDIT_REPLICA_BUCKET=audit",
		"AXIOM_AUDIT_REPLICA_ACCESS_KEY_ID=A", "AXIOM_AUDIT_REPLICA_SECRET_ACCESS_KEY=S"))
	if err != nil || settings.auditReplica.Bucket != "audit" || settings.objectstore.Configured() {
		t.Fatalf("an audit replica reads as %+v, the archive as %+v: %v", settings.auditReplica, settings.objectstore, err)
	}
}

// TestServeOpsNodeOpensOnlyProbes: an ops node holds no database and serves nothing but its probes.
func TestServeOpsNodeOpensOnlyProbes(t *testing.T) {
	t.Parallel()
	environ := []string{
		"AXIOM_NODE_ROLES=ops", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveForTest(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	addrs := servingAddrs(t, logs, done)
	if addrs.Public != "" || addrs.Admin != "" || addrs.Node != "" || addrs.Ops == "" {
		t.Fatalf("an ops node opened public=%q admin=%q node=%q probes=%q", addrs.Public, addrs.Admin, addrs.Node, addrs.Ops)
	}
	if resp, err := get(t, "http://"+addrs.Ops+"/readyz"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz on an ops node: %v %v, want 200", resp.Response, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
}

// TestServeIngressForwardsOverMutualTLS: an ingress without a database forwards its public listener to the core's node
// listener over mutual TLS, streams the core's answer, and reports itself ready only while the core is.
func TestServeIngressForwardsOverMutualTLS(t *testing.T) {
	t.Parallel()
	coreDir, ingressDir := testNodeDirs(t)
	coreTLS, err := nodetls.Server(coreDir)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := make(chan struct{})
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	core := httptest.NewUnstartedServer(coreHandler(t, release))
	core.TLS = coreTLS
	core.EnableHTTP2 = true
	core.StartTLS()
	defer core.Close()
	defer unblock() // after Close in the defer order, so a failed test releases the held stream before Close waits for it

	environ := []string{
		"AXIOM_NODE_ROLES=ingress", "AXIOM_CORE_URL=" + core.URL, "AXIOM_NODE_TLS_DIR=" + ingressDir,
		"AXIOM_PUBLIC_ADDR=127.0.0.1:0", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s",
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveForTest(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	addrs := servingAddrs(t, logs, done)
	if addrs.Admin != "" || addrs.Node != "" {
		t.Fatalf("an ingress opened admin=%q node=%q", addrs.Admin, addrs.Node)
	}
	waitFor(t, func() bool {
		resp, err := get(t, "http://"+addrs.Ops+"/readyz")
		return err == nil && resp.StatusCode == http.StatusOK
	})
	resp, err := get(t, "http://"+addrs.Public+"/hello")
	if err != nil || resp.StatusCode != http.StatusOK || string(resp.body) != "core:ok" ||
		resp.Header.Get("X-Served-By") != "core" {
		t.Fatalf("GET /hello through the ingress: %v %v %q", resp.Response, err, resp.body)
	}
	checkIngressStreams(t, "http://"+addrs.Public+"/stream", unblock)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
}

// TestRestrictSources: the admin listener answers only the configured networks, and believes a forwarded address only
// from a trusted proxy.
func TestRestrictSources(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	sources := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")}
	handler := restrictSources(next, sources, trusted)
	for _, c := range []struct {
		name, remote, forwarded string
		want                    int
	}{
		{"the client's own address is allowed", "10.1.2.3:1234", "", http.StatusNoContent},
		{"the client's own address is refused", "203.0.113.9:1234", "", http.StatusForbidden},
		{"a trusted proxy's client is allowed", "192.0.2.1:1234", "10.1.2.3", http.StatusNoContent},
		{"a trusted proxy's client is refused", "192.0.2.1:1234", "203.0.113.9", http.StatusForbidden},
		{"a peer the list does not trust cannot speak for a client", "203.0.113.9:1234", "10.1.2.3", http.StatusForbidden},
		{"an address that cannot be read is refused", "", "", http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://admin.example/api/admin/v1/version", nil)
			r.RemoteAddr = c.remote
			if c.forwarded != "" {
				r.Header.Set("X-Forwarded-For", c.forwarded)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d", w.Code, c.want)
			}
		})
	}
}

// checkIngressStreams reads an event stream through the ingress and requires the first event before the core sends the
// second: a proxy that buffers would deliver both at once, which delays every token of a generation.
func checkIngressStreams(t *testing.T, url string, unblock func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // the body is closed below; t.Fatal's Goexit hides that from the analyzer
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // the test only reads the stream
	events := make(chan string, 4)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data:") {
				events <- line
			}
		}
	}()
	readIngressEvent(t, ctx, events, "data: one", "the first event did not arrive while the core held the second")
	unblock()
	readIngressEvent(t, ctx, events, "data: two", "the second event did not arrive")
}

// readIngressEvent requires the next event to be want before ctx ends.
func readIngressEvent(t *testing.T, ctx context.Context, events <-chan string, want, missing string) {
	t.Helper()
	select {
	case line, ok := <-events:
		if !ok || line != want {
			t.Fatalf("the event was %q, want %q", line, want)
		}
	case <-ctx.Done():
		t.Fatal(missing)
	}
}

// coreHandler is the fake core: it refuses a peer without a certificate, answers the readiness probe, holds a stream
// between two events, and names itself on other responses.
func coreHandler(t *testing.T, release <-chan struct{}) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("the core accepted a peer without a certificate")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(http.StatusOK)
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if _, err := io.WriteString(w, "data: one\n\n"); err != nil {
				t.Error(err)
				return
			}
			w.(http.Flusher).Flush()
			<-release
			if _, err := io.WriteString(w, "data: two\n\n"); err != nil {
				t.Error(err)
			}
		default:
			w.Header().Set("X-Served-By", "core")
			if _, err := io.WriteString(w, "core:ok"); err != nil {
				t.Error(err)
			}
		}
	})
}

// testNodeDirs makes a core server node and an ingress client node signed by one test authority.
func testNodeDirs(t *testing.T) (coreDir, ingressDir string) {
	t.Helper()
	ca := newTestCA(t)
	coreCert, coreKey := ca.issue(t, []string{"core.internal"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	ingressCert, ingressKey := ca.issue(t, []string{"ingress.internal"}, nil, x509.ExtKeyUsageClientAuth)
	coreDir = writeTestNode(t, filepath.Join(t.TempDir(), "core"), coreCert, coreKey, ca.pem)
	ingressDir = writeTestNode(t, filepath.Join(t.TempDir(), "ingress"), ingressCert, ingressKey, ca.pem)
	return coreDir, ingressDir
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test node authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pemBlock(t, "CERTIFICATE", der)}
}

func (a *testCA) issue(t *testing.T, dns []string, ips []net.IP, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "test node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     dns,
		IPAddresses:  ips,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemBlock(t, "CERTIFICATE", der), pemBlock(t, "EC PRIVATE KEY", keyDER)
}

func pemBlock(t *testing.T, kind string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func writeTestNode(t *testing.T, dir string, certPEM, keyPEM, caPEM []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{nodetls.CertFile: certPEM, nodetls.KeyFile: keyPEM, nodetls.CAFile: caPEM} {
		mode := os.FileMode(0o600)
		if name != nodetls.KeyFile {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
