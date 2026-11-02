package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestAResponseCacheHitIsFreeAndRecorded: the first call fills the cache and pays; the second, same body and account,
// is replayed through the normal relay, gets fresh IDs and zero usage, pays nothing, and the call log says it was a hit.
//
//nolint:cyclop,gocognit // the miss, hit, money and log assertions are one end-to-end check
func TestAResponseCacheHitIsFreeAndRecorded(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	// One clock for the hit's age: a busy machine cannot make the test see the microseconds between the write and the
	// replay as an elapsed second.
	now := time.Now()
	r.cache.now = func() time.Time { return now }
	records, err := observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		Dir: t.TempDir(), Capacity: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r.exec.Records = records
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 3, 40, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true", "X-Openrouter-Cache-Ttl", "60"}

	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("miss: %d\n%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get(cacheStatusHeader); got != "MISS" {
		t.Errorf("miss header %q", got)
	}
	if got := resp.Header.Get(cacheTTLHeader); got != "60" {
		t.Errorf("miss TTL %q", got)
	}
	first := r.lastCall().ID
	waitFor(t, "the cached reply", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })

	resp, body = r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hit: %d\n%s", resp.StatusCode, body)
	}
	second := r.lastCall().ID
	if second == first {
		t.Fatal("the hit reused the call ID")
	}
	if got := resp.Header.Get(cacheStatusHeader); got != "HIT" {
		t.Errorf("hit header %q", got)
	}
	if got := resp.Header.Get(cacheSourceHeader); got != first.String() {
		t.Errorf("source %q, want %q", got, first)
	}
	if got := resp.Header.Get(cacheAgeHeader); got != "0" {
		t.Errorf("age %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("hit Cache-Control %q", got)
	}
	if !strings.Contains(body, `"cost":0`) || strings.Contains(body, `"input_tokens":130`) {
		t.Errorf("the hit body carries the original usage or cost:\n%s", body)
	}
	if !strings.Contains(body, `"cost_currency":"USD"`) {
		t.Errorf("the hit body has no site currency:\n%s", body)
	}
	if n := sends(up); n != 1 {
		t.Errorf("%d upstream sends, want the one that filled the cache", n)
	}
	if charged := r.charged(first); charged == 0 {
		t.Error("the miss was not charged")
	}
	if charged := r.charged(second); charged != 0 {
		t.Errorf("the hit was charged %d", charged)
	}
	if err := r.exec.Records.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM observability.call_log WHERE id=$1 AND cache_hit", second); n != 1 {
		t.Errorf("the hit is not recorded as a cache hit")
	}
	r.ledgerBalanced()
}

func sends(u *fakeUpstreams) (n int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.eps {
		n += e.sends
	}
	return n
}

