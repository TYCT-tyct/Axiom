package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
)

// TestTooManyKeysIsAnswered: the keys an account holds are a 409 that names the limit and the count; the keys it made
// in the last hour are a 429 with the whole seconds until the next one, rounded up.
func TestTooManyKeysIsAnswered(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		err  error
		want apiError
	}{
		"the cap on the keys held": {
			&accounts.TooManyKeys{Limit: 100, Count: 100},
			apiError{status: http.StatusConflict, code: codeTooManyKeys, message: "the account holds 100 keys, and the site allows 100"},
		},
		"the keys made in the hour": {
			fmt.Errorf("create an API key: %w", &accounts.TooManyKeys{Limit: 50, Count: 50, RetryAfter: 90*time.Second + time.Millisecond}),
			apiError{
				status: http.StatusTooManyRequests, code: codeRateLimited, retryAfter: 91,
				message: "the account made 50 keys in the last hour, and the site allows 50",
			},
		},
	} {
		got := failure(c.err)
		if got == nil {
			t.Errorf("%s: not a refusal", name)
			continue
		}
		if diff := cmp.Diff(c.want, *got, cmp.AllowUnexported(apiError{})); diff != "" {
			t.Errorf("%s (-want +got):\n%s", name, diff)
		}
	}
}

// TestPasskeyMFALimitResponses: a session out of attempts is a 401 that sends the page back to sign-in, while a
// source-address limit is a 429 with the wait the contract requires. Waiting cannot restore the session budget.
func TestPasskeyMFALimitResponses(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err        error
		status     int
		code       errorCode
		retryAfter string
	}{
		"session attempts exhausted": {
			err: accounts.ErrMFAExhausted, status: http.StatusUnauthorized, code: codeUnauthenticated,
		},
		"source address limited": {
			err:    fmt.Errorf("verify the passkey: %w", &accounts.TooManyAttempts{RetryAfter: 90*time.Second + time.Millisecond}),
			status: http.StatusTooManyRequests, code: codeRateLimited, retryAfter: "91",
		},
	}
	logger := slog.New(slog.DiscardHandler)
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			respond(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/passkeys/mfa/finish", nil), logger, c.err)
			if rec.Code != c.status {
				t.Errorf("status = %d, want %d", rec.Code, c.status)
			}
			if got := rec.Header().Get("Retry-After"); got != c.retryAfter {
				t.Errorf("Retry-After = %q, want %q", got, c.retryAfter)
			}
			if body := errorOf(t, rec); string(body.Code) != string(c.code) {
				t.Errorf("code = %s, want %s", body.Code, c.code)
			}
		})
	}
}
