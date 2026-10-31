package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
)

// PageTarget describes a reviewed official price section. Empty intervals use the documented defaults.
type PageTarget struct {
	Author          string `json:"author"`
	URL             string `json:"url"`
	Currency        string `json:"currency"`
	SectionStart    string `json:"section_start"`
	SectionEnd      string `json:"section_end"`
	IntervalSeconds int64  `json:"interval_seconds"`
	RateSeconds     int64  `json:"rate_seconds"`
}

func (p *PageTarget) validate() (string, error) {
	u, err := pageURL(p.URL)
	if err != nil {
		return "", err
	}
	if p.IntervalSeconds == 0 {
		p.IntervalSeconds = 3600
	}
	if p.RateSeconds == 0 {
		p.RateSeconds = 10
	}
	if _, err := platform.ParseCurrency(p.Currency); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Author) == "" || strings.TrimSpace(p.SectionStart) == "" || strings.TrimSpace(p.SectionEnd) == "" || p.SectionStart == p.SectionEnd {
		return "", fmt.Errorf("%w: page needs author and distinct nonempty markers", ErrInvalid)
	}
	for _, bound := range [][3]int64{{p.IntervalSeconds, 3600, 604800}, {p.RateSeconds, 1, 60}} {
		if bound[0] < bound[1] || bound[0] > bound[2] {
			return "", fmt.Errorf("%w: page interval or rate outside allowed range", ErrInvalid)
		}
	}
	p.URL = u.String()
	return "https://" + u.Host, nil
}

func pageURL(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("%w: a page needs an HTTPS URL without credentials or fragment", ErrInvalid)
	}
	u.Host = strings.ToLower(u.Host)
	if u.Port() == "443" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	return u, nil
}

// PageContent contains evidence only; it never contains a parsed price or changes a reference price.
type PageContent struct {
	Text   string            `json:"text"`
	SHA256 [sha256.Size]byte `json:"-"`
	// LegacyText is the same section extracted with the version-1 algorithm. fetchPage carries it so an upgrade can
	// prove that the stored snapshot saw the same page, rather than a text that merely collapses to the same characters.
	// It is not evidence and is never serialized.
	LegacyText string `json:"-"`
	// LegacyOK reports whether LegacyText was extracted successfully.
	LegacyOK bool `json:"-"`
}

// PageInspector shares a site's request spacing across read-only live acceptance inspections.
type PageInspector struct {
	client *egress.Client
	mu     sync.Mutex
	sites  map[string]inspectionSite
}

type inspectionSite struct {
	last, next time.Time
}

// NewPageInspector uses the same egress policy as the scheduled watcher, without opening storage.
func NewPageInspector(client *egress.Client) *PageInspector {
	return &PageInspector{client: client, sites: map[string]inspectionSite{}}
}

// Inspect fetches robots and one price section. Failed inspections never retry the page.
func (p *PageInspector) Inspect(ctx context.Context, target PageTarget) (PageContent, error) {
	return fetchPage(ctx, p.client, target, p.reserve, p.backoff)
}

func (p *PageInspector) reserve(ctx context.Context, origin string, delay time.Duration) error {
	for {
		p.mu.Lock()
		site := p.sites[origin]
		site.next = laterPageRequest(site.next, site.last.Add(delay))
		wait := time.Until(site.next)
		if wait <= 0 {
			site.last = time.Now()
			site.next = site.last.Add(delay)
		}
		p.sites[origin] = site
		p.mu.Unlock()
		if wait <= 0 {
			return ctx.Err()
		}
		if err := waitPage(ctx, wait); err != nil {
			return err
		}
	}
}

func (p *PageInspector) backoff(_ context.Context, origin string, delay time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	site := p.sites[origin]
	site.next = laterPageRequest(site.next, time.Now().Add(delay))
	p.sites[origin] = site
	return nil
}

func laterPageRequest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

type pageReservation func(context.Context, string, time.Duration) error

func fetchPage(ctx context.Context, client *egress.Client, target PageTarget, reserve, backoff pageReservation) (PageContent, error) {
	origin, err := target.validate()
	if err != nil {
		return PageContent{}, err
	}
	delay := time.Duration(target.RateSeconds) * time.Second
	robots, status, err := readPage(ctx, client, origin+"/robots.txt", origin, delay, reserve, backoff, 512<<10)
	if err != nil {
		return PageContent{}, fmt.Errorf("robots: %w", err)
	}
	delay, err = pageRobotsDelay(robots, status, target.URL, delay)
	if err != nil {
		return PageContent{}, errors.Join(err, backoff(ctx, origin, delay))
	}
	body, status, err := readPage(ctx, client, target.URL, origin, delay, reserve, backoff, maxDatasetBytes)
	if err != nil {
		return PageContent{}, err
	}
	if status != http.StatusOK {
		return PageContent{}, fmt.Errorf("page: status %d", status)
	}
	text, err := extractPriceSection(body, target.SectionStart, target.SectionEnd)
	if err != nil {
		return PageContent{}, err
	}
	// A failed legacy extraction is not fatal: the new extraction still succeeds, and a version upgrade must review
	// conservatively rather than assume the content is unchanged.
	legacy, legacyErr := legacyExtractPriceSection(body, target.SectionStart, target.SectionEnd)
	return PageContent{
		Text:       text,
		SHA256:     sha256.Sum256([]byte(text)),
		LegacyText: legacy,
		LegacyOK:   legacyErr == nil,
	}, nil
}

func readPage(ctx context.Context, client *egress.Client, address, origin string, delay time.Duration,
	reserve, backoff pageReservation, limit int64,
) (body []byte, status int, err error) {
	if err := reserve(ctx, origin, delay); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		wait := time.Hour
		if n, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && n >= 0 {
			wait = time.Duration(min(n, math.MaxInt64/int64(time.Second))) * time.Second
		} else if at, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			wait = max(0, time.Until(at))
		}
		if err := backoff(ctx, origin, wait); err != nil {
			return nil, resp.StatusCode, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	body, err = egress.ReadBody(resp.Body, limit)
	return body, resp.StatusCode, err
}

func waitPage(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(max(0, delay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func pageRobotsDelay(body []byte, status int, address string, delay time.Duration) (time.Duration, error) {
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return delay, nil
	case http.StatusOK:
	default:
		return 0, fmt.Errorf("robots: status %d; page not fetched", status)
	}
	rules, err := robotstxt.FromBytes(body)
	if err != nil {
		return 0, fmt.Errorf("robots: %w", err)
	}
	group := rules.FindGroup("Axiom")
	u, err := url.Parse(address)
	if err != nil {
		return 0, err
	}
	delay = max(delay, group.CrawlDelay)
	if !group.Test(u.RequestURI()) {
		return delay, errors.New("robots: page disallowed for Axiom")
	}
	if delay > time.Minute {
		return delay, errors.New("robots: crawl delay exceeds 60 seconds; page not fetched")
	}
	return delay, nil
}
