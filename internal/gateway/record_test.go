package gateway

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"axiom/internal/observability"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
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

// TestAttributionOf: the three app-attribution headers are kept as the client's name for its application, cleaned and
// bounded; a value that cannot be stored never reaches the column that way.
func TestAttributionOf(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    appAttribution
	}{
		{"nothing named", nil, appAttribution{}},
		{
			"all three",
			map[string]string{
				"HTTP-Referer": "https://app.example.com/chat", "X-OpenRouter-Title": "Example Chat",
				"X-OpenRouter-Categories": "cli-agent,cloud-agent",
			},
			appAttribution{"https://app.example.com/chat", "Example Chat", "cli-agent,cloud-agent"},
		},
		{
			"trimmed",
			map[string]string{
				"HTTP-Referer": "  https://app.example.com  ", "X-OpenRouter-Title": "\tExample Chat\n",
			},
			appAttribution{referer: "https://app.example.com", title: "Example Chat"},
		},
		{"the older title name", map[string]string{"X-Title": "Old Name"}, appAttribution{title: "Old Name"}},
		{
			"the new title name wins",
			map[string]string{"X-Title": "Old Name", "X-OpenRouter-Title": "New Name"},
			appAttribution{title: "New Name"},
		},
		{"blank new title falls back", map[string]string{"X-Title": "Old Name", "X-OpenRouter-Title": "  "}, appAttribution{title: "Old Name"}},
		{
			"control characters and a NUL",
			map[string]string{"X-OpenRouter-Title": "a\x00b\nc\td"},
			appAttribution{title: "a b c d"},
		},
		{"bytes that are not text", map[string]string{"X-OpenRouter-Title": "bad\xfftext"}, appAttribution{title: "bad\uFFFDtext"}},
		{
			"categories in the OpenRouter spelling",
			map[string]string{"X-OpenRouter-Categories": " CLI-Agent, cloud-agent ,not_a_category,third"},
			appAttribution{categories: "cli-agent,cloud-agent"},
		},
		{
			"categories with a bad hyphen",
			map[string]string{"X-OpenRouter-Categories": "-cli,--agent,cli-,-,tool"},
			appAttribution{categories: "tool"},
		},
		{
			"categories beyond two",
			map[string]string{"X-OpenRouter-Categories": "cli-agent,cloud-agent,image-gen"},
			appAttribution{categories: "cli-agent,cloud-agent"},
		},
		{
			"a category longer than 30",
			map[string]string{"X-OpenRouter-Categories": strings.Repeat("a", 31) + ",ok"},
			appAttribution{categories: "ok"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for name, value := range tc.headers {
				h.Set(name, value)
			}
			if got := attributionOf(h); got != tc.want {
				t.Errorf("attribution %+v, want %+v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		header string
		limit  int
	}{
		{"the referer", "HTTP-Referer", maxAppReferer},
		{"the title", "X-OpenRouter-Title", maxAppTitle},
	} {
		t.Run(tc.name+" is cut at its width", func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			h.Set(tc.header, strings.Repeat("界", tc.limit+100))
			got := attributionOf(h)
			value := got.referer
			if tc.header != "HTTP-Referer" {
				value = got.title
			}
			if n := utf8.RuneCountInString(value); n != tc.limit || !utf8.ValidString(value) {
				t.Errorf("%d runes, valid %v; want %d valid runes", n, utf8.ValidString(value), tc.limit)
			}
		})
	}
}

// TestAttributionHeadersAreRecorded: a call's attribution headers are stored on its call record, and none of them is
// sent upstream.
func TestAttributionHeadersAreRecorded(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	r.exec.Records = observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		QueueSize: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	a := r.funded(1e12)
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 1, 10, false)}
	}
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")),
		"HTTP-Referer", " https://app.example.com/chat ",
		"X-OpenRouter-Title", " Example Chat ",
		"X-OpenRouter-Categories", "CLI-Agent, cloud-agent, third",
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if err := r.exec.Records.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var referer, title, categories string
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(),
			`SELECT app_referer, app_title, app_categories FROM observability.call_log`).Scan(&referer, &title, &categories)
	}); err != nil {
		t.Fatal(err)
	}
	if want := (appAttribution{"https://app.example.com/chat", "Example Chat", "cli-agent,cloud-agent"}); (appAttribution{referer, title, categories}) != want {
		t.Errorf("recorded (%q, %q, %q), want %+v", referer, title, categories, want)
	}
	assertAttributionNotForwarded(t, up)
}

// assertAttributionNotForwarded: the upstream saw the one call, and none of the client's attribution headers.
func assertAttributionNotForwarded(t *testing.T, up *fakeUpstreams) {
	t.Helper()
	sent := 0
	for _, e := range up.eps {
		for _, call := range e.calls {
			sent++
			for _, name := range []string{"HTTP-Referer", "X-OpenRouter-Title", "X-OpenRouter-Categories"} {
				if got := call.Header.Get(name); got != "" {
					t.Errorf("%s reached the upstream as %q", name, got)
				}
			}
		}
	}
	if sent != 1 {
		t.Errorf("%d calls reached the upstream, want 1", sent)
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
