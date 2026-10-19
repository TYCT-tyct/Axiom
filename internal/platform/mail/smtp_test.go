package mail

import (
	"bytes"
	"context"
	"errors"
	"mime"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

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
			_, err := sd.Send(t.Context(), Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"})
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

func TestSendRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		step     string
		code     int
		rejected bool
	}{
		{"a rejected sender", "mail", 550, true},
		{"a rejected recipient", "rcpt", 550, true},
		{"a rejected message", "data", 550, true},
		{"a temporary sender failure", "mail", 451, false},
		{"a temporary recipient failure", "rcpt", 451, false},
		{"a temporary message failure", "data", 451, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			server := startTLSServer(t, func(s *fakeServer) {
				s.rejectStep, s.rejectCode = c.step, c.code
			})
			sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
			_, err := sd.Send(t.Context(), Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"})
			if err == nil {
				t.Fatal("Send() = nil; want the server's refusal")
			}
			if got := errors.Is(err, ErrRejected); got != c.rejected {
				t.Errorf("Send() error = %v; ErrRejected = %v, want %v", err, got, c.rejected)
			}
			select {
			case got := <-server.messages:
				t.Errorf("the server accepted %q", got.payload)
			default:
			}
		})
	}
}

func TestSendRequiresSTARTTLS(t *testing.T) {
	t.Parallel()
	server := newFakeServer(t, nil)
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	_, err := sd.Send(t.Context(), Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"})
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
	if _, err := sd.Send(t.Context(), Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"}); err != nil {
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
	_, err := sd.Send(t.Context(), Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send() error = %v; want the session deadline", err)
	}
}

func TestSendRateLimitsRecipient(t *testing.T) {
	t.Parallel()
	server := startTLSServer(t, nil)
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	msg := Message{To: "user@example.com", Subject: "Welcome", Text: "Hello", HTML: "<p>Hello</p>"}
	if _, err := sd.Send(t.Context(), msg); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}
	if _, err := sd.Send(t.Context(), msg); !errors.Is(err, ErrRateLimited) {
		t.Errorf("second Send() error = %v; want ErrRateLimited", err)
	}
	other := msg
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
