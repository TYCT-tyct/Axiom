package gateway

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/observability"
)

// TestClientAppOf: the clients the log names are recognised by their User-Agent whatever its case, the rest are left
// unnamed rather than guessed.
func TestClientAppOf(t *testing.T) {
	t.Parallel()
	for agent, want := range map[string]string{
		"claude-cli/2.0.5 (external, cli)": "claude-code",
		"Claude Code/1.2":                  "claude-code",
		"codex_cli_rs/0.50.0":              "codex",
		"Codex":                            "codex",
		"python-requests/2.32":             "",
		"":                                 "",
	} {
		h := http.Header{}
		h.Set("User-Agent", agent)
		if got := clientAppOf(h); got != want {
			t.Errorf("User-Agent %q is %q, want %q", agent, got, want)
		}
	}
}

// TestACallWithNoAttemptIsRecorded: a call that is admitted but finds every credential taken makes no attempt, so
// nothing binds a price to it; its record still carries the site's currency, which the call log's CHECK demands, and is
// written rather than dropped as a bad row.
func TestACallWithNoAttemptIsRecorded(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	for _, e := range up.eps {
		e.noSlot = true
	}
	r.exec.Records = observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		QueueSize: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	a := r.funded(1e12)
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if err := r.exec.Records.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM observability.call_log WHERE currency = 'USD' AND attempt_count = 0"); n != 1 {
		t.Errorf("%d records of the call, want the one in the site's currency", n)
	}
}
