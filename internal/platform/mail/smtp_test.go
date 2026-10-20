package mail

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net"
	netmail "net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/secret"
)

func TestModeFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		port int
		want tlsMode
	}{
		{465, implicitTLS}, {587, startTLS}, {25, startTLS}, {2525, startTLS},
	} {
		if got := modeFor(c.port); got != c.want {
			t.Errorf("modeFor(%d) = %q; want %q", c.port, got, c.want)
		}
	}
}

// testMessage is a message every test below sends, unless it is the message under test.
var testMessage = Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"}

func TestSend(t *testing.T) {
	t.Parallel()
	server := startTLSServer(t, func(s *fakeServer) { s.user, s.pass = "axiom", "s3cret" })
	sd := server.openSender(t, Config{
		Host: "127.0.0.1", Port: 587, Username: "axiom", Password: secret.New("s3cret"),
	})
	msg := Message{To: "Test User <user@example.com>", Subject: "验证邮件", Text: "验证码 123456", HTML: "<p>验证码 123456</p>"}
	id, err := sd.Send(t.Context(), msg)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@axiom.example>") {
		t.Errorf("Send() id = %q; want an id at axiom.example", id)
	}
	got := waitDelivery(t, server)
	checkDelivered(t, got, id, msg)
	checkEnvelope(t, got, "MAIL FROM:<no-reply@axiom.example>", "RCPT TO:<user@example.com>")
}

// checkDelivered checks the message the server accepted: encrypted, and parsed back with the headers and bodies the
// sender was given.
func checkDelivered(t *testing.T, got delivery, id string, msg Message) {
	t.Helper()
	if !got.encrypted {
		t.Error("the session was not encrypted")
	}
	parsed, err := netmail.ReadMessage(bytes.NewReader(got.payload))
	if err != nil {
		t.Fatalf("the server's copy does not parse: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Errorf("Subject = %q, %v; want %q", subject, err, msg.Subject)
	}
	if got := parsed.Header.Get("Message-Id"); got != id {
		t.Errorf("Message-Id = %q; want the id Send returned, %q", got, id)
	}
	if from, err := parsed.Header.AddressList("From"); err != nil || len(from) != 1 || from[0].Address != "no-reply@axiom.example" {
		t.Errorf("From = %v, %v; want no-reply@axiom.example", from, err)
	}
	if text, html := bodyParts(t, got.payload); text != msg.Text || html != msg.HTML {
		t.Errorf("bodies = %q, %q; want %q, %q", text, html, msg.Text, msg.HTML)
	}
}

// checkEnvelope compares the envelope the server received, line by line: the bytes of MAIL FROM and RCPT TO are what the
// receiving server uses to route and to refuse, so a test asserts them rather than the address they came from.
func checkEnvelope(t *testing.T, got delivery, want ...string) {
	t.Helper()
	if diff := cmp.Diff(want, got.envelope); diff != "" {
		t.Errorf("envelope (-want +got):\n%s", diff)
	}
}

func TestSendAuthenticationFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		password   string
		rejectStep string
	}{
		{name: "a wrong password", password: "planted-password"},
		{name: "a server that refuses the account", password: "s3cret", rejectStep: "auth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			server := startTLSServer(t, func(s *fakeServer) {
				s.user, s.pass = "axiom", "s3cret"
				s.rejectStep = c.rejectStep
			})
			sd := server.openSender(t, Config{
				Host: "127.0.0.1", Port: 587, Username: "axiom", Password: secret.New(c.password),
			})
			_, err := sd.Send(t.Context(), testMessage)
			if !errors.Is(err, ErrRejected) {
				t.Errorf("Send() error = %v; want ErrRejected", err)
			}
			if strings.Contains(err.Error(), "planted-password") {
				t.Errorf("Send() error = %q quotes the password", err)
			}
			select {
			case got := <-server.messages:
				t.Errorf("the server accepted %q after a failed authentication", got.payload)
			default:
			}
		})
	}
}

