package gateway

import (
	"context"
	"encoding/json" //nolint:depguard // a tool call's small arguments object
	"errors"
	"fmt"
	"math"
	"strings"

	"axiom/internal/billing"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
)

// serverLoop is one openrouter:web_search request's model-side loop: the backend it searches through, the caps the
// client set, and how much of each was used. It lives on the session, so its caps hold for the whole call; each
// sampling is an attempt of its own, and billing charges the loop's samplings together (docs/domains/billing.md
// 「预留与结算」).
type serverLoop struct {
	backend *websearch.Backend
	tool    *protocol.ServerWebSearch
	cap     int   // the most tool calls, one step each
	steps   int   // the calls seen
	results int64 // the results returned so far, for max_total_results
	bytes   int   // the result text appended, for ServerResultBudget
	// disabled says the loop must answer with what it has: a cap was reached. The next hop declares no function.
	disabled bool
}

func newServerLoop(c *Call) *serverLoop {
	t := c.Request.ServerSearch
	cap := int(t.MaxUses)
	if cap == 0 {
		cap = protocol.DefaultServerToolCalls
	}
	if c.Request.HasMaxToolCalls {
		cap = min(cap, int(c.Request.MaxToolCalls))
	}
	return &serverLoop{backend: c.searchBackend, tool: t, cap: cap}
}

// The tool results that tell the model why a call ran no search, so it can answer or call again.
const (
	stepLimitResult   = "The web search step limit for this request was reached. Answer with the results you already have."
	noQueryResult     = "The web search call did not name a query. Call it again with a query, or answer without a search."
	longQueryResult   = "The search query is longer than this search engine accepts. Try a shorter one."
	resultLimitResult = "The result limit for this request was reached. Answer with the results you already have."
)

// search runs the searches of one tool-call step and appends the model's calls and their results to the conversation.
// A backend failure ends the call before anything is written: the model usage of the hops so far is waived as
// undelivered, while the searches that did run stay charged (docs/domains/billing.md「免单规则」).
func (a *attempt) search(ctx context.Context, turn protocol.ServerTurn) *ended {
	loop := a.s.loop
	results := make([]string, 0, len(turn.Calls))
	for _, call := range turn.Calls {
		if call.Client {
			continue
		}
		if e := a.stopped(ctx); e != nil {
			return e
		}
		text, e := a.searchCall(ctx, call)
		if e != nil {
			return e
		}
		results = append(results, text)
	}
	if loop.steps >= loop.cap {
		loop.disabled = true
	}
	if e := a.stopped(ctx); e != nil {
		return e // the searches that ran stay charged; nothing is sent after the attempt ended
	}
	return a.appendTurn(turn, results)
}

// searchCall runs one tool call's search and returns the tool result the model reads: the results, or why there are
// none. A cap the call reaches disables the loop, so the next hop declares no function.
func (a *attempt) searchCall(ctx context.Context, call protocol.ServerToolCall) (string, *ended) {
	loop := a.s.loop
	if loop.disabled || loop.steps >= loop.cap {
		loop.disabled = true
		return stepLimitResult, nil
	}
	loop.steps++
	query := searchQuery(call.Arguments)
	switch {
	case query == "":
		return noQueryResult, nil
	case len(query) > protocol.MaxWebSearchQueryBytes:
		return longQueryResult, nil
	}
	limit, room, ok := loop.room()
	if !ok {
		loop.disabled = true
		return resultLimitResult, nil
	}
	return a.runSearch(ctx, query, limit, room)
}

// room is what the next search may add: at most limit results, the per-search cap within what max_total_results leaves,
// in at most room bytes of ServerResultBudget. ok is false once either total is spent.
func (l *serverLoop) room() (limit int64, room int, ok bool) {
	limit = int64(protocol.MaxServerResults)
	if l.tool.MaxResults > 0 {
		limit = int64(l.tool.MaxResults)
	}
	if l.tool.MaxTotalResults > 0 {
		left := l.tool.MaxTotalResults - l.results
		if left <= 0 {
			return 0, 0, false
		}
		limit = min(limit, left)
	}
	room = min(protocol.MaxServerResultBytes, protocol.ServerResultBudget-l.bytes)
	return limit, room, room > 0
}

