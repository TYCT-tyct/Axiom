package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	netmail "net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// delivery is one message the fake server accepted, the envelope commands that led to it, and whether the session that
// carried it was encrypted.
type delivery struct {
	payload   []byte
	envelope  []string // the MAIL FROM and RCPT TO lines as they arrived, angle brackets included
	encrypted bool
}

// fakeServer is an SMTP server for one test: it speaks enough of the protocol for one message per session, can refuse
// at a chosen step, and records what it accepted.
type fakeServer struct {
	ln         net.Listener
	cert       *tls.Certificate // nil serves without TLS
	pool       *x509.CertPool   // trusts cert, for the sender under test
	implicit   bool             // TLS from the first byte, as port 465 speaks
	startTLS   bool             // advertise and honour STARTTLS
	silent     bool             // accept and say nothing, as a stalled server does
	user, pass string           // empty serves without authentication
	rejectStep string           // "hello", "starttls", "auth", "mail", "rcpt" or "data": the step answered with rejectCode
	rejectCode int
	rejectText string // what that reply says after the code; empty uses a fixed sentence
	messages   chan delivery

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
	wg     sync.WaitGroup
}

func newFakeServer(t *testing.T, configure func(*fakeServer)) *fakeServer {
	t.Helper()
	s := &fakeServer{messages: make(chan delivery, 4)}
	if configure != nil {
		configure(s)
	}
	if s.rejectCode == 0 {
		s.rejectCode = 550
	}
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.implicit {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*s.cert}, MinVersion: tls.VersionTLS12})
	}
	s.ln = ln
	s.wg.Add(1)
	go s.accept(t.Context())
	t.Cleanup(s.stop)
	return s
}

// startTLSServer is the usual fixture: a server that upgrades with STARTTLS, using a certificate this test trusts.
func startTLSServer(t *testing.T, configure func(*fakeServer)) *fakeServer {
	t.Helper()
	return newFakeServer(t, func(s *fakeServer) {
		s.startTLS = true
		s.cert, s.pool = testCertificate(t)
		if configure != nil {
			configure(s)
		}
	})
}

// openSender builds a sender pointed at the server: the listener's port is ephemeral, so only the mode comes from the
// configured port, and the server's certificate is the only one it trusts.
func (s *fakeServer) openSender(t *testing.T, cfg Config) *sender {
	t.Helper()
	if cfg.From.Address == "" {
		cfg.From = netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"}
	}
	sd := newSender(cfg)
	sd.addr = s.ln.Addr().String()
	sd.roots = s.pool
	return sd
}

func (s *fakeServer) accept(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close() //nolint:errcheck // the session is refused, not used
			return
		}
		s.conns = append(s.conns, conn)
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.serve(ctx, conn)
		}()
	}
}

// stop closes the listener and every session, so a test that leaves a server waiting does not wait for the goroutine
// to notice on its own.
func (s *fakeServer) stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	_ = s.ln.Close() //nolint:errcheck // the listener is being torn down either way
	conns := s.conns
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close() //nolint:errcheck // the sessions are being torn down either way
	}
	s.wg.Wait()
}

func (s *fakeServer) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close() //nolint:errcheck // the test owns the connection
	if s.silent {
		_, _ = io.Copy(io.Discard, conn) //nolint:errcheck // the client's timeout closes it
		return
	}
	session := &session{
		server: s, conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn),
		encrypted: s.implicit && s.cert != nil,
	}
	if !session.write("220 fake ESMTP") {
		return
	}
	for {
		line, err := session.r.ReadString('\n')
		if err != nil {
			return
		}
		if !session.command(ctx, strings.TrimRight(line, "\r\n")) {
			return
		}
	}
}

// session is one SMTP connection of the fake server.
type session struct {
	server        *fakeServer
	conn          net.Conn
	r             *bufio.Reader
	w             *bufio.Writer
	encrypted     bool
	authenticated bool
	envelope      []string // the MAIL FROM and RCPT TO lines, as received
}

// write sends one reply line and reports whether the session continues.
func (s *session) write(line string) bool {
	if _, err := s.w.WriteString(line + "\r\n"); err != nil {
		return false
	}
	return s.w.Flush() == nil
}