// TestSendRefusal covers every step a server can refuse, with a permanent and a temporary reply: 5xx is the server
// refusing this message for good, and a job must cancel rather than retry; 4xx is a fault that may pass.
func TestSendRefusal(t *testing.T) {
	t.Parallel()
	steps := []string{"hello", "starttls", "auth", "mail", "rcpt", "data"}
	for _, step := range steps {
		for _, code := range []int{451, 550} {
			t.Run(step+"/"+strconv.Itoa(code), func(t *testing.T) {
				t.Parallel()
				server := startTLSServer(t, func(s *fakeServer) {
					s.user, s.pass = "axiom", "s3cret"
					s.rejectStep, s.rejectCode = step, code
				})
				sd := server.openSender(t, Config{
					Host: "127.0.0.1", Port: 587, Username: "axiom", Password: secret.New("s3cret"),
				})
				_, err := sd.Send(t.Context(), testMessage)
				if err == nil {
					t.Fatal("Send() = nil; want the server's refusal")
				}
				if got, want := errors.Is(err, ErrRejected), code >= 500; got != want {
					t.Errorf("Send() error = %v; ErrRejected = %v, want %v", err, got, want)
				}
				select {
				case got := <-server.messages:
					t.Errorf("the server accepted %q", got.payload)
				default:
				}
			})
		}
	}
}