// runSearch asks the backend for one query and returns its results as the tool result, counted against the loop's
// caps. A backend failure ends the call, unless it was the attempt or the client ending that cut the search.
func (a *attempt) runSearch(ctx context.Context, query string, limit int64, room int) (string, *ended) {
	s, loop := a.s, a.s.loop
	if !s.reserveSearch(websearch.WorkingSet) {
		return "", a.abort(protocol.Error{Type: protocol.Overloaded}, 1)
	}
	// The search is new paid work: the attempt's end (the call's deadline, the node's shutdown) stops it, and so does
	// the client leaving, even when the endpoint's policy kept the first sampling reading after it.
	searchCtx, cancel := context.WithTimeout(ctx, websearch.Timeout)
	stop := context.AfterFunc(s.req.Context(), cancel) //nolint:contextcheck // the client leaving ends the search; the search never ends the client
	found, err := loop.backend.Search(searchCtx, websearch.Query{
		Text: query, MaxResults: int(limit),
		IncludeDomains: loop.tool.IncludeDomains, ExcludeDomains: loop.tool.ExcludeDomains,
	})
	stop()
	cancel()
	if err != nil {
		if e := a.stopped(ctx); e != nil {
			return "", e // cut by the attempt's end or the client, not the backend's failure
		}
		if errors.Is(err, websearch.ErrQueryTooLong) {
			return longQueryResult, nil
		}
		s.logger.WarnContext(ctx, "the web search backend failed in the model-side loop", "request_id", s.c.ID.String(), "err", err)
		return "", a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	s.searches++
	s.searched = true
	text, n := searchResults(query, found, room, int(limit))
	loop.took(n, len(text))
	s.cite(found)
	return text, nil
}

// took counts a search's results and bytes against the loop's totals, and disables the loop once either is spent.
func (l *serverLoop) took(results, bytes int) {
	l.results += int64(results)
	l.bytes += bytes
	if l.tool.MaxTotalResults > 0 && l.results >= l.tool.MaxTotalResults || l.bytes >= protocol.ServerResultBudget {
		l.disabled = true
	}
}

// cite keeps a search's results as the citations an aggregated Chat reply carries, at most 32 for the call.
func (s *session) cite(found []websearch.Result) {
	for _, r := range found {
		if len(s.citations) >= 32 {
			return
		}
		s.citations = append(s.citations, protocol.Citation{URL: r.URL, Title: r.Title, Content: r.Content})
	}
}

// appendTurn makes the next sampling's request: the conversation with the model's turn replayed and the tool results
// after it, read again as the request it now is.
func (a *attempt) appendTurn(turn protocol.ServerTurn, results []string) *ended {
	s, loop := a.s, a.s.loop
	// AppendServerTurn copies the whole conversation with the replayed turn, already JSON, and the results, which a JSON
	// escape writes in at most six bytes a byte (\u00XX); Inspect reads the copy. Both are charged before they allocate.
	added := len(turn.Assistant)
	for _, r := range results {
		added += 6 * len(r)
	}
	held := 2*int64(len(s.c.Body)+added) + 8<<10
	if !s.reserveSearch(held) {
		return a.abort(protocol.Error{Type: protocol.Overloaded}, 1)
	}
	body, err := protocol.AppendServerTurn(s.c.Format, s.c.Body, turn, results)
	if err != nil {
		s.logger.Error("the web search turn could not be appended", "request_id", s.c.ID.String(), "err", err)
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	req, err := protocol.Inspect(s.c.Format, body)
	if err != nil {
		s.logger.Error("the appended web search body is invalid", "request_id", s.c.ID.String(), "err", err)
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	// The tool's declaration left the body before admission; what the gateway read of it is restored, so the quote
	// and the next hop see the same request.
	req.ServerSearch = s.c.Request.ServerSearch
	req.NativeWeb = s.c.Request.NativeWeb
	req.MaxToolCalls, req.HasMaxToolCalls = s.c.Request.MaxToolCalls, s.c.Request.HasMaxToolCalls
	req.WebSearches = s.c.Request.WebSearches
	// The admission's own fields are not in the body and would be lost with the new Inspect: the tier it resolved,
	// the files it parsed and the switch that bills them, and the input bound a search plugin reserved. The loop's own
	// input is what Inspect just read. The parse's fields come from the session, which copied them before any rewrite.
	req.ServiceTiers = s.c.Request.ServiceTiers
	restoreFile(&req, s.c.Request.File, s.parses, s.pages, s.fileBilling)
	req.SearchBytes = s.c.Request.SearchBytes
	req.Needs |= protocol.WebSearch | protocol.Tools
	s.c.Body, s.c.Request = body, req
	s.serverFunction = !loop.disabled
	return nil
}

// stopped is how the loop ends when its attempt or its client did, nil while both go on: a client that left gets
// nothing, a call out of time or a node going away the timeout, and an extension refused its billing error.
func (a *attempt) stopped(ctx context.Context) *ended {
	switch cause := context.Cause(ctx); {
	case a.s.isGone():
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	case ctx.Err() == nil:
		return nil
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, errShutdown):
		return a.abort(protocol.Error{Type: protocol.Timeout}, 0)
	default:
		if failed, ok := errors.AsType[*extendFailed](cause); ok {
			e := cut(failed.err)
			return a.abort(e.err, e.after)
		}
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
}

// searchQuery is the query a tool call asked for; an arguments object this gateway cannot read is no query.
func searchQuery(arguments string) string {
	var call struct {
		Query string `json:"query"`
	}
	if json.Unmarshal([]byte(arguments), &call) != nil {
		return ""
	}
	return strings.TrimSpace(call.Query)
}

// searchResults formats one search's results as the tool result the model reads, within room bytes and limit results.
// It returns how many results it carried, which max_total_results counts.
func searchResults(query string, found []websearch.Result, room, limit int) (string, int) {
	if len(found) == 0 {
		return fmt.Sprintf("No results were found for %q.", query), 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n\n", query)
	n := 0
	for i, r := range found {
		if n >= limit {
			break
		}
		entry := fmt.Sprintf("[%d] %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
		if b.Len()+len(entry) > room {
			break
		}
		b.WriteString(entry)
		n++
	}
	if n == 0 {
		return fmt.Sprintf("No usable results were found for %q.", query), 0
	}
	return strings.TrimSpace(b.String()), n
}

// abort ends a loop after a failure: nothing was delivered, so the hops' model usage is waived while the searches
// already run are charged; a client that left is not waived (docs/domains/billing.md「免单规则」).
func (a *attempt) abort(e protocol.Error, after int) *ended {
	s := a.s
	s.dropOut()
	rec := &s.ends[a.i]
	rec.Result, rec.Finish = billing.FailedBeforeCommit, billing.FinishError
	if s.isGone() {
		rec.Finish = billing.FinishCancelled
	}
	s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
	return &ended{err: e, after: after}
}

// holdOut keeps the client-format events of a hop that may not be the answer. What is charged to the node's budget
// is the buffer's capacity, charged before it grows, until the call ends or the hop turns out to be a tool call.
func (s *session) holdOut(out []byte) bool {
	need := len(s.out) + len(out)
	if need <= cap(s.out) {
		s.out = append(s.out, out...)
		return true
	}
	size := max(need, 2*cap(s.out))
	if !s.x.Memory.Charge(int64(size - cap(s.out))) {
		return false
	}
	s.outHeld += int64(size - cap(s.out))
	s.out = append(append(make([]byte, 0, size), s.out...), out...)
	return true
}

// dropOut discards the held events of a tool-call hop and gives their budget back, with the buffer: a buffer kept for
// the next hop would still hold the memory the budget no longer counts.
func (s *session) dropOut() {
	s.x.Memory.Refund(s.outHeld)
	s.outHeld, s.out = 0, nil
}

// hopCap is the output limit of one sampling: each hop is its own upstream call under the request's max output. The
// meter's cap covers every sampling together, which is what the estimate at settlement uses.
func (a *attempt) hopCap() int64 {
	out := a.entry.Target.MaxOutput
	if out <= 0 {
		return a.meter.OutputCap()
	}
	n := max(a.s.c.Request.Choices, 1)
	if out > math.MaxInt64/n {
		return math.MaxInt64
	}
	return out * n
}

// hopDone keeps one sampling's usage as its own attempt's billing segments, estimated under the sampling's own output
// limit, and tells the meter the sampling is over, so the next one's progress adds to it.
func (a *attempt) hopDone(relay *protocol.Relay) {
	if a.hopRecorded {
		return
	}
	a.hopRecorded = true
	a.seen, a.usage = relay.Progress(), relay.Usage()
	a.meter.Complete(a.billingProgress(a.seen))
	d := routing.DemandOf(&a.s.c.Request)
	a.hops = estimated(a.usage, a.seen, d, a.entry.Limits, a.hopCap(), billing.TokenizerOf(a.entry.Target.Model))
}