// command answers one command line and reports whether the session continues.
func (s *session) command(ctx context.Context, line string) bool {
	verb, rest, _ := strings.Cut(line, " ")
	switch strings.ToUpper(verb) {
	case "EHLO":
		return s.ehlo()
	case "HELO":
		if s.server.rejectStep == "hello" {
			return s.write(s.server.rejection())
		}
		return s.write("250 OK")
	case "RSET", "NOOP":
		return s.write("250 OK")
	case "STARTTLS":
		return s.startTLS(ctx)
	case "AUTH":
		return s.authenticate(rest)
	case "MAIL":
		s.envelope = append(s.envelope, line)
		return s.mail()
	case "RCPT":
		s.envelope = append(s.envelope, line)
		return s.rcpt()
	case "DATA":
		return s.data()
	case "QUIT":
		_ = s.write("221 2.0.0 Bye")
		return false
	default:
		return s.write("502 5.5.2 Command not recognized")
	}
}

func (s *session) ehlo() bool {
	if s.server.rejectStep == "hello" {
		return s.write(s.server.rejection())
	}
	extensions := []string{"250-fake"}
	if s.server.startTLS && !s.encrypted {
		extensions = append(extensions, "250-STARTTLS")
	}
	if s.server.user != "" {
		extensions = append(extensions, "250-AUTH PLAIN")
	}
	extensions = append(extensions, "250 OK")
	for _, extension := range extensions {
		if !s.write(extension) {
			return false
		}
	}
	return true
}

func (s *session) startTLS(ctx context.Context) bool {
	if !s.server.startTLS || s.encrypted {
		return s.write("502 5.5.1 STARTTLS is not offered")
	}
	if s.server.rejectStep == "starttls" {
		return s.write(s.server.rejection())
	}
	if !s.write("220 Ready to start TLS") {
		return false
	}
	// The client sends nothing between the command and the handshake, so the line reader holds no bytes of it that
	// wrapping the connection would lose.
	conn := tls.Server(s.conn, &tls.Config{Certificates: []tls.Certificate{*s.server.cert}, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		return false
	}
	s.conn, s.encrypted = conn, true
	s.r, s.w = bufio.NewReader(conn), bufio.NewWriter(conn)
	return true
}

func (s *session) authenticate(rest string) bool {
	if s.server.rejectStep == "auth" {
		return s.write(s.server.rejection())
	}
	s.authenticated = s.server.credentials(rest)
	reply := "535 5.7.8 Authentication failed"
	if s.authenticated {
		reply = "235 2.7.0 Authentication successful"
	}
	return s.write(reply)
}

// credentials checks AUTH PLAIN <base64(identity \0 user \0 password)> against the configured account.
func (s *fakeServer) credentials(rest string) bool {
	encoded, ok := strings.CutPrefix(rest, "PLAIN ")
	if !ok {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	parts := strings.Split(string(decoded), "\x00")
	return len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass
}

func (s *session) mail() bool {
	if s.server.user != "" && !s.authenticated {
		return s.write("530 5.7.0 Authentication required")
	}
	if s.server.rejectStep == "mail" {
		return s.write(s.server.rejection())
	}
	return s.write("250 2.1.0 OK")
}

func (s *session) rcpt() bool {
	if s.server.rejectStep == "rcpt" {
		return s.write(s.server.rejection())
	}
	return s.write("250 2.1.5 OK")
}

func (s *session) data() bool {
	if s.server.rejectStep == "data" {
		return s.write(s.server.rejection())
	}
	if !s.write("354 End data with <CR><LF>.<CR><LF>") {
		return false
	}
	payload, err := readData(s.r)
	if err != nil {
		return false
	}
	if !s.write("250 2.0.0 OK: queued") {
		return false
	}
	s.server.messages <- delivery{payload: payload, envelope: s.envelope, encrypted: s.encrypted}
	return true
}

// rejection is the reply of the step the test configured the server to refuse. rejectText lets a test speak like a real
// server, which quotes the mailbox it refuses.
func (s *fakeServer) rejection() string {
	if s.rejectText != "" {
		return fmt.Sprintf("%d %s", s.rejectCode, s.rejectText)
	}
	return fmt.Sprintf("%d refused by the test server", s.rejectCode)
}

// readData reads a message up to the lone dot that ends it, undoing the dot stuffing of RFC 5321 section 4.5.2.
func readData(r *bufio.Reader) ([]byte, error) {
	var b bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == ".\r\n" {
			return b.Bytes(), nil
		}
		b.WriteString(strings.TrimPrefix(line, "."))
	}
}

func waitDelivery(t *testing.T, server *fakeServer) delivery {
	t.Helper()
	select {
	case d := <-server.messages:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the server received no message")
		return delivery{}
	}
}

// testCertificate is a self-signed certificate for the fake server's 127.0.0.1 listener, and the pool that trusts it.
func testCertificate(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "axiom mail test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return &cert, pool
}