// TestACacheEntryRespectsTheKeysCurrentRoute: a cache shared by an account's keys must not replay a reply produced by a
// model or endpoint that the current key may not use. The same body under two keys with different model permissions
// misses on the second key and goes upstream; narrowing a key's models after a hit makes the next call a miss too.
//
//nolint:cyclop // the miss, hit and narrowed-key assertions are one route check
func TestACacheEntryRespectsTheKeysCurrentRoute(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 3, 40, false)}
	}
	a := r.funded(1e12)
	alpha, alphaID := r.newKeyWithModels(a, "acme/alpha")
	beta, _ := r.newKeyWithModels(a, "acme/beta")
	headers := []string{"X-Openrouter-Cache", "true"}
	alphaBody := strings.Replace(stream("openrouter/auto"), "hello", "alpha", 1)
	betaBody := strings.Replace(stream("openrouter/auto"), "hello", "beta", 1)

	if resp, out := r.do("POST", "/v1/messages", alpha, strings.NewReader(alphaBody), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "MISS" {
		t.Fatalf("alpha miss: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), out)
	}
	waitFor(t, "the alpha entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	if resp, out := r.do("POST", "/v1/messages", beta, strings.NewReader(betaBody), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "MISS" {
		t.Fatalf("beta reuse: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), out)
	}
	if n := sends(up); n != 2 {
		t.Fatalf("%d upstream sends, want alpha and beta to miss separately", n)
	}
	if resp, out := r.do("POST", "/v1/messages", alpha, strings.NewReader(alphaBody), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "HIT" {
		t.Fatalf("alpha hit: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), out)
	}
	// Narrow alpha to beta only: the entry from alpha's old route is no longer authorized, so it must miss.
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := r.accounts.UpdateKey(t.Context(), tx, audit.System("gateway.test"), alphaID,
			accounts.KeySettings{Name: "Test", Models: []string{"acme/beta"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if resp, out := r.do("POST", "/v1/messages", alpha, strings.NewReader(alphaBody), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "MISS" {
		t.Fatalf("narrowed alpha: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), out)
	}
	if n := sends(up); n != 3 {
		t.Errorf("%d upstream sends, want the narrowed key to miss", n)
	}
}

func (r *rig) newKeyWithModels(a account, models ...string) (string, accounts.KeyID) {
	r.t.Helper()
	text, id := r.newKey(a)
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := r.accounts.UpdateKey(r.t.Context(), tx, audit.System("gateway.test"), id,
			accounts.KeySettings{Name: "Test", Models: models})
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return text, id
}

// TestTheResponseCacheIsPerAccount: two accounts sending the same body have different entries. A key of one account can
// never read the other's, even when the request bytes are identical.
func TestTheResponseCacheIsPerAccount(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	headers := []string{"X-Openrouter-Cache", "true"}
	one, two := r.funded(1e12), r.funded(1e12)
	for i, a := range []account{one, two} {
		if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
			t.Fatalf("%d: %s", resp.StatusCode, body)
		}
		want := int64(i + 1)
		waitFor(t, "the account's entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == want })
		if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
			t.Fatalf("%d: %s", resp.StatusCode, body)
		}
	}
	if n := sends(up); n != 2 {
		t.Errorf("%d upstream sends, want one miss per account", n)
	}
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 2 {
		t.Errorf("%d cache rows, want one per account", n)
	}
}

// TestTheResponseCacheSeparatesStreaming: the same body on the same account has one entry for a stream and another for a
// whole answer, because the two relays write different bytes.
func TestTheResponseCacheSeparatesStreaming(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true"}
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(messageBody("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "both entries", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 2 })
	if n := sends(up); n != 2 {
		t.Errorf("%d upstream sends, want one per streaming mode", n)
	}
}

// TestClearingTheResponseCache: a clear request deletes the entry before its lookup, so it goes upstream and fills the
// entry again; the next ordinary request hits the new entry.
func TestClearingTheResponseCache(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true"}
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "the first entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	clear := append(append([]string{}, headers...), "X-Openrouter-Cache-Clear", "true")
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), clear...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get(cacheStatusHeader); got != "MISS" {
		t.Errorf("clear answered %q, want a miss", got)
	}
	waitFor(t, "the replacement entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "HIT" {
		t.Errorf("after the clear: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), body)
	}
}

// TestAnExpiredEntryIsAMiss: the row's expiry is what decides, not a timer: a row past it is invisible at once and the
// next call goes upstream.
func TestAnExpiredEntryIsAMiss(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true"}
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "the entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	r.sql("UPDATE gateway.response_cache SET created_at = now() - interval '2 seconds', expires_at = now() - interval '1 second'")
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "MISS" {
		t.Errorf("expired: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), body)
	}
	if n := sends(up); n != 2 {
		t.Errorf("%d upstream sends, want a miss after expiry", n)
	}
}

// TestAFailedAnswerIsNotCached: only a successful, complete upstream answer is stored.
func TestAFailedAnswerIsNotCached(t *testing.T) {
	t.Parallel()
	r := ledgerRigNoCacheWriter(t)
	up := withExecutor(r, &flakyLedger{})
	for _, e := range up.eps {
		e.script = []scripted{failure(routing.ScopeEndpoint, false)}
	}
	a := r.funded(1e12)
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), "X-Openrouter-Cache", "true")
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("the failure answered 200:\n%s", body)
	}
	r.handler.Outstanding.Drain(t.Context(), time.Minute)
	// A failed answer is never offered to the writer: with the writer stopped, the queue itself proves it.
	if n := len(r.cache.queue); n != 0 {
		t.Errorf("%d queued writes after a failed answer", n)
	}
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 0 {
		t.Errorf("%d entries after a failed answer", n)
	}
}

// TestTheCachedReplyIsEncryptedAtRest: the row holds the upstream's bytes only as ciphertext under the envelope keyring,
// bound to this row and purpose; the plaintext is not in the table.
func TestTheCachedReplyIsEncryptedAtRest(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), "X-Openrouter-Cache", "true"); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "the entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	var (
		id                            uuid.UUID
		ciphertext, nonce, wrappedDEK []byte
		keyVersion                    uint32
		size                          int
	)
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id, ciphertext, nonce, wrapped_dek, key_version, body_size FROM gateway.response_cache`).
			Scan(&id, &ciphertext, &nonce, &wrappedDEK, &keyVersion, &size)
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("message_start")) || bytes.Contains(ciphertext, []byte("text_delta")) {
		t.Error("the cached reply is plaintext in the table")
	}
	opened, err := r.keys.Open(secret.Sealed{Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrappedDEK, KeyVersion: keyVersion},
		secret.Location{Table: "gateway.response_cache", RowID: id.String(), Purpose: "body"})
	if err != nil {
		t.Fatal(err)
	}
	payload := opened.Reveal()
	if len(payload) < 4 {
		t.Fatalf("opened payload is %d bytes", len(payload))
	}
	sep := bytes.IndexByte([]byte(payload), 0)
	if sep < 0 || sep+1+size != len(payload) || size == 0 {
		t.Fatalf("open: separator %d, payload %d, size %d", sep, len(payload), size)
	}
	if !strings.Contains(payload[sep+1:], "message_start") {
		t.Error("the opened reply is not the upstream's stream")
	}
}

// TestACorruptCacheEntryIsAMissAndIsDeleted: an entry that no longer opens is removed and the call goes upstream.
func TestACorruptCacheEntryIsAMissAndIsDeleted(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true"}
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "the entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	r.sql("UPDATE gateway.response_cache SET body_size = body_size + 1")
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), headers...)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "MISS" {
		t.Fatalf("corrupt entry: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), body)
	}
	if n := sends(up); n != 2 {
		t.Errorf("%d upstream sends, want the corrupt entry to be a miss", n)
	}
	waitFor(t, "the replacement", func() bool {
		return r.count("SELECT count(*) FROM gateway.response_cache WHERE body_size > 0") == 1
	})
}

// TestABrokenStreamIsNotCached: only a stream the upstream ended normally is stored.
func TestABrokenStreamIsNotCached(t *testing.T) {
	t.Parallel()
	r := ledgerRigNoCacheWriter(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, true)}
	}
	a := r.funded(1e12)
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")), "X-Openrouter-Cache", "true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	r.handler.Outstanding.Drain(t.Context(), time.Minute)
	// A broken stream is never offered to the writer either: the stopped writer leaves any offer in the queue.
	if n := len(r.cache.queue); n != 0 {
		t.Errorf("%d queued writes after a broken stream", n)
	}
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 0 {
		t.Errorf("%d entries after a broken stream", n)
	}
}

// TestWhitespaceOutsideStringsIsTheSameCacheKey: the normalized body decides, so formatting alone does not fill a second
// entry.
func TestWhitespaceOutsideStringsIsTheSameCacheKey(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 2, 20, false)}
	}
	a := r.funded(1e12)
	headers := []string{"X-Openrouter-Cache", "true"}
	one := stream("acme/beta")
	two := strings.Replace(one, `{`, `{  `, 1)
	if resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(one), headers...); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	waitFor(t, "the entry", func() bool { return r.count("SELECT count(*) FROM gateway.response_cache") == 1 })
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(two), headers...)
	if resp.StatusCode != http.StatusOK || resp.Header.Get(cacheStatusHeader) != "HIT" {
		t.Fatalf("formatted: %d %q\n%s", resp.StatusCode, resp.Header.Get(cacheStatusHeader), body)
	}
	if n := sends(up); n != 1 {
		t.Errorf("%d upstream sends, want the two bodies to share an entry", n)
	}
}

func TestCacheCaptureStopsAtTheEntryLimit(t *testing.T) {
	t.Parallel()
	s := &session{x: &Executor{Memory: protocol.NewMemory(4 << 20)}}
	c := newCacheCapture(s, io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxCacheEntry+1))))
	if c == nil {
		t.Fatal("no capture")
	}
	buf := make([]byte, 64<<10)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	if !c.overflow || len(c.buf) != 0 || s.cacheHeld != 0 {
		t.Errorf("overflow %v, held %d, buffer %d; want the whole charge back", c.overflow, s.cacheHeld, len(c.buf))
	}
}
