package sub2api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// tokenAnswer is a login or refresh answer with the given tokens.
func tokenAnswer(access, refresh string) adapter.Response {
	return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"message":"success","data":{"access_token":%q,"refresh_token":%q,"expires_in":86400,"token_type":"Bearer"}}`, access, refresh))}
}

func (s *server) count(prefix string) (n int) {
	for _, p := range s.paths() {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// TestLoginCodecRoundTripsAndRedacts: the stored form round-trips, refuses members it does not know, and the Login
// never prints a secret.
func TestLoginCodecRoundTripsAndRedacts(t *testing.T) {
	t.Parallel()
	l := Login{
		Email: "owner@example.test", Password: secret.New("PASSWORD_PLACEHOLDER"), Access: secret.New("ACCESS_PLACEHOLDER"),
		Refresh: secret.New("REFRESH_PLACEHOLDER"), AccessExpires: epoch, Lifetime: time.Hour,
	}
	b, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLogin(b)
	if err != nil || got.Email != l.Email || got.Password.Reveal() != "PASSWORD_PLACEHOLDER" || got.Access.Reveal() != "ACCESS_PLACEHOLDER" ||
		got.Refresh.Reveal() != "REFRESH_PLACEHOLDER" || !got.AccessExpires.Equal(epoch) || got.Lifetime != time.Hour {
		t.Errorf("round trip = %+v, %v", got, err)
	}
	for _, shown := range []string{fmt.Sprintf("%v", l), fmt.Sprintf("%+v", l), fmt.Sprintf("%#v", l)} {
		if strings.Contains(shown, "_PLACEHOLDER") {
			t.Errorf("a Login printed a secret: %s", shown)
		}
	}
	if _, err := DecodeLogin([]byte(`{"v":1,"email":"a","extra":1}`)); err == nil {
		t.Error("a member the version does not know was ignored")
	}
}

// TestExpiringTokenIsRefreshedAndStored: in the last quarter of its life the access token is replaced by a refresh, the
// new pair is stored over the version that was read, and no login with the password happens.
func TestExpiringTokenIsRefreshedAndStored(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		switch r.Path {
		case "/api/v1/auth/refresh":
			return tokenAnswer("ACCESS_NEW", "REFRESH_NEW"), nil
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer ACCESS_NEW" {
				return adapter.Response{Status: 401, Body: []byte(`{"code":"TOKEN_EXPIRED"}`)}, nil
			}
			return fixture(t, "me"), nil
		}
		return fixture(t, "subscriptions_active"), nil
	})
	st := login(t, epoch.Add(5*time.Hour)) // 5h left of 24h: inside the last quarter
	m := manager0(t, srv, st)
	if _, err := m.Account(t.Context()); err != nil {
		t.Fatal(err)
	}
	l := st.current(t)
	if st.swaps != 1 || l.Access.Reveal() != "ACCESS_NEW" || l.Refresh.Reveal() != "REFRESH_NEW" || !l.AccessExpires.Equal(epoch.Add(24*time.Hour)) {
		t.Errorf("stored login after refresh: %d swaps, expires %v", st.swaps, l.AccessExpires)
	}
	if srv.count("POST /api/v1/auth/login") != 0 {
		t.Error("the password was used although the refresh worked")
	}
	if srv.count("POST /api/v1/auth/refresh") != 1 {
		t.Errorf("refreshed %d times, want once: %v", srv.count("POST /api/v1/auth/refresh"), srv.paths())
	}
}

// TestRefusedRefreshTokenFallsBackToThePassword: a refresh token used before is refused (they work once), and the stored
// password logs in again.
func TestRefusedRefreshTokenFallsBackToThePassword(t *testing.T) {
	t.Parallel()
	srv := routes(map[string]adapter.Response{
		"POST /api/v1/auth/refresh": fixture(t, "refresh_reused"),
		"POST /api/v1/auth/login":   tokenAnswer("ACCESS_LOGIN", "REFRESH_LOGIN"),
	})
	st := login(t, epoch.Add(time.Hour))
	manager0(t, srv, st)
	if l := st.current(t); l.Access.Reveal() != "ACCESS_LOGIN" || l.Password.Reveal() != "PASSWORD_PLACEHOLDER" {
		t.Errorf("after the fallback the stored login is %+v", l)
	}
	if !slicesEqual(srv.paths(), []string{"POST /api/v1/auth/refresh", "POST /api/v1/auth/login"}) {
		t.Errorf("requests = %v", srv.paths())
	}
}

// TestRejectedAccountStopsAtOnce: a wrong password is an auth rejection, and so is a revoked token in the middle of a
// call; neither is followed by another login, which would only risk the account.
func TestRejectedAccountStopsAtOnce(t *testing.T) {
	t.Parallel()
	t.Run("the password is refused", func(t *testing.T) {
		srv := routes(map[string]adapter.Response{
			"POST /api/v1/auth/refresh": fixture(t, "refresh_reused"),
			"POST /api/v1/auth/login":   fixture(t, "login_wrong_password"),
		})
		_, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, login(t, epoch.Add(time.Hour)))
		if !errors.Is(err, adapter.ErrAuthRejected) {
			t.Fatalf("Open = %v, want ErrAuthRejected", err)
		}
		if srv.count("POST /api/v1/auth/login") != 1 {
			t.Errorf("tried the password %d times", srv.count("POST /api/v1/auth/login"))
		}
	})
	for _, code := range []string{"TOKEN_REVOKED", "USER_INACTIVE", "SESSION_BINDING_MISMATCH"} {
		t.Run(code, func(t *testing.T) {
			srv := newServer(func(adapter.Request) (adapter.Response, error) {
				return adapter.Response{Status: 401, Body: []byte(`{"code":"` + code + `"}`)}, nil
			})
			m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
			if _, err := m.Account(t.Context()); !errors.Is(err, adapter.ErrAuthRejected) {
				t.Fatalf("Account = %v, want ErrAuthRejected", err)
			}
			if srv.count("POST") != 0 || len(srv.requests) != 1 {
				t.Errorf("after %s the adapter sent %v", code, srv.paths())
			}
		})
	}
}

// TestExpiredTokenInTheMiddleIsRenewedOnce: an access token that ran out between checks is renewed and the call is sent
// again, once; a second refusal is the end.
func TestExpiredTokenInTheMiddleIsRenewedOnce(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		switch r.Path {
		case "/api/v1/auth/refresh":
			return tokenAnswer("ACCESS_NEW", "REFRESH_NEW"), nil
		case "/api/v1/auth/me":
			return adapter.Response{Status: 401, Body: []byte(`{"code":"TOKEN_EXPIRED"}`)}, nil
		}
		return adapter.Response{}, errors.New("unexpected " + r.Path)
	})
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	if _, err := m.Account(t.Context()); !errors.Is(err, adapter.ErrAuthRejected) {
		t.Fatalf("Account = %v, want ErrAuthRejected after the renewed token was refused", err)
	}
	if got := srv.count("GET /api/v1/auth/me"); got != 2 {
		t.Errorf("sent the call %d times, want 2", got)
	}
	if srv.count("POST /api/v1/auth/refresh") != 1 {
		t.Error("renewed more than once")
	}
}

// TestStaleSwapUsesTheOtherHoldersLogin: when someone else stored a newer login first, theirs is used and no further
// request is made with ours.
func TestStaleSwapUsesTheOtherHoldersLogin(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if r.Path == "/api/v1/auth/refresh" {
			return tokenAnswer("ACCESS_MINE", "REFRESH_MINE"), nil
		}
		return adapter.Response{}, errors.New("unexpected " + r.Path)
	})
	st := login(t, epoch.Add(time.Hour))
	theirs := Login{
		Email: "owner@example.test", Password: secret.New("PASSWORD_PLACEHOLDER"), Access: secret.New("ACCESS_THEIRS"),
		Refresh: secret.New("REFRESH_THEIRS"), AccessExpires: epoch.Add(24 * time.Hour), Lifetime: 24 * time.Hour,
	}
	var err error
	if st.other, err = theirs.Encode(); err != nil {
		t.Fatal(err)
	}
	st.stale = true
	m := manager0(t, srv, st)
	if got := m.(*manager).s.login.Access.Reveal(); got != "ACCESS_THEIRS" {
		t.Errorf("the session uses %q after losing the swap", got)
	}
}

// TestAmbiguousRefreshFailureDoesNotLogInAgain: a refresh that fails without an answer (network) is an error for the
// round; the password is not used, because the token may or may not have been spent.
func TestAmbiguousRefreshFailureDoesNotLogInAgain(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) { return adapter.Response{}, errors.New("timeout") })
	_, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, login(t, epoch.Add(time.Hour)))
	if err == nil || errors.Is(err, adapter.ErrAuthRejected) || srv.count("POST /api/v1/auth/login") != 0 {
		t.Errorf("Open = %v after %v", err, srv.paths())
	}
}

// TestRateLimitedAnswerBlocksTheBuckets: a 429 is the caller's to wait for, with the time the supplier named.
func TestRateLimitedAnswerBlocksTheBuckets(t *testing.T) {
	t.Parallel()
	limited := fixture(t, "login_wrong_password")
	limited.Status, limited.Header = 429, map[string][]string{"Retry-After": {"59"}}
	srv := routes(map[string]adapter.Response{"POST /api/v1/auth/refresh": limited})
	_, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, login(t, epoch.Add(time.Hour)))
	var blocked adapter.Blocked
	if !errors.As(err, &blocked) || !blocked.Until.Equal(epoch.Add(59*time.Second)) || !errors.Is(err, adapter.ErrBucketsFull) {
		t.Errorf("Open = %v, want a block until %v", err, epoch.Add(59*time.Second))
	}
}

// TestFailuresCarryNoSupplierText: errors on the failure paths name codes and statuses and never quote the body, even
// when the supplier echoes a credential in it.
func TestFailuresCarryNoSupplierText(t *testing.T) {
	t.Parallel()
	leaks := []string{"sk-leaked-credential-0123456789", "hunter2-password"}
	echo := adapter.Response{Status: 500, Body: []byte(`{"message":"bad ` + leaks[0] + ` ` + leaks[1] + `"`)} // truncated JSON
	for name, resp := range map[string]adapter.Response{
		"500": echo, "200 truncated": {Status: 200, Body: echo.Body}, "200 other shape": {Status: 200, Body: []byte(`{"code":0,"data":"` + leaks[0] + `"}`)},
	} {
		srv := newServer(func(adapter.Request) (adapter.Response, error) { return resp, nil })
		m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
		for _, call := range []func() error{
			func() error { _, err := m.Account(t.Context()); return err },
			func() error { _, err := m.Keys(t.Context()); return err },
			func() error { _, err := m.Offer(t.Context()); return err },
			func() error { _, _, err := m.Charges(t.Context(), adapter.BeginScan(epoch, epoch)); return err },
		} {
			err := call()
			if err == nil {
				t.Errorf("%s: a bad answer was accepted", name)
				continue
			}
			for _, leak := range leaks {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("%s: the error quotes the supplier: %v", name, err)
				}
			}
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