// TestSendEnvelope: the envelope carries the same mailbox as the header, serialized as RFC 5321 wants it, so a local
// part holding a space, a quote or a backslash arrives at the server as the address the caller meant.
func TestSendEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		from     string
		to       string
		mailFrom string
		rcptTo   string
		wantFrom string
		wantTo   string
	}{
		{
			name:     "a space in both local parts",
			from:     `"no reply" <"no reply"@axiom.example>`,
			to:       `"a b" <"a b"@example.com>`,
			mailFrom: `MAIL FROM:<"no reply"@axiom.example>`,
			rcptTo:   `RCPT TO:<"a b"@example.com>`,
			wantFrom: `"no reply" <"no reply"@axiom.example>`,
			wantTo:   `"a b" <"a b"@example.com>`,
		},
		{
			name:     "a quote and a backslash",
			from:     `no-reply@axiom.example`,
			to:       `"a\"b\\c" <"a\"b\\c"@example.com>`,
			mailFrom: `MAIL FROM:<no-reply@axiom.example>`,
			rcptTo:   `RCPT TO:<"a\"b\\c"@example.com>`,
			wantFrom: `<no-reply@axiom.example>`,
			wantTo:   `"a\"b\\c" <"a\"b\\c"@example.com>`,
		},
		{
			name:     "a dot-atom local part needs no quotes",
			from:     `no-reply@axiom.example`,
			to:       `user.name+tag@example.com`,
			mailFrom: `MAIL FROM:<no-reply@axiom.example>`,
			rcptTo:   `RCPT TO:<user.name+tag@example.com>`,
			wantFrom: `<no-reply@axiom.example>`,
			wantTo:   `<user.name+tag@example.com>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			from, err := netmail.ParseAddress(c.from)
			if err != nil {
				t.Fatalf("the test's sender does not parse: %v", err)
			}
			server := startTLSServer(t, nil)
			sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587, From: *from})
			if _, err := sd.Send(t.Context(), Message{
				To: c.to, Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>",
			}); err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			got := waitDelivery(t, server)
			checkEnvelope(t, got, c.mailFrom, c.rcptTo)
			checkAddressHeaders(t, got, c.wantFrom, c.wantTo)
		})
	}
}

// checkAddressHeaders checks that the headers name the same addresses the envelope carried.
func checkAddressHeaders(t *testing.T, got delivery, wantFrom, wantTo string) {
	t.Helper()
	parsed, err := netmail.ReadMessage(bytes.NewReader(got.payload))
	if err != nil {
		t.Fatalf("the server's copy does not parse: %v", err)
	}
	if from := parsed.Header.Get("From"); from != wantFrom {
		t.Errorf("From = %q; want %q", from, wantFrom)
	}
	if to := parsed.Header.Get("To"); to != wantTo {
		t.Errorf("To = %q; want %q", to, wantTo)
	}
}

func TestSendRequiresSTARTTLS(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, nil)
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	_, err := sd.Send(t.Context(), testMessage)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("Send() error = %v; want a refusal to send in the clear", err)
	}
	if errors.Is(err, ErrRejected) {
		t.Errorf("Send() error = %v; a missing STARTTLS is not a 5xx refusal", err)
	}
	select {
	case got := <-server.messages:
		t.Errorf("the server accepted %q without TLS", got.payload)
	default:
	}
}

func TestSendImplicitTLS(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, func(s *fakeServer) {
		s.implicit = true
		s.cert, s.pool = testCertificate(t)
	})
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 465})
	if _, err := sd.Send(t.Context(), testMessage); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := waitDelivery(t, server); !got.encrypted {
		t.Error("the session was not encrypted")
	}
}

func TestSendTimeout(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, func(s *fakeServer) { s.silent = true })
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	sd.sendTimeout = 50 * time.Millisecond
	_, err := sd.Send(t.Context(), testMessage)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send() error = %v; want the session deadline", err)
	}
}

// TestSendDeadlineFirst: the session's deadline passes while the server says nothing, and the send reports the deadline
// rather than the closed connection that carried it. The context's deadline has already passed, so the order of the two
// is fixed and the test needs no clock.
func TestSendDeadlineFirst(t *testing.T) {
	t.Parallel()
	sd := blockingSender()
	ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
	defer cancel()
	if _, err := sd.Send(ctx, testMessage); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send() error = %v; want the session deadline", err)
	}
}

// TestSendSocketFirst: the socket fails while the session's deadline is still ahead — a server that closes the session,
// a reset — and the send reports what the socket did, not a deadline it never reached. The blocking connection says when
// it is the socket's turn, so the order is explicit and nothing waits on the clock.
func TestSendSocketFirst(t *testing.T) {
	t.Parallel()
	conn := newBlockingConn()
	sd := blockingSenderWith(conn)
	errc := make(chan error, 1)
	go func() {
		_, err := sd.Send(t.Context(), testMessage)
		errc <- err
	}()
	<-conn.reading // the handshake is waiting for a greeting the server will never send
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	err := <-errc
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("Send() error = %v; want the socket's own failure", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send() error = %v; a socket that timed out is not the session's deadline", err)
	}
}

// blockingSender is a sender whose connection answers nothing until the test closes it.
func blockingSender() *sender { return blockingSenderWith(newBlockingConn()) }

func blockingSenderWith(conn *blockingConn) *sender {
	sd := newSender(Config{
		Host: "127.0.0.1", Port: 587, From: netmail.Address{Name: "Axiom", Address: "no-reply@axiom.example"},
	})
	sd.dial = func(context.Context) (net.Conn, error) { return conn, nil }
	return sd
}

// blockingConn is a connection that reads nothing and blocks until it is closed, and then reports the failure a socket
// reports when its deadline passes. It is how a test picks the winner between the socket and the session's deadline
// without sleeping.
type blockingConn struct {
	reading   chan struct{}
	closed    chan struct{}
	readOnce  sync.Once
	closeOnce sync.Once
}

func newBlockingConn() *blockingConn {
	return &blockingConn{reading: make(chan struct{}), closed: make(chan struct{})}
}

func (c *blockingConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.reading) })
	<-c.closed
	return 0, os.ErrDeadlineExceeded
}

// Write accepts everything: the handshake under test never gets as far as writing.
func (*blockingConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *blockingConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (*blockingConn) LocalAddr() net.Addr              { return testAddr{} }
func (*blockingConn) RemoteAddr() net.Addr             { return testAddr{} }
func (*blockingConn) SetDeadline(time.Time) error      { return nil }
func (*blockingConn) SetReadDeadline(time.Time) error  { return nil }
func (*blockingConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr struct{}

func (testAddr) Network() string { return "test" }
func (testAddr) String() string  { return "test" }

func TestSendRateLimitsRecipient(t *testing.T) {
	t.Parallel()
	server := startTLSServer(t, nil)
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	if _, err := sd.Send(t.Context(), testMessage); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	if _, err := sd.Send(t.Context(), testMessage); !errors.Is(err, ErrRateLimited) {
		t.Errorf("second Send() error = %v; want ErrRateLimited", err)
	}
	other := testMessage
	other.To = "other@example.com"
	if _, err := sd.Send(t.Context(), other); err != nil {
		t.Errorf("Send() to another recipient = %v", err)
	}
	waitDelivery(t, server)
	waitDelivery(t, server)
	select {
	case got := <-server.messages:
		t.Errorf("the server accepted a third message: %q", got.payload)
	default:
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := &limiter{last: map[string]time.Time{}, now: func() time.Time { return now }, interval: time.Minute}
	if !l.allow("User@Example.com") {
		t.Error("the first message to a recipient was refused")
	}
	if l.allow("user@example.com") {
		t.Error("a second message to the same address in another case was allowed")
	}
	if !l.allow("other@example.com") {
		t.Error("a message to another recipient was refused")
	}
	now = now.Add(time.Minute - time.Nanosecond)
	if l.allow("user@example.com") {
		t.Error("a message inside the interval was allowed")
	}
	now = now.Add(time.Nanosecond)
	if !l.allow("user@example.com") {
		t.Error("a message after the interval was refused")
	}
	if len(l.last) != 1 {
		t.Errorf("the limiter holds %d recipients; want only the one just messaged", len(l.last))
	}
}
