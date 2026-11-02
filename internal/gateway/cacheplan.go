package gateway

import (
	"crypto/sha256"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/protocol"
)

// The response cache's bounds. A TTL is what one request asks for; the row keeps that lifetime even when a later request
// with another TTL hits it (docs/domains/gateway.md「响应缓存」).
const (
	cacheDefaultTTL = 300 * time.Second
	cacheMaxTTL     = 86400 * time.Second
	// maxCacheEntry bounds one stored upstream reply. A normal streamed answer is far smaller; a reply above this is
	// simply not cached, never truncated.
	maxCacheEntry = 1 << 20
)

// cachePlan is what one call decided about the response cache, before admission. It is zero when caching is off, which
// is every call that did not ask for it.
type cachePlan struct {
	enabled bool
	ttl     time.Duration
	clear   bool
	key     [sha256.Size]byte
}

// cachePlanFor applies a preset's cache settings and the request's headers. A preset that says false cannot be turned on
// by a header; a header false turns off a preset that says true. Neither source asking leaves the cache off.
func cachePlanFor(c *Call, header http.Header) cachePlan {
	enabled := false
	if c.presetCache != nil {
		enabled = *c.presetCache
	}
	if on, ok := parseCacheSwitch(header.Get("X-Openrouter-Cache")); ok {
		if !on {
			enabled = false
		} else if c.presetCache == nil || *c.presetCache {
			enabled = true
		}
	}
	if !enabled || !cacheable(c) {
		return cachePlan{}
	}
	ttl := cacheDefaultTTL
	if c.presetTTL > 0 {
		ttl = time.Duration(c.presetTTL) * time.Second
	}
	if seconds, ok := parseCacheTTL(header.Get("X-Openrouter-Cache-Ttl")); ok {
		ttl = time.Duration(seconds) * time.Second
	}
	return cachePlan{
		enabled: true,
		ttl:     ttl,
		clear:   strings.EqualFold(strings.TrimSpace(header.Get("X-Openrouter-Cache-Clear")), "true"),
		key:     responseCacheKey(c),
	}
}

// cacheable is the paths this first version caches: a plain generation whose answer the request body alone determines.
// Direct formats need their own replay path; plugins, history and server-side tools add charges or context outside the
// body, so a hit would skip them (docs/domains/gateway.md「响应缓存」).
//
//nolint:cyclop // the exclusions are one declarative predicate
func cacheable(c *Call) bool {
	if c.Compact || c.Format.Direct() {
		return false
	}
	switch c.Format {
	case protocol.Chat, protocol.Responses, protocol.Messages:
	case protocol.Embeddings, protocol.Rerank, protocol.Completions:
		return false
	default:
		return false
	}
	r := &c.Request
	return r.Previous == "" && r.Web == nil && r.File == nil && r.FileParses == 0 && r.ServerSearch == nil &&
		!r.Compacts && r.FileRefs == 0 && !r.Needs.Has(protocol.WebSearch) &&
		!r.Needs.Has(protocol.CodeExecution) && !r.Needs.Has(protocol.FileSearch)
}

// responseCacheKey is never shared across accounts: the account ID is hashed into it, and every lookup runs in that
// account's RLS scope. The client format and streaming mode are explicit even though the body names stream, so two
// endpoints of the same path cannot collide.
func responseCacheKey(c *Call) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte("axiom/response-cache/v1\x00"))
	account := uuid.UUID(c.caller.AccountID())
	h.Write(account[:])
	h.Write([]byte{byte(c.Format), boolByte(c.Request.Stream)})
	writeNormalized(h, c.Body)
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// writeNormalized writes body without whitespace outside strings. It changes nothing else: property order, duplicate
// keys, number spellings and the whitespace inside a string are all significant (OpenRouter《Response Caching》,
// 2026-11-02). It streams the bytes, so hashing a request does not copy its body.
func writeNormalized(w io.Writer, body []byte) {
	start := 0
	inString, escaped := false, false
	for i, b := range body {
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			if start < i {
				_, _ = w.Write(body[start:i]) //nolint:errcheck // a hash never fails
			}
			start = i + 1
		case '"':
			inString = true
		}
	}
	if start < len(body) {
		_, _ = w.Write(body[start:]) //nolint:errcheck // a hash never fails
	}
}

// parseCacheSwitch reads the OpenRouter switch. A value that is neither true nor false is absent, so it cannot enable the
// cache by accident.
func parseCacheSwitch(value string) (on, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// parseCacheTTL reads leading digits, truncates a fraction and ignores trailing text, as OpenRouter does; the result is
// clamped to the cache's range. A value that does not begin with a digit is ignored.
func parseCacheTTL(value string) (seconds int64, ok bool) {
	value = strings.TrimSpace(value)
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(value[:i], 10, 64)
	if err != nil {
		n = int64(cacheMaxTTL / time.Second)
	}
	if n < 1 {
		n = 1
	}
	if n > int64(cacheMaxTTL/time.Second) {
		n = int64(cacheMaxTTL / time.Second)
	}
	return n, true
}

// The response headers of OpenRouter's cache (2026-11-02). They are written only for a successful answer of a call that
// asked for the cache; an error keeps the ordinary error headers.
const (
	cacheStatusHeader = "X-Openrouter-Cache-Status"
	cacheAgeHeader    = "X-Openrouter-Cache-Age"
	cacheTTLHeader    = "X-Openrouter-Cache-Ttl"
	cacheSourceHeader = "X-Openrouter-Cache-Source-Id"
)

// cacheHeaders adds the cache's result to a 200 that is about to be written. A miss is answered from the upstream this
// time; a hit says how old the answer is and how much of its TTL is left.
func (s *session) cacheHeaders(h http.Header) {
	if s.x.Cache == nil || !s.c.cache.enabled {
		return
	}
	if s.cacheHit {
		h.Set(cacheStatusHeader, "HIT")
		h.Set(cacheAgeHeader, strconv.FormatInt(int64(max(0, s.cacheAge)/time.Second), 10))
		h.Set(cacheTTLHeader, strconv.FormatInt(int64(max(0, s.cacheTTL)/time.Second), 10))
		h.Set(cacheSourceHeader, s.cacheSource.String())
		return
	}
	h.Set(cacheStatusHeader, "MISS")
	h.Set(cacheTTLHeader, strconv.FormatInt(int64(s.c.cache.ttl/time.Second), 10))
}
