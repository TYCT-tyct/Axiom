// Package websearch runs the gateway's web plugin through a self-hosted SearXNG instance: one query in, at most a
// bounded number of results out. The backend is configured by the deployment administrator and disabled by default;
// nothing here knows about a paid search provider.
package websearch

import (
	"bytes"
	"context"
	"encoding/json/jsontext" //nolint:depguard // read the backend's results token by token so their size is bounded
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// The environment variables that configure the backend. They are named here, where they are read, so a startup error
// and .env.example spell them the same way.
const (
	urlVar = "AXIOM_SEARCH_URL"
	keyVar = "AXIOM_SEARCH_KEY" //nolint:gosec // a variable name, not a credential
)

const (
	// Timeout is how long one search may take, the whole request included. A search runs before the model call it
	// serves, so it must not spend the call's time budget on a slow engine.
	Timeout = 15 * time.Second
	// DefaultResults is OpenRouter's max_results default for its web plugin (OpenRouter《Web Search》, read 2026-10-31).
	DefaultResults = 5
	// MaxResults bounds what a client may ask for; a SearXNG page holds more than a prompt should.
	MaxResults = 10

	maxQueryBytes    = 8192
	maxResponseBytes = 4 << 20
	maxURLBytes      = 8 << 10
	maxTitleBytes    = 256
	maxContentBytes  = 1024
)

// WorkingSet is the most one Search may hold past the caller's query and request: the response body, one value while
// it is unquoted, and the bounded results. The gateway charges it to the node's budget before searching.
const WorkingSet = 4 * maxResponseBytes

// ErrUpstream is a search backend that did not answer with usable results: a status other than 200, a body over the
// cap, malformed JSON, or a transport error. The gateway answers it as an upstream failure.
var ErrUpstream = errors.New("websearch: the backend failed")

// ErrNoQuery is a search with no text: the request should have been refused before the reservation.
var ErrNoQuery = errors.New("websearch: empty query")

// Config is the backend one node searches through. The zero value means web search is disabled.
type Config struct {
	URL string        // the SearXNG base URL, without a query; /search is appended
	Key secret.Secret // the bearer token a reverse proxy in front of SearXNG checks; empty sends none
}

// Configured reports whether a backend is set.
func (c Config) Configured() bool { return c.URL != "" }

// ReadConfig reads the backend from env. It is one boundary with the admin's saved settings, so both go through Parse.
func ReadConfig(env *config.Env) (Config, error) {
	return Parse(env.String(urlVar, ""), env.String(keyVar, ""))
}

// Parse validates a backend URL and key from any boundary: the environment or the admin's saved settings. With no URL
// the zero Config is returned and every request keeps the documented refusal; a key without a URL is an error rather
// than a credential silently unused.
func Parse(raw, key string) (Config, error) {
	raw, key = strings.TrimSpace(raw), strings.TrimSpace(key)
	if raw == "" {
		if key != "" {
			return Config{}, fmt.Errorf("%s is set but %s is not: the key has no backend to authenticate to", keyVar, urlVar)
		}
		return Config{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("%s must be an http(s) base URL with a host, no credentials, query or fragment", urlVar)
	}
	return Config{URL: strings.TrimRight(raw, "/"), Key: secret.New(key)}, nil
}

// Query is one search: the text and the filters the request's web plugin asked for.
type Query struct {
	Text           string
	MaxResults     int
	IncludeDomains []string
	ExcludeDomains []string
}

// Result is one search result, with the text bounded so one page cannot fill a prompt.
type Result struct {
	URL, Title, Content string
}

// Searcher is what the gateway runs. The interface exists for the one adapter below and for tests; there is no second
// implementation to abstract over yet.
type Searcher interface {
	Search(ctx context.Context, q Query) ([]Result, error)
}

// SearXNG searches through a SearXNG instance's JSON API.
type SearXNG struct {
	base   *url.URL
	key    secret.Secret
	client *egress.Client
}

// Open validates the configuration and builds the adapter. With no URL it returns a nil interface: the feature is off,
// and the gateway keeps its refusal in front of the reservation. The client must be the node's egress client, so the backend is subject to the same
// address, redirect and timeout rules as every other outbound request.
func Open(c Config, client *egress.Client) (Searcher, error) {
	if !c.Configured() {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("websearch: configured without an egress client")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("websearch: %s is not an http(s) base URL", urlVar)
	}
	return &SearXNG{base: u, key: c.Key, client: client}, nil
}

// Search asks the backend for the query's results. The request carries the context's deadline, which egress requires;
// the caller sets it (the gateway uses Timeout). Errors never quote the query or the backend's body: the query is user
// content and the body is the backend's.
func (s *SearXNG) Search(ctx context.Context, q Query) ([]Result, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, ErrNoQuery
	}
	qtext := queryText(text, q)
	if len(qtext) > maxQueryBytes {
		return nil, fmt.Errorf("websearch: the query is longer than %d bytes", maxQueryBytes)
	}
	limit := q.MaxResults
	if limit <= 0 {
		limit = DefaultResults
	}
	if limit > MaxResults {
		limit = MaxResults
	}
	u := *s.base
	u.Path = strings.TrimRight(u.Path, "/") + "/search"
	values := u.Query()
	values.Set("q", qtext)
	values.Set("format", "json")
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: the request could not be built", ErrUpstream)
	}
	req.Header.Set("Accept", "application/json")
	if key := s.key.Reveal(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// The transport's error quotes the URL, which carries the query: report only the class, so a log line keeps
		// no client content.
		return nil, fmt.Errorf("%w: %s", ErrUpstream, transportFailure(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the backend answered %d", ErrUpstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read the response: %v", ErrUpstream, err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("%w: the backend answered more than %d bytes", ErrUpstream, maxResponseBytes)
	}
	results, err := parseResults(body, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: the backend answered invalid JSON", ErrUpstream)
	}
	return results, nil
}

// transportFailure names what went wrong without the request's URL.
func transportFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the backend did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the search was canceled"
	default:
		return "the backend could not be reached"
	}
}

