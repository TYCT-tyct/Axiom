package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"axiom/internal/platform/egress"
)

const (
	// defaultDialTimeout bounds the connection, DNS included, and nothing else. sub2api dials with the same limit
	// (backend/internal/service/email_service.go smtpDialTimeout, 2026-10-19).
	defaultDialTimeout = 10 * time.Second
	// defaultSendTimeout bounds one whole session, dial included: one clock for the send, so a connection, a
	// greeting, a TLS handshake or a stalled body cannot each spend a timeout of their own. sub2api bounds a whole
	// delivery at 30 seconds too; larger than a normal message needs, shorter than the queue's ten-minute job
	// timeout.
	defaultSendTimeout = 30 * time.Second
	// rateInterval is how long a recipient is left alone after one message, so a repeated flow cannot fill an inbox.
	// sub2api keeps a minute between verification mails to one address (email_service.go verifyCodeCooldown,
	// 2026-10-19).
	rateInterval = time.Minute
	// helloName is the name the client gives itself in EHLO. net/smtp defaults to this one; saying it here makes the
	// hello a step of its own, with an error of its own.
	helloName = "localhost"
)

// tlsMode is how a session is encrypted before the first command is sent.
type tlsMode string

const (
	// startTLS is a plain connection upgraded before authentication, the shape of the submission port 587.
	startTLS tlsMode = "starttls"
	// implicitTLS is TLS from the first byte, the shape of port 465.
	implicitTLS tlsMode = "implicit"
)

// modeFor gives the mode a port speaks. new-api chooses implicit TLS for 465 and TLS by hand otherwise
// (common/email.go newSMTPClient, 2026-10-19); Axiom has no plaintext mode, so every other port upgrades with
// STARTTLS and a server that does not offer it is refused.
func modeFor(port int) tlsMode {
	if port == 465 {
		return implicitTLS
	}
	return startTLS
}

// sender delivers one message per SMTP session. It is safe for concurrent use: the throttle serializes its decision, and
// every Send opens its own connection.
type sender struct {
	exit        *egress.Client
	cfg         Config
	addr        string
	mode        tlsMode
	roots       *x509.CertPool // nil is the system's trust store; tests inject their own server's
	dialTimeout time.Duration
	sendTimeout time.Duration
	throttle    *throttle
	// dial opens the connection a session runs on. Tests replace it to control the socket: a real one cannot be made
	// to fail on demand without a clock.
	dial func(ctx context.Context) (net.Conn, error)
}

// newSender takes a Config that ReadConfig accepted.
func newSender(cfg Config) *sender {
	s := &sender{
		exit:        egress.NewDirect(),
		cfg:         cfg,
		addr:        net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		mode:        modeFor(cfg.Port),
		dialTimeout: defaultDialTimeout,
		sendTimeout: defaultSendTimeout,
		throttle:    newThrottle(),
	}
	s.dial = s.dialServer
	return s
}

// Send delivers one message and returns the Message-ID it used, which it also returns when delivery fails so the log
// can be matched against the server's. A recipient messaged within rateInterval is refused with ErrRateLimited, a
// server reply of 5xx wraps ErrRejected, and the rest is worth another try.
func (s *sender) Send(ctx context.Context, msg Message) (string, error) {
	to, err := msg.validate()
	if err != nil {
		return "", err
	}
	// The refusal carries no address: it reaches the caller, a log line and, through the job, a row no tenant policy
	// covers, and every one of those already knows the recipient.
	if !s.throttle.allow(ctx, to.Address) {
		return "", ErrRateLimited
	}
	c := compose{
		from: s.cfg.From, to: *to, subject: msg.Subject, text: msg.Text, html: msg.HTML,
		date: time.Now().UTC(), messageID: newMessageID(s.cfg.From.Address), boundary: rand.Text(),
	}
	// A value the headers cannot carry is refused before a connection is opened: every server would refuse it the same
	// way, and the job must not spend its attempts on it.
	payload, err := c.bytes()
	if err != nil {
		return c.messageID, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.sendTimeout)
	defer cancel()
	conn, err := s.connect(ctx)
	if err != nil {
		return c.messageID, interrupted(ctx, err)
	}
	defer conn.Close() //nolint:errcheck // the session is over; a failure to close it cannot change the outcome
	// The send's deadline is the context's, and the connection carries none of its own: when the context ends, the
	// connection is closed and the read that was waiting returns at once, and interrupted names the deadline rather
	// than the socket it looks like.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() }) //nolint:errcheck // same as the deferred close
	defer stop()
	client, err := s.session(conn)
	if err != nil {
		return c.messageID, interrupted(ctx, err)
	}
	defer client.Close() //nolint:errcheck // the message is accepted or not; closing cannot change that
	if err := deliver(client, c, payload); err != nil {
		return c.messageID, interrupted(ctx, err)
	}
	return c.messageID, nil
}

