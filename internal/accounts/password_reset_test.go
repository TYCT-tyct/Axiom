package accounts_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/mail"
	"net/netip"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

const changedPassword = "a different horse battery"

type recoveryWorld struct {
	site
	recovery *accounts.PasswordRecovery
	queue    *pg.Queue
}

func recoverySite(t *testing.T) recoveryWorld {
	t.Helper()
	x := newTestSite(t)
	logger := slog.New(slog.DiscardHandler)
	queue := x.db.NewQueue(logger, platform, nil)
	mailer, err := axmail.Register(t.Context(), x.db, queue, axmail.Config{
		Host: "localhost", Port: 1, From: mail.Address{Address: "no-reply@example.com"},
	}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := accounts.RegisterPasswordRecovery(x.db, queue, mailer, platform, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec(t, x.db, platform, "INSERT INTO river.river_queue (name, paused_at) VALUES ('notifications', now()), ('archive', now()) ON CONFLICT (name) DO UPDATE SET paused_at = now()"); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := queue.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	x.s.UsePasswordRecovery(recovery)
	return recoveryWorld{x, recovery, queue}
}

func (x recoveryWorld) requestReset(t *testing.T, email, language string, hosts ...string) uuid.UUID {
	t.Helper()
	host := console
	if len(hosts) > 0 {
		host = hosts[0]
	}
	if err := x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{
		Host: host, Email: email, Language: language, ClientIP: client,
	}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
		return tx.QueryRow(t.Context(), "SELECT id FROM accounts.password_reset_requests WHERE site_id = $1 AND email_hash = $2", s1, hash[:]).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (x recoveryWorld) token(t *testing.T) string {
	t.Helper()
	id := x.requestReset(t, x.email, "zh")
	if err := x.recovery.ProcessPasswordReset(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT text_body FROM platform.mail_outbox ORDER BY created_at DESC, id DESC LIMIT 1").Scan(&text)
	}); err != nil {
		t.Fatal(err)
	}
	_, token, found := strings.Cut(text, "#token=")
	if !found {
		t.Fatalf("reset email has no link: %s", text)
	}
	return token
}

func (x recoveryWorld) reset(t *testing.T, host, token, pw string) error {
	t.Helper()
	return x.s.ResetPassword(t.Context(), host, token, pw, "zh", client)
}

func TestPasswordResetLifecycle(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	old := x.signIn(t, console)
	token := x.token(t)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token entropy: %d bytes, %v", len(raw), err)
	}
	digest := sha256.Sum256([]byte(token))
	got := texts(t, x.db, platform, "SELECT encode(token_hash, 'hex') FROM accounts.password_reset_tokens")
	if len(got) != 1 || got[0] != hex.EncodeToString(digest[:]) {
		t.Fatalf("stored token hash: %q", got)
	}
	if err := x.reset(t, console, token, "too short"); !errors.Is(err, accounts.ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	if err := x.reset(t, console, token, changedPassword); err != nil {
		t.Fatal(err)
	}
	if err := x.reset(t, console, token, password); !errors.Is(err, accounts.ErrInvalidReset) {
		t.Fatalf("replay: %v", err)
	}
	if _, err := x.principal(t, console, old); !errors.Is(err, accounts.ErrNoSession) {
		t.Fatalf("old session: %v", err)
	}
	x.checkResetPasswords(t)
	if got := texts(t, x.db, platform, "SELECT action FROM platform.audit_log WHERE action = 'user.password_set'"); len(got) != 1 {
		t.Fatalf("audit: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT subject FROM platform.mail_outbox WHERE subject LIKE '%密码已变更%'"); len(got) != 1 {
		t.Fatalf("notice: %v", got)
	}
}

func TestPasswordResetRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"wrong token", "expired token", "expired request", "other site", "new request", "factor changed", "disabled user"} {
		t.Run(name, func(t *testing.T) { testPasswordResetRefusal(t, name) })
	}
}

func TestPasswordResetConcurrentConsumption(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	token := x.token(t)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; results <- x.reset(t, console, token, changedPassword) }()
	}
	close(start)
	won := 0
	for range 2 {
		err := <-results
		if err == nil {
			won++
		} else if !errors.Is(err, accounts.ErrInvalidReset) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d successful consumers", won)
	}
}

func TestPasswordResetRequestDoesNotLookUpAccounts(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, "REVOKE SELECT ON accounts.users FROM axiom_app"); err != nil {
		t.Fatal(err)
	}
	release, err := x.s.HashTurn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, email := range []string{x.email, "unknown@example.com"} {
		x.requestReset(t, email, "en")
	}
	for _, query := range []string{
		"SELECT count(*)::text FROM accounts.password_reset_requests",
		"SELECT count(*)::text FROM river.river_job WHERE kind = 'accounts.password_reset'",
	} {
		if got := texts(t, x.db, platform, query); got[0] != "2" {
			t.Fatalf("same durable work: %q", got)
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "0" {
		t.Fatal("request issued mail inline")
	}
}

func TestPasswordResetIssuanceAndBrand(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	for _, email := range []string{"unknown@example.com", x.email} {
		id := x.requestReset(t, email, "zh")
		for range 2 {
			if err := x.recovery.ProcessPasswordReset(t.Context(), id); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := texts(t, x.db, platform, "SELECT text_body FROM platform.mail_outbox")
	if len(rows) != 1 || !strings.Contains(rows[0], "Main") || !strings.Contains(rows[0], "https://main.example/reset-password#token=") || !strings.Contains(rows[0], "30 分钟") {
		t.Fatalf("reset mail: %v", rows)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_requests WHERE email IS NOT NULL"); got[0] != "0" {
		t.Fatal("processed address retained")
	}
	if got := texts(t, x.db, platform, "SELECT args::text FROM river.river_job WHERE kind IN ('accounts.password_reset', 'mail.send')"); strings.Contains(strings.Join(got, ""), "@") || strings.Contains(strings.Join(got, ""), "token") {
		t.Fatal("personal data or token in River")
	}
}

func TestPasswordResetLimits(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "cache unavailable"}[shared], func(t *testing.T) { testPasswordResetLimit(t, shared) })
	}
}

