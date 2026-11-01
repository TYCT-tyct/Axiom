package websearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// firecrawlBase is the search API's base URL when the admin leaves one out. Firecrawl's v2 search takes a query and
// returns web results with a description per result (Firecrawl《Search》, read 2026-11-01).
const (
	firecrawlBase = "https://api.firecrawl.dev/v2"
	// firecrawlQueryBytes is Firecrawl's documented query limit. It is smaller than the gateway's, so a query that
	// fits the plugin can still be too long for this backend.
	firecrawlQueryBytes = 500
)

// ErrQueryTooLong identifies a query a backend refuses for its own length limit, which the client can shorten. It is
// a client error, not an upstream failure.
var ErrQueryTooLong = errors.New("websearch: the query is too long for the backend")

// Firecrawl searches through Firecrawl's search API: one POST per search, with the key in the Authorization header.
type Firecrawl struct {
	base   *url.URL
	key    secret.Secret
	client *egress.Client
}

// OpenFirecrawl validates the configuration and builds the adapter. Firecrawl needs a key; an empty URL means the
// documented base.
func OpenFirecrawl(c Config, client *egress.Client) (Searcher, error) {
	if client == nil {
		return nil, errors.New("websearch: configured without an egress client")
	}
	if c.URL == "" {
		c.URL = firecrawlBase
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("websearch: %s is not an http(s) base URL", urlVar)
	}
	if c.Key.Reveal() == "" {
		return nil, errors.New("websearch: firecrawl needs an API key")
	}
	return &Firecrawl{base: u, key: c.Key, client: client}, nil
}

// firecrawlRequest is the search request's body. includeDomains and excludeDomains are mutually exclusive, so both
// filters are written as the query operators Firecrawl documents instead (Firecrawl《Search API》, read 2026-11-01).
type firecrawlRequest struct {
	Query          string   `json:"query"`
	Limit          int      `json:"limit"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
}

type firecrawlResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Web []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"web"`
	} `json:"data"`
	Error string `json:"error"`
}

// Search asks Firecrawl for the query's results. It never quotes the query or the backend's body in an error: the
// query is client content and the body is the backend's.
//
//nolint:cyclop,funlen // the request, its filters and the response are one adapter
func (f *Firecrawl) Search(ctx context.Context, q Query) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, ErrNoQuery
	}
	limit := q.MaxResults
	if limit <= 0 {
		limit = DefaultResults
	}
	if limit > MaxResults {
		limit = MaxResults
	}
	in := firecrawlRequest{Query: text, Limit: limit}
	switch {
	case len(q.IncludeDomains) > 0 && len(q.ExcludeDomains) > 0:
		in.Query = queryText(text, q)
	case len(q.IncludeDomains) > 0:
		in.IncludeDomains = q.IncludeDomains
	case len(q.ExcludeDomains) > 0:
		in.ExcludeDomains = q.ExcludeDomains
	}
	if len(in.Query) > firecrawlQueryBytes {
		return nil, ErrQueryTooLong
	}
	body, err := jsonx.MarshalCanonical(in)
	if err != nil {
		return nil, err
	}
	u := *f.base
	u.Path = strings.TrimRight(u.Path, "/") + "/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: the request could not be built", ErrUpstream)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.key.Reveal())
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUpstream, transportFailure(err))
	}
	defer resp.Body.Close() //nolint:errcheck // an error closing a response whose answer is already decided changes nothing
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the backend answered %d", ErrUpstream, resp.StatusCode)
	}
	raw, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	var wire firecrawlResponse
	if err := jsonx.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("%w: the backend answered invalid JSON", ErrUpstream)
	}
	if !wire.Success || wire.Data == nil {
		return nil, fmt.Errorf("%w: the backend answered without results", ErrUpstream)
	}
	c := results{limit: limit, out: make([]Result, 0, min(limit, len(wire.Data.Web)))}
	for _, r := range wire.Data.Web {
		c.add(wireResult{URL: r.URL, Title: r.Title, Content: r.Description})
	}
	return c.out, nil
}