// interrupted adds why a send was cut short: a closed connection says nothing about the deadline that closed it, and a
// job that ran out of time should record the timeout rather than a network fault.
func interrupted(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(ctxErr, err)
	}
	return err
}

// connect opens the connection the session runs on. The dial has a timeout of its own, which ends the DNS lookup and
// the handshake of a host that does not answer; everything after it is bounded by the context, one deadline for the
// whole send.
func (s *sender) connect(ctx context.Context) (net.Conn, error) {
	conn, err := s.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", s.cfg.Host, err)
	}
	return conn, nil
}

// dialServer is the real dial, whose timeout covers the addresses of a name and the TCP handshake.
func (s *sender) dialServer(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	defer cancel()
	return s.exit.DialContext(ctx, s.addr)
}

// session speaks TLS and authenticates: port 465 is TLS from the first byte, everything else is upgraded with
// STARTTLS, and a server that does not offer it is refused rather than sent a password in the clear.
func (s *sender) session(conn net.Conn) (*smtp.Client, error) {
	var (
		client *smtp.Client
		err    error
	)
	switch s.mode {
	case implicitTLS:
		client, err = greet(tls.Client(conn, s.tlsConfig()), s.cfg.Host)
	case startTLS:
		client, err = s.upgrade(conn)
	}
	if err != nil {
		return nil, err
	}
	if s.cfg.Username == "" {
		return client, nil
	}
	if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password.Reveal(), s.cfg.Host)); err != nil {
		_ = client.Close() //nolint:errcheck // reporting the authentication failure is what matters
		return nil, wrapRejected("authenticate", err)
	}
	return client, nil
}

// greet reads the greeting and says hello. net/smtp sends EHLO, falls back to HELO, and keeps whichever error it got for
// the next command to report, where it would be blamed on MAIL FROM; asking here gives the handshake an error of its
// own, with the step's name.
func greet(conn net.Conn, host string) (*smtp.Client, error) {
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return nil, wrapRejected("greet", err)
	}
	if err := client.Hello(helloName); err != nil {
		_ = client.Close() //nolint:errcheck // the session is unusable either way
		return nil, wrapRejected("hello", err)
	}
	return client, nil
}

// upgrade opens a plain session and moves it to TLS before anything else is sent. StartTLS reports both ways of failing
// — the command refused, and the EHLO that follows the handshake refused — so a 5xx on either is permanent.
func (s *sender) upgrade(conn net.Conn) (*smtp.Client, error) {
	client, err := greet(conn, s.cfg.Host)
	if err != nil {
		return nil, err
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		_ = client.Close() //nolint:errcheck // the session is unusable either way
		return nil, errors.New("mail: the server does not offer STARTTLS, so the message is not sent in the clear")
	}
	if err := client.StartTLS(s.tlsConfig()); err != nil {
		_ = client.Close() //nolint:errcheck // the session is unusable either way
		return nil, wrapRejected("start TLS", err)
	}
	return client, nil
}

// tlsConfig verifies the server's certificate against its host name. There is no switch to skip that check: new-api
// offers SMTPInsecureSkipVerify (common/email.go, 2026-10-19), which would let any network position read the password
// and every message; a private CA belongs in the system's trust store instead.
func (s *sender) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}
}

// deliver sends the envelope and the message. The payload is rendered before this point, so a message the headers cannot
// carry never reaches the wire, and the envelope paths are the same mailboxes the headers show. The message is accepted
// when the DATA reply arrives, so a failing QUIT afterwards is not reported: some servers answer it in ways net/smtp
// takes for an error.
func deliver(client *smtp.Client, c compose, payload []byte) error {
	from, err := mailbox(c.from)
	if err != nil {
		return err
	}
	to, err := mailbox(c.to)
	if err != nil {
		return err
	}
	if err := client.Mail(from); err != nil {
		return wrapRejected("MAIL FROM", err)
	}
	if err := client.Rcpt(to); err != nil {
		return wrapRejected("RCPT TO", err)
	}
	w, err := client.Data()
	if err != nil {
		return wrapRejected("DATA", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("mail: write the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return wrapRejected("end of DATA", err)
	}
	_ = client.Quit() //nolint:errcheck // the message is already accepted; a nonstandard reply must not undo that
	return nil
}

// wrapRejected marks a 5xx reply: the server will refuse the same message again, so the job must not retry it.
func wrapRejected(step string, err error) error {
	if replied, ok := errors.AsType[*textproto.Error](err); ok && replied.Code >= 500 && replied.Code < 600 {
		return fmt.Errorf("%w: %s: %w", ErrRejected, step, err)
	}
	return fmt.Errorf("mail: %s: %w", step, err)
}
