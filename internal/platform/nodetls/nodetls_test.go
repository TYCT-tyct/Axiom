package nodetls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// authority is a test certificate authority; issue makes one leaf for a node.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
	pool *x509.CertPool
}

func newAuthority(t *testing.T) *authority {
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
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &authority{cert: cert, key: key, pem: pemBlock("CERTIFICATE", der), pool: pool}
}

// intermediate makes a CA below a that names only the given purposes, so a leaf's chain can conflict with the leaf
// itself.
func (a *authority) intermediate(t *testing.T, usage ...x509.ExtKeyUsage) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test intermediate authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &authority{cert: cert, key: key, pem: pemBlock("CERTIFICATE", der)}
}

// issue signs a leaf valid for the given names and purposes.
func (a *authority) issue(t *testing.T, dns []string, ips []net.IP, usage ...x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
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
		ExtKeyUsage:  usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemBlock("CERTIFICATE", der), pemBlock("EC PRIVATE KEY", keyDER)
}

func pemBlock(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

// writeNode puts one node's files in a directory with the modes the loader expects.
func writeNode(t *testing.T, dir string, certPEM, keyPEM, caPEM []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // this is a directory, not a file; testing.TempDir is 0775 under a 0002 umask
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{CertFile: certPEM, KeyFile: keyPEM, CAFile: caPEM} {
		mode := os.FileMode(0o600)
		if name != KeyFile {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// nodeDirs makes a server and a client node under one authority.
func nodeDirs(t *testing.T) (serverDir, clientDir string, ca *authority) {
	t.Helper()
	ca = newAuthority(t)
	serverCert, serverKey := ca.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := ca.issue(t, []string{"ingress.test"}, nil, x509.ExtKeyUsageClientAuth)
	serverDir = writeNode(t, filepath.Join(t.TempDir(), "server"), serverCert, serverKey, ca.pem)
	clientDir = writeNode(t, filepath.Join(t.TempDir(), "client"), clientCert, clientKey, ca.pem)
	return serverDir, clientDir, ca
}

func TestMutualTLSRequiresACertificateFromBothSides(t *testing.T) {
	t.Parallel()
	serverDir, clientDir, _ := nodeDirs(t)
	serverConfig, err := Server(serverDir)
	if err != nil {
		t.Fatal(err)
	}
	clientConfig, err := Client(clientDir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, serverConfig, clientConfig); err != nil {
		t.Fatalf("two configured nodes could not talk: %v", err)
	}
	// The client trusts the authority and the server name but presents no certificate: the server must refuse it.
	if err := handshake(t, serverConfig, &tls.Config{
		RootCAs: mustPool(t, clientConfig.RootCAs), ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13,
	}); err == nil {
		t.Fatal("the server accepted a peer without a certificate")
	}
}

func TestClientVerifiesTheServersName(t *testing.T) {
	t.Parallel()
	serverDir, clientDir, _ := nodeDirs(t)
	serverConfig, err := Server(serverDir)
	if err != nil {
		t.Fatal(err)
	}
	clientConfig, err := Client(clientDir, "wrong.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, serverConfig, clientConfig); err == nil {
		t.Fatal("the client accepted a certificate for another name")
	}
}

// mustPool returns the pool unchanged; the helper only documents that the test reuses the client's trust.
func mustPool(t *testing.T, pool *x509.CertPool) *x509.CertPool {
	t.Helper()
	if pool == nil {
		t.Fatal("the client has no trust pool")
	}
	return pool
}

// handshake runs one TLS handshake over an in-memory connection and returns the first failure of either side.
func handshake(t *testing.T, serverConfig, clientConfig *tls.Config) error {
	t.Helper()
	// A real socket with a deadline: an in-memory pipe lets a failed handshake wait forever for a flight the other
	// side will not read, and a test that hangs says nothing about the code.
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}()
	done := serveHandshake(t, listener, serverConfig)
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", listener.Addr().String())
	if err != nil {
		return err
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	clientErr := tls.Client(conn, clientConfig).HandshakeContext(t.Context())
	serverErr := <-done
	if clientErr != nil {
		return clientErr
	}
	return serverErr
}

// serveHandshake accepts one connection and handshakes it, reporting the outcome on the channel. The close happens
// before the send, so a test that receives the outcome is still running when a close failure is reported.
func serveHandshake(t *testing.T, listener net.Listener, config *tls.Config) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			if closeErr := conn.Close(); closeErr != nil {
				t.Error(closeErr)
			}
			done <- err
			return
		}
		err = tls.Server(conn, config).HandshakeContext(t.Context())
		if closeErr := conn.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		done <- err
	}()
	return done
}

// TestCertificatePurposeCoversTheWholeChain: verification at startup asks for the purpose the handshake will ask for,
// so a CA that allows only serverAuth cannot hide a client certificate that the core will refuse, and an explicit
// ExtKeyUsageAny is accepted for either side.
func TestCertificatePurposeCoversTheWholeChain(t *testing.T) {
	t.Parallel()
	root := newAuthority(t)
	serverOnly := root.intermediate(t, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := serverOnly.issue(t, []string{"node.test"}, nil, x509.ExtKeyUsageClientAuth)
	clientChain := append(append([]byte{}, clientCert...), serverOnly.pem...)
	clientDir := writeNode(t, t.TempDir(), clientChain, clientKey, root.pem)
	if _, err := Client(clientDir, "node.test"); err == nil || !strings.Contains(err.Error(), "not a client certificate") {
		t.Fatalf("Client accepted a client leaf under a server-only CA: %v", err)
	}

	clientOnly := root.intermediate(t, x509.ExtKeyUsageClientAuth)
	serverCert, serverKey := clientOnly.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	serverChain := append(append([]byte{}, serverCert...), clientOnly.pem...)
	serverDir := writeNode(t, t.TempDir(), serverChain, serverKey, root.pem)
	if _, err := Server(serverDir); err == nil || !strings.Contains(err.Error(), "not a server certificate") {
		t.Fatalf("Server accepted a server leaf under a client-only CA: %v", err)
	}

	anyCert, anyKey := root.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageAny)
	anyDir := writeNode(t, t.TempDir(), anyCert, anyKey, root.pem)
	if _, err := Server(anyDir); err != nil {
		t.Fatalf("Server refused a certificate that is good for any purpose: %v", err)
	}
	if _, err := Client(anyDir, "node.test"); err != nil {
		t.Fatalf("Client refused a certificate that is good for any purpose: %v", err)
	}
}

func TestRefusesUnusableMaterial(t *testing.T) {
	t.Parallel()
	serverDir, clientDir, ca := nodeDirs(t)
	validServerCert, validServerKey := ca.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	_, otherKey := ca.issue(t, []string{"other.test"}, nil, x509.ExtKeyUsageClientAuth)
	clientOnlyCert, clientOnlyKey := ca.issue(t, []string{"client.test"}, nil, x509.ExtKeyUsageClientAuth)
	expired := expiredCert(t, ca)
	emptyCA := writeNode(t, t.TempDir(), validServerCert, validServerKey, nil)
	badCA := writeNode(t, t.TempDir(), validServerCert, validServerKey, []byte("not a certificate"))
	missingCA := writeNode(t, t.TempDir(), validServerCert, validServerKey, ca.pem)
	if err := os.Remove(filepath.Join(missingCA, CAFile)); err != nil {
		t.Fatal(err)
	}
	mismatch := writeNode(t, t.TempDir(), validServerCert, otherKey, ca.pem)
	notServer := writeNode(t, t.TempDir(), clientOnlyCert, clientOnlyKey, ca.pem)
	notClient := writeNode(t, t.TempDir(), validServerCert, validServerKey, ca.pem)
	otherCA := newAuthority(t)
	otherCert, otherKey := otherCA.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	otherAuthority := writeNode(t, t.TempDir(), otherCert, otherKey, ca.pem)
	openDir := writeNode(t, t.TempDir(), validServerCert, validServerKey, ca.pem)
	// The directory is made writable on purpose, to prove the loader refuses it.
	if err := os.Chmod(openDir, 0o775); err != nil { //nolint:gosec // see above
		t.Fatal(err)
	}
	// The key is made readable on purpose, to prove the loader refuses it.
	if err := os.Chmod(filepath.Join(serverDir, KeyFile), 0o644); err != nil { //nolint:gosec // see above
		t.Fatal(err)
	}

	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"no directory", "", "AXIOM_NODE_TLS_DIR: required"},
		{"no certificate file", filepath.Join(t.TempDir(), "absent"), CertFile + ": file not found"},
		{"a directory group or others can write", openDir, "the certificate directory must not be writable by group or others"},
		{"a key file others can read", serverDir, KeyFile + " permissions must deny all group and other access"},
		{"a certificate without its key", mismatch, "not a matching certificate and key"},
		{"no certificate authority file", missingCA, CAFile + ": file not found"},
		{"an empty certificate authority", emptyCA, CAFile + " holds no certificate"},
		{"a certificate authority that is not one", badCA, CAFile + " holds no certificate"},
		{"a certificate from another authority", otherAuthority, "is not signed by " + CAFile},
		{"a client certificate where the server needs one", notServer, "is not a server certificate"},
		{"an expired certificate", expired, "is not valid now"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Server(c.dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Server(%q) = %v, want %q", c.dir, err, c.want)
			}
		})
	}
	if _, err := Client(notClient, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "is not a client certificate") {
		t.Fatalf("Client accepted a server-only certificate: %v", err)
	}
	if _, err := Client(clientDir, ""); err == nil || !strings.Contains(err.Error(), "names no host") {
		t.Fatalf("Client accepted an empty server name: %v", err)
	}
}

