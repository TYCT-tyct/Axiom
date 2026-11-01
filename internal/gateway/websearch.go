package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/websearch"
)

// SearchBackends are the node's enabled search engines, by name. The interface keeps tests independent of how the
// node loaded them; the node's own value is *websearch.Engines.
type SearchBackends interface {
	Find(name string) (*websearch.Backend, bool)
	Default() (*websearch.Backend, bool)
	List() []websearch.Engine
	Configured() bool
}

// checkWebSearch refuses a web plugin the node cannot serve, before anything is reserved, and otherwise fixes the
// query and the input the results may add. The refusal while no backend is configured is the documented one: the same
// sentence the plugin and :online gave before the backend existed.
func (h *Handler) checkWebSearch(req *protocol.Request, body []byte) (string, *Refusal) {
	if req.Web == nil {
		return "", nil
	}
	if req.Format != protocol.Chat {
		if req.Web.Online {
			return "", invalid("The :online variant is only supported for Chat requests.")
		}
		return "", invalid(`plugin "web" is not supported for this format`)
	}
	query, err := protocol.ChatSearchQuery(body)
	if err != nil {
		return "", invalid(err.Error())
	}
	if query == "" {
		return "", invalid(protocol.ErrEmptySearchQuery.Error())
	}
	if len(query) > protocol.MaxWebSearchQueryBytes {
		return "", invalid(fmt.Sprintf("The web plugin's search query is longer than %d bytes.", protocol.MaxWebSearchQueryBytes))
	}
	// The name is checked here, where the configured engines are known; whether the default exists is only known
	// against the entry's model, so resolveSearch decides that after routing.
	if req.Web.Engine != "" && req.Web.Engine != "native" {
		if _, ok := findBackend(h.Search, req.Web.Engine); !ok {
			return "", invalid(fmt.Sprintf("plugin option %q is not an enabled search engine", req.Web.Engine))
		}
	}
	// The results are not in the body admission reads, so the request reserves the most they may add before the
	// search runs (docs/domains/billing.md「预留与结算」).
	req.SearchBytes = protocol.WebSearchMaxBytes
	return query, nil
}

// findBackend is Find that also answers for a node with no search configured at all.
func findBackend(all SearchBackends, name string) (*websearch.Backend, bool) {
	if all == nil {
		return nil, false
	}
	return all.Find(name)
}

// defaultBackend is Default for a node with no search configured.
func defaultBackend(all SearchBackends) (*websearch.Backend, bool) {
	if all == nil {
		return nil, false
	}
	return all.Default()
}

// onlineWebPlugin gives a request whose deprecated :online variant asked for web search the plugin it stands for: one
// search, OpenRouter's default number of results.
func onlineWebPlugin(req *protocol.Request, online bool) {
	if online && req.Web == nil {
		req.Web = &protocol.WebPlugin{MaxResults: protocol.DefaultWebSearchResults, Online: true}
	}
}

// restoreWeb puts back what compression removed: the plugin is a routing setting, not a body one, and the input its
// results may add is reserved for again here (docs/domains/billing.md「预留与结算」).
func restoreWeb(req *protocol.Request, web *protocol.WebPlugin) {
	req.Web = web
	if web != nil {
		req.SearchBytes = protocol.WebSearchMaxBytes
	}
}

// webSearch runs the request's one search and injects its results before the first user turn. It must run after
// admission, so a call that cannot pay searches nothing, and before an attempt, so a failed search is an upstream
// failure rather than a charge. The query is client content and is never logged.
func (s *session) webSearch(ctx context.Context) *ended {
	web, backend := s.c.Request.Web, s.c.searchBackend
	if web == nil || backend == nil {
		return nil
	}
	if !s.reserveSearch(websearch.WorkingSet) {
		return &ended{err: protocol.Error{Type: protocol.Overloaded}}
	}
	searchCtx, cancel := context.WithTimeout(ctx, websearch.Timeout)
	defer cancel()
	results, err := backend.Search(searchCtx, websearch.Query{
		Text: s.c.search, MaxResults: web.MaxResults,
		IncludeDomains: web.IncludeDomains, ExcludeDomains: web.ExcludeDomains,
	})
	if err != nil {
		if errors.Is(err, websearch.ErrQueryTooLong) {
			return &ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: "The web plugin's search query is longer than this search engine accepts."}}
		}
		s.logger.WarnContext(ctx, "the web search backend failed", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	if !s.reserveSearch(3*int64(len(s.c.Body)) + protocol.WebSearchMaxBytes) {
		return &ended{err: protocol.Error{Type: protocol.Overloaded}}
	}
	body, err := protocol.InjectWebSearch(s.c.Body, webSearchMessage(web, results, s.start))
	if err != nil {
		s.logger.Error("the web search results could not be injected", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	req, err := protocol.Inspect(s.c.Format, body)
	if err != nil {
		s.logger.Error("the injected web search body is invalid", "request_id", s.c.ID.String(), "err", err)
		return &ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	// :online is a routing setting, not a body member, so Inspect cannot see it; the plugin's own members remain in
	// the body and are read again, then removed by Prepare.
	if req.Web == nil {
		req.Web = web
	}
	req.SearchBytes = 0
	s.c.Body, s.c.Request = body, req
	s.searches = 1
	s.citations = make([]protocol.Citation, len(results))
	for i, r := range results {
		s.citations[i] = protocol.Citation{URL: r.URL, Title: r.Title, Content: r.Content}
	}
	return nil
}

// reserveSearch draws from the node's budget for a search's working set and keeps it until the call is over, when
// release gives it back. The results and the injected body are the call's to hold; the adapter's response buffer is
// already gone by then, but the same reservation keeps the whole operation within the node's budget.
func (s *session) reserveSearch(n int64) bool {
	if n <= 0 {
		return true
	}
	if !s.x.Memory.Charge(n) {
		return false
	}
	s.searchHeld += n
	return true
}

// webSearchMessage is the system message the results are injected in, worded as OpenRouter's web search announcement
// (OpenRouter《Introducing web search via the API》, read 2026-10-31). A configured search_prompt replaces the
// instruction; the results follow either way, one per line, so a model can cite the URL it used.
func webSearchMessage(web *protocol.WebPlugin, results []websearch.Result, at time.Time) string {
	prompt := web.SearchPrompt
	if prompt == "" {
		prompt = fmt.Sprintf("A web search was conducted on %s. Incorporate the following web search results into your response. IMPORTANT: Cite them using markdown links named using the domain of the source. Example: [nytimes.com](https://nytimes.com/some-page).", at.UTC().Format("2006-01-02"))
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n")
	if len(results) == 0 {
		b.WriteString("No results were returned.")
		return b.String()
	}
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return strings.TrimSpace(b.String())
}
