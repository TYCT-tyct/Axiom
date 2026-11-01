package websearch

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/secret"
)

// Kind is a search backend's implementation. A deployment may run several engines of either kind.
type Kind string

// The backend kinds.
const (
	KindSearXNG   Kind = "searxng"
	KindFirecrawl Kind = "firecrawl"
)

// ParseKind reads a backend kind. An unknown kind is refused: it would otherwise be saved and fail at the next start.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindSearXNG, KindFirecrawl:
		return k, nil
	default:
		return "", fmt.Errorf("websearch: %q is not a search backend kind", s)
	}
}

// Engine is one configured backend: its adapter, its sealed key, whether it may be used, and its published prices.
// Cost and price are per search and per extra result, the two figures a paid search API charges (OpenRouter's web
// search page lists Firecrawl at $0.005 per search plus $0.0125 per result, read 2026-11-01).
type Engine struct {
	ID           uuid.UUID
	Name         string
	Kind         Kind
	URL          string
	Key          secret.Secret
	Enabled      bool
	Default      bool
	CostSearch   platform.UnitPrice
	CostResult   platform.UnitPrice
	Markup       string
	PriceSearch  platform.UnitPrice
	PriceResult  platform.UnitPrice
	PriceVersion int64
	UpdatedAt    time.Time
}

// Config is the adapter configuration of a SearXNG engine, whose URL is the instance's base and whose key may be
// empty. A Firecrawl engine's URL defaults to its documented base and its key is required.
func (e Engine) Config() Config { return Config{URL: e.URL, Key: e.Key} }

// Price is the published sale price of one search returning results results: the per-search price plus one
// per-result price for every result after the first. A zero per-search price means the model's published web_search
// price applies instead, which keeps a self-hosted backend on the same price it had before engines existed.
func (e Engine) Price(results int) (platform.UnitPrice, error) {
	if e.PriceSearch.IsZero() {
		return platform.UnitPrice{}, nil
	}
	if results < 1 {
		results = DefaultResults
	}
	extra, err := e.PriceResult.Scale(big.NewRat(int64(results-1), 1))
	if err != nil {
		return platform.UnitPrice{}, err
	}
	return e.PriceSearch.Add(extra)
}

// Backend is one opened engine: its configuration and the adapter the gateway searches through.
type Backend struct {
	Engine
	Searcher
}

// Engines are the enabled backends of a node, by name, with the one a request that names none falls back to.
type Engines struct {
	all    []*Backend
	byName map[string]*Backend
	def    *Backend
}

// OpenEngines builds the enabled backends and their adapters. It fails at startup on a configuration a call could not
// use: a duplicate name, a kind without an adapter, an incomplete URL or key, or an enabled set without exactly one
// default. Disabled engines are left out: they are configuration waiting to be turned on.
func OpenEngines(list []Engine, client *egress.Client) (*Engines, error) {
	e := &Engines{byName: make(map[string]*Backend, len(list))}
	defaults := 0
	for _, cfg := range list {
		if !cfg.Enabled {
			continue
		}
		if !ValidName(cfg.Name) {
			return nil, fmt.Errorf("websearch: %q is not a search engine name", cfg.Name)
		}
		if _, ok := e.byName[cfg.Name]; ok {
			return nil, fmt.Errorf("websearch: search engine %q is configured twice", cfg.Name)
		}
		backend, err := openEngine(cfg, client)
		if err != nil {
			return nil, fmt.Errorf("websearch: search engine %q: %w", cfg.Name, err)
		}
		e.all = append(e.all, backend)
		e.byName[cfg.Name] = backend
		if cfg.Default {
			defaults++
			e.def = backend
		}
	}
	switch {
	case len(e.all) == 0:
		e.def = nil
	case defaults != 1:
		return nil, fmt.Errorf("websearch: %d search engines are enabled but %d is the default, want exactly one", len(e.all), defaults)
	}
	return e, nil
}

func openEngine(cfg Engine, client *egress.Client) (*Backend, error) {
	if client == nil {
		return nil, errors.New("opened without an egress client")
	}
	var s Searcher
	var err error
	switch cfg.Kind {
	case KindSearXNG:
		s, err = Open(cfg.Config(), client)
	case KindFirecrawl:
		s, err = OpenFirecrawl(cfg.Config(), client)
	default:
		return nil, fmt.Errorf("%q is not a search backend kind", cfg.Kind)
	}
	if err != nil {
		return nil, err
	}
	return &Backend{Engine: cfg, Searcher: s}, nil
}

// Find returns the enabled backend named name.
func (e *Engines) Find(name string) (*Backend, bool) {
	if e == nil {
		return nil, false
	}
	b, ok := e.byName[name]
	return b, ok
}

// Default returns the backend a request that names none falls back to.
func (e *Engines) Default() (*Backend, bool) {
	if e == nil || e.def == nil {
		return nil, false
	}
	return e.def, true
}

// List is the enabled engines in name order, for the model pages that show what a search costs.
func (e *Engines) List() []Engine {
	if e == nil {
		return nil
	}
	out := make([]Engine, 0, len(e.all))
	for _, b := range e.all {
		out = append(out, b.Engine)
	}
	slices.SortFunc(out, func(a, b Engine) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Configured reports whether the node has any backend at all.
func (e *Engines) Configured() bool { return e != nil && len(e.all) > 0 }

// ValidName reports whether name can be a backend name and an engine value: lowercase letters, digits, '-' and '_',
// starting with a letter or digit, at most 32 bytes, and not "native", which selects the provider's own search.
func ValidName(name string) bool {
	if name == "" || len(name) > 32 || name == "native" || !lowerAlnum(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !lowerAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func lowerAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