// TestKeyReplacedAtOpenIsRefused replaces node.key with a 0644 file of valid key material at the moment the key is
// opened, the same ordering the key ring's test proves (#529): the type and mode that decide come from the open
// descriptor, so the file that passes the checks is the file that is read. Checking the path first and reading it again
// would admit the 0644 key, whose contents are valid. The same material loaded through the production opener without a
// replacement must succeed, so a refusal cannot come from the seam refusing everything.
func TestKeyReplacedAtOpenIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	ca := newAuthority(t)
	cert, key := ca.issue(t, []string{"node.test"}, []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth)
	dir := writeNode(t, filepath.Join(t.TempDir(), "node"), cert, key, ca.pem)
	readable := filepath.Join(t.TempDir(), "readable.key")
	if err := os.WriteFile(readable, key, 0o644); err != nil { //nolint:gosec // made readable on purpose, to prove the loader refuses it
		t.Fatal(err)
	}
	if err := os.Chmod(readable, 0o644); err != nil { //nolint:gosec // made readable on purpose, to prove the loader refuses it
		t.Fatal(err)
	}
	swap := func(name string) (*os.File, error) {
		if filepath.Base(name) == KeyFile {
			if err := os.Rename(readable, name); err != nil {
				return nil, err
			}
		}
		return os.Open(name) // #nosec G304 -- the test builds both paths it uses.
	}
	if _, _, err := loadWith(dir, x509.ExtKeyUsageServerAuth, swap); err == nil || !strings.Contains(err.Error(), "permissions must deny all group and other access") {
		t.Fatalf("a 0644 key put in place at the open: %v, want a permissions refusal", err)
	}
	unchanged := writeNode(t, filepath.Join(t.TempDir(), "unchanged"), cert, key, ca.pem)
	if _, _, err := loadWith(unchanged, x509.ExtKeyUsageServerAuth, os.Open); err != nil {
		t.Fatalf("the unchanged directory: %v", err)
	}
}

func expiredCert(t *testing.T, ca *authority) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "expired"},
		NotBefore:    time.Now().Add(-2 * time.Hour),
		NotAfter:     time.Now().Add(-time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return writeNode(t, t.TempDir(), pemBlock("CERTIFICATE", der), pemBlock("EC PRIVATE KEY", keyDER), ca.pem)
}
