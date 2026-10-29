package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"

	"axiom/internal/platform/egress"
)

// SMTP replies can echo passwords and recipients. Log only typed transport diagnoses and numeric reply codes.
func smtpDiagnostic(err error) error {
	if reply, ok := errors.AsType[*textproto.Error](err); ok {
		return fmt.Errorf("SMTP reply %d", reply.Code)
	}
	if verification, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return fmt.Errorf("TLS certificate verification failed (%T)", verification.Err)
	}
	if dns, ok := errors.AsType[*net.DNSError](err); ok {
		return fmt.Errorf("SMTP DNS lookup failed (timeout=%t, not_found=%t)", dns.IsTimeout, dns.IsNotFound)
	}

	switch {
	case errors.Is(err, egress.ErrAddressDenied):
		return errors.New("SMTP destination denied by egress policy")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("SMTP deadline exceeded")
	case errors.Is(err, context.Canceled):
		return errors.New("SMTP test cancelled")
	case errors.Is(err, ErrInvalidMessage):
		return errors.New("SMTP test message is invalid")
	case errors.Is(err, ErrRateLimited):
		return ErrRateLimited
	default:
		return errors.New("SMTP connection or protocol failed")
	}
}
