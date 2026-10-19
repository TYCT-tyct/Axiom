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
)

const (
	// defaultDialTimeout bounds the connection, DNS included. sub2api dials with the same limit
	// (backend/internal/service/email_service.go smtpDialTimeout, 2026-10-19).
	defaultDialTimeout = 10 * time.Second
	// defaultSendTimeout bounds one whole session. A server that stops answering mid-session holds a queue worker
	// until it ends, and sub2api bounds a whole delivery at 30 seconds too; larger than a normal message needs,
	// shorter than the queue's ten-minute job timeout.
	defaultSendTimeout = 30 * time.Second
	// rateInterval is how long a recipient is left alone after one message, so a repeated flow cannot fill an inbox.
	// sub2api keeps a minute between verification mails to one address (email_service.go verifyCodeCooldown,
	// 2026-10-19).
	rateInterval = time.Minute
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

// sender delivers one message per SMTP session. It is safe for concurrent use: the limiter locks, and every Send
// opens its own connection.
type sender struct {
	cfg         Config
	addr        string
	mode        tlsMode
	roots       *x509.CertPool // nil is the system's trust store; tests inject their own server's
	dialTimeout time.Duration
	sendTimeout time.Duration
	limiter     *limiter
}

// newSender takes a Config that ReadConfig accepted.
func newSender(cfg Config) *sender {
	return &sender{
		cfg:         cfg,
		addr:        net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		mode:        modeFor(cfg.Port),
		dialTimeout: defaultDialTimeout,
		sendTimeout: defaultSendTimeout,
		limiter:     newLimiter(),
	}
}

// Send delivers one message and returns the Message-ID it used, which it also returns when delivery fails so the log
// can be matched against the server's. A recipient messaged within rateInterval is refused with ErrRateLimited, a
// server reply of 5xx wraps ErrRejected, and the rest is worth another try.
func (s *sender) Send(ctx context.Context, msg Message) (string, error) {
	to, err := msg.validate()
	if err != nil {
		return "", err
	}
	if !s.limiter.allow(to.Address) {
		return "", fmt.Errorf("%w: %s", ErrRateLimited, to.Address)
	}
	c := compose{
		from: s.cfg.From, to: *to, subject: msg.Subject, text: msg.Text, html: msg.HTML,
		date: time.Now().UTC(), messageID: newMessageID(s.cfg.From.Address), boundary: rand.Text(),
	}

	ctx, cancel := context.WithTimeout(ctx, s.sendTimeout)
	defer cancel()
	conn, err := s.connect(ctx)
	if err != nil {
		return c.messageID, interrupted(ctx, err)
	}
	defer conn.Close() //nolint:errcheck // the session is over; a failure to close it cannot change the outcome
	// A cancelled job (the queue stopping) unblocks the read below; the deadline alone would wait out the whole send.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() }) //nolint:errcheck // same as the deferred close
	defer stop()
	client, err := s.session(conn)
	if err != nil {
		return c.messageID, interrupted(ctx, err)
	}
	defer client.Close() //nolint:errcheck // the message is accepted or not; closing cannot change that
	if err := deliver(client, c); err != nil {
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

// connect dials and sets the deadline of the whole session on the connection, so no single step can outlive the send.
func (s *sender) connect(ctx context.Context) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: s.dialTimeout}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", s.cfg.Host, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close() //nolint:errcheck // the connection is unusable either way
			return nil, fmt.Errorf("mail: set the session deadline: %w", err)
		}
	}
	return conn, nil
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
		client, err = smtp.NewClient(tls.Client(conn, s.tlsConfig()), s.cfg.Host)
		if err != nil {
			return nil, wrapRejected("greet", err)
		}
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

// upgrade opens a plain session and moves it to TLS before anything else is sent.
func (s *sender) upgrade(conn net.Conn) (*smtp.Client, error) {
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return nil, wrapRejected("greet", err)
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		_ = client.Close() //nolint:errcheck // the session is unusable either way
		return nil, errors.New("mail: the server does not offer STARTTLS, so the message is not sent in the clear")
	}
	if err := client.StartTLS(s.tlsConfig()); err != nil {
		_ = client.Close() //nolint:errcheck // the session is unusable either way
		return nil, fmt.Errorf("mail: start TLS: %w", err)
	}
	return client, nil
}

// tlsConfig verifies the server's certificate against its host name. There is no switch to skip that check: new-api
// offers SMTPInsecureSkipVerify (common/email.go, 2026-10-19), which would let any network position read the password
// and every message; a private CA belongs in the system's trust store instead.
func (s *sender) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}
}

// deliver sends the envelope and the message. The message is accepted when the DATA reply arrives, so a failing QUIT
// afterwards is not reported: some servers answer it in ways net/smtp takes for an error.
func deliver(client *smtp.Client, c compose) error {
	if err := client.Mail(c.from.Address); err != nil {
		return wrapRejected("MAIL FROM", err)
	}
	if err := client.Rcpt(c.to.Address); err != nil {
		return wrapRejected("RCPT TO", err)
	}
	w, err := client.Data()
	if err != nil {
		return wrapRejected("DATA", err)
	}
	if _, err := w.Write(c.bytes()); err != nil {
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
