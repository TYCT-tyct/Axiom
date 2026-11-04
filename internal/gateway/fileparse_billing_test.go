package gateway

import (
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/websearch"
)

// searchParsePrice prices the two per-use items and nothing else: with token prices at zero, the settled charge is
// exactly the work the gateway did itself, so a test reads the tool bill without re-deriving the upstream's usage.
const searchParsePrice = `{"v":1,"currency":"USD","rates":{"input":"0","output":"0","cache_write":"0","image_in":"0",` +
	`"web_search":"0.01","file_parse":"0.02"},"service_tiers":{"priority":{"rates":{"input":"0","output":"0",` +
	`"cache_write":"0","image_in":"0","web_search":"0.01","file_parse":"0.02"}}}}`

// searchFileBody is fileBody with one search path added: the web plugin, or the server-side tool the loop runs.
func searchFileBody(engine string, tool bool) string {
	body := strings.Replace(string(fileBody(engine)), `"stream":false`, `"stream":true`, 1)
	if tool {
		return strings.Replace(body, `"plugins":[`,
			`"tools":[{"type":"openrouter:web_search","parameters":{"engine":"test","max_results":2}}],"plugins":[`, 1)
	}
	return strings.Replace(body, `"plugins":[`, `"plugins":[{"id":"web","engine":"test","max_results":2},`, 1)
}

type searchParseCase struct {
	name         string
	tool         bool
	engine       string
	pages        int
	wantSearches int64
	wantPages    int64
}

// TestSearchAndParseBillingSurviveTheRewrite: both search paths rewrite the body after parsing the file, so the
// re-inspection cannot see FileBilling or FilePages. The settlement still bills the search and the parse in their own
// units: a free parse adds no search, and a multi-page OCR keeps one file_parse per page. Both the final usage and the
// ledger are checked, because admission alone is what the earlier combination tests covered.
func TestSearchAndParseBillingSurviveTheRewrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []searchParseCase{
		{name: "the web plugin with a free parse", engine: "cloudflare-ai", pages: 1, wantSearches: 1},
		{name: "the web plugin with a multi-page OCR", engine: "mistral-ocr", pages: 3, wantSearches: 1, wantPages: 3},
		{name: "the server tool with a free parse", tool: true, engine: "cloudflare-ai", pages: 1, wantSearches: 1},
		{name: "the server tool with a multi-page OCR", tool: true, engine: "mistral-ocr", pages: 3, wantSearches: 1, wantPages: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkSearchParseCase(t, tc)
		})
	}
}

// checkSearchParseCase runs one call through the search path and parse engine the case names, then reads its bill.
func checkSearchParseCase(t *testing.T, tc searchParseCase) {
	t.Helper()
	parser := &recordingParser{text: "The parsed page.", pages: tc.pages}
	searcher := &recordingSearcher{results: []websearch.Result{{
		URL: "https://example.com/one", Title: "Example One", Content: "The first result.",
	}}}
	r := newRig(t, func(o *rigOptions) {
		o.spec, o.parser, o.price = parseSpec, parser, searchParsePrice
		o.search = searcher
	})
	up := withCosts(r, r.billing, fakeCosts{cost: 1000, known: true})
	script := []scripted{serve(beat{data: chatAnswer})}
	if tc.tool {
		// The loop's first sampling asks for a search; the second answers with what it found.
		script = []scripted{serve(beat{data: chatToolCall}), serve(beat{data: chatAnswer})}
	}
	for _, e := range up.eps {
		e.script = script
	}
	a := r.funded(1e12)
	resp, body := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(searchFileBody(tc.engine, tc.tool)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if len(searcher.queries) != 1 || len(parser.docs) != 1 {
		t.Fatalf("searches = %d, parses = %d, want one of each", len(searcher.queries), len(parser.docs))
	}
	calls := r.calls()
	if len(calls) != 1 {
		t.Fatalf("admitted %d calls, want one", len(calls))
	}
	checkSearchParseBilling(t, r, calls[0].ID, tc.wantSearches, tc.wantPages)
	r.idle(1 << 30)
}

// checkSearchParseBilling reads the settled usage and the ledger: the two per-use counts the call was charged for, the
// charge the reservation records, and the user-balance entries the settlement wrote, which must be one settlement of
// that same amount. The rates are one cent a search and two a page.
func checkSearchParseBilling(t *testing.T, r *rig, id uuid.UUID, searches, pages int64) {
	t.Helper()
	charged := searches*10_000_000 + pages*20_000_000
	const settled = " FROM billing.reservations WHERE request_id = $1 AND state = 'settled'"
	if got := r.count("SELECT coalesce((final_usage->0->'counts'->>10)::bigint, 0)"+settled, id); got != searches {
		t.Errorf("settled searches = %d, want %d", got, searches)
	}
	if got := r.count("SELECT coalesce((final_usage->0->'counts'->>19)::bigint, 0)"+settled, id); got != pages {
		t.Errorf("settled file pages = %d, want %d", got, pages)
	}
	if got := r.count("SELECT charged"+settled, id); got != charged {
		t.Errorf("the reservation charged %d, want %d", got, charged)
	}
	if n := r.count("SELECT count(*) FROM billing.transactions WHERE request_id = $1 AND kind = 'settle'", id); n != 1 {
		t.Fatalf("the call settled %d times, want once", n)
	}
	if got := r.count("SELECT coalesce(sum(e.amount), 0) FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id "+
		"WHERE t.request_id = $1 AND t.kind = 'settle' AND e.book = 'user_balance'", id); got != charged {
		t.Errorf("the ledger charged %d, want %d", got, charged)
	}
}