func testPasswordResetRefusal(t *testing.T, name string) {
	t.Parallel()
	x := recoverySite(t)
	token, host := x.token(t), console
	token, host = refuseReset(t, x, name, token, host)
	if err := x.reset(t, host, token, changedPassword); !errors.Is(err, accounts.ErrInvalidReset) {
		t.Fatalf("reset: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_tokens WHERE used_at IS NOT NULL"); got[0] != "0" {
		t.Fatal("refusal spent the token")
	}
}

func refuseReset(t *testing.T, x recoveryWorld, name, token, host string) (string, string) {
	t.Helper()
	switch name {
	case "wrong token":
		token = strings.Repeat("x", 43)
	case "expired token":
		if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.password_reset_tokens SET expires_at = now() - interval '1 second'"); err != nil {
			t.Fatal(err)
		}
	case "expired request":
		if _, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_requests SET expires_at = now() - interval '1 second'"); err != nil {
			t.Fatal(err)
		}
	case "other site":
		host = otherConsole
	case "new request":
		x.requestReset(t, x.email, "en")
	case "factor changed":
		if _, err := exec(t, x.db, platform, "UPDATE accounts.users SET factor_version = factor_version + 1"); err != nil {
			t.Fatal(err)
		}
	case "disabled user":
		if _, err := exec(t, x.db, platform, "UPDATE accounts.users SET status = 'disabled'"); err != nil {
			t.Fatal(err)
		}
	}
	return token, host
}

func testPasswordResetLimit(t *testing.T, shared bool) {
	t.Parallel()
	x := recoverySite(t)
	if shared {
		s := cachedNode(t, x.db, deadCache(t, 2, x.clock.Now), x.clock.Now)
		s.UsePasswordRecovery(x.recovery)
		x.s = s
	}
	tries := 10
	if shared {
		tries = 5
	}
	for range tries {
		x.requestReset(t, x.email, "en")
	}
	err := x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{Host: console, Email: " SOMEONE@example.com ", ClientIP: client})
	if !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatalf("email throttle: %v", err)
	}
	ip := netip.MustParseAddr("198.51.100.77")
	for range 21 {
		err = x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{Host: console, Email: uuid.NewV7().String() + "@example.com", ClientIP: ip})
		if errors.Is(err, accounts.ErrTooManyAttempts) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(err, accounts.ErrTooManyAttempts) {
		t.Fatal("IP bucket did not stop a reset flood")
	}
}

func (x recoveryWorld) checkResetPasswords(t *testing.T) {
	t.Helper()
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: changedPassword}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password}); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Fatalf("old password: %v", err)
	}
}

func TestPasswordResetLimitAcrossNodes(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	server, namespace := kvtest.New(t), kvtest.Namespace()
	nodes := []*accounts.Service{
		cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now),
		cachedNode(t, x.db, server.OpenIn(t, namespace, kv.Deps{Now: clientClock(x.clock)}), x.clock.Now),
	}
	for _, node := range nodes {
		node.UsePasswordRecovery(x.recovery)
	}
	for attempt := range 11 {
		err := nodes[attempt%2].RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{
			Host: console, Email: x.email, ClientIP: netip.AddrFrom4([4]byte{198, 51, 100, byte(attempt + 1)}),
		})
		if attempt < 10 && err != nil {
			t.Fatal(err)
		}
		if attempt == 10 && !errors.Is(err, accounts.ErrTooManyAttempts) {
			t.Fatalf("shared email limit: %v", err)
		}
	}
}

func TestPasswordResetDisabledAndUnavailable(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	if err := x.queue.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{x.email, "unknown@example.com"} {
		err := x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{Host: console, Email: email})
		if err == nil {
			t.Fatal("unavailable queue accepted a request")
		}
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_requests"); got[0] != "0" {
		t.Fatal("queue failure left request rows")
	}
	disabled, err := axmail.Register(t.Context(), x.db, x.queue, axmail.Config{}, nil, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// A separate queue avoids registering the recovery kind twice; no queue work is needed while SMTP is disabled.
	queue := x.db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	recovery, err := accounts.RegisterPasswordRecovery(x.db, queue, disabled, platform, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	x.s.UsePasswordRecovery(recovery)
	if err := x.s.RequestPasswordReset(t.Context(), accounts.PasswordResetRequest{Host: console, Email: x.email}); !errors.Is(err, accounts.ErrBusy) {
		t.Fatalf("disabled SMTP: %v", err)
	}
}

func TestPasswordResetWorkerRejectsStaleRequests(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	old := x.requestReset(t, x.email, "en")
	latest := x.requestReset(t, x.email, "en")
	if err := x.recovery.ProcessPasswordReset(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.password_reset_tokens"); got[0] != "0" {
		t.Fatal("superseded task issued a token")
	}
	if _, err := exec(t, x.db, platform, "UPDATE accounts.password_reset_requests SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := x.recovery.ProcessPasswordReset(t.Context(), latest); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "0" {
		t.Fatal("expired request sent mail")
	}
}
