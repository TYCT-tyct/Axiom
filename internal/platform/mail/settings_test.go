package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

func settingsKeys(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestSMTPSettingsEncryptionAndRollback(t *testing.T) {
	t.Parallel()
	w := newQueueWorld(t)
	keys := settingsKeys(t)
	ctx := t.Context()
	scope := pg.NewScope(true, nil, nil)
	cfg := Settings{Enabled: true, Host: "smtp.example.com", Port: 587, Username: "test", From: "no-reply@example.com", Password: new("saved-test-password")}
	save := func(s Settings) error {
		return w.db.Tx(ctx, scope, func(tx pg.Tx) error {
			_, err := SaveConfig(ctx, tx, keys, audit.System("mail.test"), s, "test")
			return err
		})
	}
	if err := save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Password = nil
	if err := save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Host = "other.example.com"
	if err := save(cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("forwarded password: %v", err)
	}
	if err := w.db.Tx(ctx, scope, func(tx pg.Tx) error {
		_, err := SaveConfig(ctx, tx, keys, audit.Actor{}, Settings{}, "audit must fail")
		return err
	}); err == nil {
		t.Fatal("saved configuration without audit")
	}
	checkSavedSMTP(t, w.db, keys, scope)
	for _, scope := range []pg.Scope{{}, pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, nil)} {
		if err := w.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
			_, found, err := StoredConfig(ctx, tx, keys)
			if found {
				t.Fatal("SMTP visible outside platform")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.db.Tx(ctx, scope, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.smtp_config SET ciphertext='broken'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		_, _, err := StoredConfig(ctx, tx, keys)
		if err == nil {
			t.Fatal("tampering accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The probe shares the production sender; only the fixture's trusted certificate differs.
func TestSMTPConfigProbe(t *testing.T) {
	t.Parallel()

	if got := TestConfig(t.Context(), Config{}, "recipient@example.com", slog.New(slog.DiscardHandler)); got != "disabled" {
		t.Fatal(got)
	}
	for _, want := range []string{"accepted", "rejected", "failed"} {
		t.Run(want, func(t *testing.T) {
			checkSMTPProbe(t, want)
		})
	}
}

func checkSavedSMTP(t *testing.T, db *pg.DB, keys *secret.Keyring, scope pg.Scope) {
	t.Helper()
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) error {
		read, found, err := StoredConfig(t.Context(), tx, keys)
		if err != nil {
			return err
		}
		if !found || read.Host != "smtp.example.com" || read.Password.Reveal() != "saved-test-password" {
			t.Fatal("stored config changed")
		}
		var bytes []byte
		if err := tx.QueryRow(t.Context(), "SELECT ciphertext FROM platform.smtp_config").Scan(&bytes); err != nil {
			return err
		}
		if strings.Contains(string(bytes), "saved-test-password") {
			t.Fatal("plaintext credential")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func checkSMTPProbe(t *testing.T, want string) {
	t.Helper()
	server := startTLSServer(t, func(s *fakeServer) {
		if want == "rejected" {
			s.rejectStep = "rcpt"
			s.rejectCode = 550
		}
		if want == "failed" {
			s.startTLS = false
		}
	})
	if got := server.openSender(t, Config{Host: "127.0.0.1", Port: 587}).testConfig(t.Context(), "recipient@example.com", slog.New(slog.DiscardHandler)); got != want {
		t.Fatalf("probe %s, want %s", got, want)
	}

	if want == "accepted" {
		delivery := waitDelivery(t, server)
		if !delivery.encrypted {
			t.Fatal("unencrypted test")
		}
	}
}

func TestSMTPDiagnosticRedactsServerEcho(t *testing.T) {
	t.Parallel()
	server := startTLSServer(t, func(s *fakeServer) {
		s.user = "test"
		s.pass = "planted-test-password"
		s.rejectStep = "rcpt"
		s.rejectCode = 550
		s.rejectText = "planted-test-password private-recipient@example.com"
	})
	var logs bytes.Buffer
	sender := server.openSender(t, Config{Host: "127.0.0.1", Port: 587, Username: "test", Password: secret.New("planted-test-password")})
	status := sender.testConfig(t.Context(), "private-recipient@example.com", slog.New(slog.NewJSONHandler(&logs, nil)))
	if strings.Contains(logs.String(), "planted-test-password") || strings.Contains(logs.String(), "private-recipient@example.com") {
		t.Fatal("SMTP echo leaked into diagnostic")
	}
	if status != "rejected" || !strings.Contains(logs.String(), "SMTP reply 550") || !strings.Contains(logs.String(), "request_id") {
		t.Fatalf("missing diagnostic: %s", logs.String())
	}
}

func TestSMTPDiagnosticCategories(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&tls.CertificateVerificationError{Err: x509.HostnameError{Host: "private-value"}}, "x509.HostnameError"},
		{&net.DNSError{Err: "private-value", Name: "private-value", IsNotFound: true}, "not_found=true"},
		{egress.ErrAddressDenied, "destination denied"},
		{context.DeadlineExceeded, "deadline exceeded"},
		{context.Canceled, "cancelled"},
		{ErrInvalidMessage, "message is invalid"},
		{ErrRateLimited, ErrRateLimited.Error()},
		{errors.New("private-value"), "connection or protocol failed"},
	} {
		diagnostic := smtpDiagnostic(tc.err).Error()
		if !strings.Contains(diagnostic, tc.want) || strings.Contains(diagnostic, "private-value") {
			t.Fatalf("unsafe or unhelpful diagnostic: %s", diagnostic)
		}
	}
}