// queryText writes the domain filters as the search operators SearXNG's engines understand. The gateway has already
// held each domain to a plain host name, so a value cannot write an operator of its own.
func queryText(text string, q Query) string {
	var b strings.Builder
	b.WriteString(text)
	for _, d := range q.IncludeDomains {
		b.WriteString(" site:")
		b.WriteString(d)
	}
	for _, d := range q.ExcludeDomains {
		b.WriteString(" -site:")
		b.WriteString(d)
	}
	return b.String()
}

// results collects the results a prompt can carry: an http(s) URL, no duplicate, and bounded text. It never keeps
// more than limit, so a response with many empty or invalid objects cannot grow the adapter's working set.
type results struct {
	limit int
	out   []Result
	seen  map[string]struct{}
}

func (c *results) add(r wireResult) {
	if len(c.out) >= c.limit {
		return
	}
	key, ok := resultURL(r.URL)
	if !ok {
		return
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{}, c.limit)
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.out = append(c.out, Result{
		URL:     key,
		Title:   truncate(strings.TrimSpace(r.Title), maxTitleBytes),
		Content: truncate(strings.TrimSpace(r.Content), maxContentBytes),
	})
}

// resultURL validates and normalizes one result's URL. SearXNG also returns answers and key-value rows with no URL;
// those are not citations and are left out.
func resultURL(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return "", false
	}
	return u.String(), true
}

// parseResults reads the results array one object at a time and keeps only its bounded, valid results. Decoding the
// whole array first lets the number of empty objects decide the allocation, not the number of results; MaxResults
// applies while reading, not after.
func parseResults(body []byte, limit int) ([]Result, error) {
	if err := jsonx.Validate(body); err != nil {
		return nil, err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if dec.PeekKind() != '{' {
		return nil, errors.New("the backend response is not an object")
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	c := results{limit: limit, out: make([]Result, 0, limit)}
	for dec.PeekKind() != '}' {
		name, err := readMemberName(dec)
		if err != nil {
			return nil, err
		}
		if name != "results" {
			if err := dec.SkipValue(); err != nil {
				return nil, err
			}
			continue
		}
		if err := readResultArray(dec, &c); err != nil {
			return nil, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the backend response has data after the object")
	}
	return c.out, nil
}

// readResultArray reads results one object at a time. A null array is the same as an absent one.
func readResultArray(dec *jsontext.Decoder, c *results) error {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadToken()
		return err
	}
	if dec.PeekKind() != '[' {
		return errors.New("results is not an array")
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != ']' {
		if dec.PeekKind() == 'n' {
			if _, err := dec.ReadToken(); err != nil {
				return err
			}
			continue
		}
		if dec.PeekKind() != '{' {
			return errors.New("a result is not an object")
		}
		r, ok, err := readResult(dec)
		if err != nil {
			return err
		}
		if ok {
			c.add(r)
		}
	}
	_, err := dec.ReadToken()
	return err
}

// readResult reads one result's three fields, skipping the engine-specific ones. A field beyond its bound makes the
// result unusable instead of being unquoted in full; the rest of the object is still skipped so the stream stays
// aligned.
func readResult(dec *jsontext.Decoder) (wireResult, bool, error) {
	if _, err := dec.ReadToken(); err != nil {
		return wireResult{}, false, err
	}
	var r wireResult
	ok := true
	for dec.PeekKind() != '}' {
		name, err := readMemberName(dec)
		if err != nil {
			return wireResult{}, false, err
		}
		var field *string
		max, shorten := 0, false
		switch name {
		case "url":
			field, max = &r.URL, maxURLBytes
		case "title":
			field, max, shorten = &r.Title, maxTitleBytes, true
		case "content":
			field, max, shorten = &r.Content, maxContentBytes, true
		default:
			if err := dec.SkipValue(); err != nil {
				return wireResult{}, false, err
			}
			continue
		}
		v, valid, err := readField(dec, max, shorten)
		if err != nil {
			return wireResult{}, false, err
		}
		if !valid {
			ok = false
			continue
		}
		*field = v
	}
	if _, err := dec.ReadToken(); err != nil {
		return wireResult{}, false, err
	}
	return r, ok, nil
}

// readField reads a string field, or null, with a bound on both its encoded and decoded size. A title or snippet
// over the bound is decoded and truncated, because the body itself is bounded; a URL over its bound invalidates the
// result.
func readField(dec *jsontext.Decoder, max int, shorten bool) (string, bool, error) {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadToken()
		return "", err == nil, err
	}
	if dec.PeekKind() != '"' {
		return "", false, errors.New("a result field is not a string")
	}
	raw, err := dec.ReadValue()
	if err != nil {
		return "", false, err
	}
	if len(raw) > 6*max+2 && !shorten {
		return "", false, nil
	}
	b, err := jsontext.AppendUnquote(nil, raw)
	if err != nil {
		return "", false, err
	}
	s := string(b)
	if len(s) > max {
		if !shorten {
			return "", false, nil
		}
		s = truncate(s, max)
	}
	return s, true, nil
}

func readMemberName(dec *jsontext.Decoder) (string, error) {
	raw, err := dec.ReadValue()
	if err != nil {
		return "", err
	}
	b, err := jsontext.AppendUnquote(nil, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// wireResult is the part of a SearXNG result this adapter reads; the API's many engine-specific members are left out.
type wireResult struct {
	URL     string
	Title   string
	Content string
}

// truncate cuts a UTF-8 string to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
