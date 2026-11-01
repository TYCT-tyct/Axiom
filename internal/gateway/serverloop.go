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
// client set, and how much of each was used. It lives on the session, so every hop of the loop shares one attempt and
// the attempt's usage carries every sampling (docs/domains/billing.md「预留与结算」).
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

// search runs the searches of one tool-call step and appends the model's calls and their results to the conversation.
// A backend failure ends the call before anything is written: the model usage of the hops so far is waived as
// undelivered, while the searches that did run stay charged (docs/domains/billing.md「免单规则」).
//
//nolint:cyclop,funlen,gocognit // one branch per cap, query, result budget and backend outcome
func (a *attempt) search(ctx context.Context, calls []protocol.ServerToolCall) *ended {
	s, loop := a.s, a.s.loop
	results := make([]string, 0, len(calls))
	for _, call := range calls {
		if loop.disabled || loop.steps >= loop.cap {
			loop.disabled = true
			results = append(results, "The web search step limit for this request was reached. Answer with the results you already have.")
			continue
		}
		loop.steps++
		query := searchQuery(call.Arguments)
		if query == "" {
			results = append(results, "The web search call did not name a query. Call it again with a query, or answer without a search.")
			continue
		}
		if len(query) > protocol.MaxWebSearchQueryBytes {
			results = append(results, "The search query is longer than this search engine accepts. Try a shorter one.")
			continue
		}
		remaining := int64(protocol.MaxServerResults)
		if loop.tool.MaxResults > 0 {
			remaining = int64(loop.tool.MaxResults)
		}
		if loop.tool.MaxTotalResults > 0 {
			left := loop.tool.MaxTotalResults - loop.results
			if left <= 0 {
				loop.disabled = true
				results = append(results, "The result limit for this request was reached. Answer with the results you already have.")
				continue
			}
			remaining = min(remaining, left)
		}
		room := min(protocol.MaxServerResultBytes, protocol.ServerResultBudget-loop.bytes)
		if room <= 0 {
			loop.disabled = true
			results = append(results, "The result limit for this request was reached. Answer with the results you already have.")
			continue
		}
		if !s.reserveSearch(websearch.WorkingSet) {
			return a.abort(protocol.Error{Type: protocol.Overloaded}, 1)
		}
		searchCtx, cancel := context.WithTimeout(ctx, websearch.Timeout)
		found, err := loop.backend.Search(searchCtx, websearch.Query{
			Text: query, MaxResults: int(remaining),
			IncludeDomains: loop.tool.IncludeDomains, ExcludeDomains: loop.tool.ExcludeDomains,
		})
		cancel()
		switch {
		case errors.Is(err, websearch.ErrQueryTooLong):
			results = append(results, "The search query is longer than this search engine accepts. Try a shorter one.")
			continue
		case err != nil:
			s.logger.WarnContext(ctx, "the web search backend failed in the model-side loop", "request_id", s.c.ID.String(), "err", err)
			return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
		}
		s.searches++
		s.searched = true
		text, n := searchResults(query, found, room, int(remaining))
		loop.results += int64(n)
		loop.bytes += len(text)
		results = append(results, text)
		for _, r := range found {
			if len(s.citations) >= 32 {
				break
			}
			s.citations = append(s.citations, protocol.Citation{URL: r.URL, Title: r.Title, Content: r.Content})
		}
		if loop.tool.MaxTotalResults > 0 && loop.results >= loop.tool.MaxTotalResults {
			loop.disabled = true
		}
		if loop.bytes >= protocol.ServerResultBudget {
			loop.disabled = true
		}
	}
	if loop.steps >= loop.cap {
		loop.disabled = true
	}
	// AppendServerTurn copies the whole conversation: charge the copy and the results before it allocates, not after.
	held := int64(2*len(s.c.Body)) + int64(protocol.MaxServerResultBytes) + 8<<10
	if !s.reserveSearch(held) {
		return a.abort(protocol.Error{Type: protocol.Overloaded}, 1)
	}
	body, err := protocol.AppendServerTurn(s.c.Format, s.c.Body, calls, results)
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
	// The admission's own fields are not in the body and would be lost with the new Inspect: the tier it resolved, the
	// files it parsed, and the input bound a search plugin reserved. The loop's own input is what Inspect just read.
	req.ServiceTiers = s.c.Request.ServiceTiers
	req.FileParses = s.c.Request.FileParses
	req.SearchBytes = s.c.Request.SearchBytes
	req.Needs |= protocol.WebSearch | protocol.Tools
	s.c.Body, s.c.Request = body, req
	s.serverFunction = !loop.disabled
	return nil
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

// holdOut keeps the client-format events of a hop that may not be the answer. The bytes are charged to the node's
// budget until the call ends or the hop turns out to be a tool call.
func (s *session) holdOut(out []byte) bool {
	if len(out) == 0 {
		return true
	}
	if !s.x.Memory.Charge(int64(len(out))) {
		return false
	}
	s.outHeld += int64(len(out))
	s.out = append(s.out, out...)
	return true
}

// dropOut discards the held events of a tool-call hop and gives their budget back.
func (s *session) dropOut() {
	s.x.Memory.Refund(s.outHeld)
	s.outHeld, s.out = 0, s.out[:0]
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

// hopDone keeps one hop's usage as its own billing segments: every sampling has its input and output, and only the
// charged attempt's segments reach the user's bill, so they are merged here rather than priced as one reply.
func (a *attempt) hopDone(relay *protocol.Relay) {
	if a.hopRecorded {
		return
	}
	a.hopRecorded = true
	s := a.s
	u := relay.Usage()
	a.usage.Segments = append(a.usage.Segments, u.Segments...)
	if u.Invalid != nil && a.usage.Invalid == nil {
		a.usage.Invalid = u.Invalid
	}
	a.seen = relay.Progress()
	d := routing.DemandOf(&s.c.Request)
	a.hops = append(a.hops, estimated(u, a.seen, d, a.entry.Limits, a.hopCap(), billing.TokenizerOf(a.entry.Target.Model))...)
}
