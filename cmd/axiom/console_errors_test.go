package main

import (
	"fmt"
	"net/http"
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
