package gateway

import (
	"bytes"
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// TestEvictionKeepsTheNewestRows: an account over either bound loses its oldest replies, never the one just written.
func TestEvictionKeepsTheNewestRows(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	a := r.newAccount(0)
	r.insertResponseCacheRows(a, cacheAccountEntries+6, 1)
	r.cache.store(t.Context(), cacheEntry{
		scope: pg.NewScope(false, nil, []uuid.UUID{a.id}), site: siteID, account: a.id, key: [32]byte{0xaa},
		source: uuid.NewV7(), format: protocol.Chat, model: "m", endpoint: uuid.NewV7(), body: []byte("x"), ttl: time.Minute,
	})
	if got := r.count("SELECT count(*) FROM gateway.response_cache"); got != cacheAccountEntries {
		t.Errorf("%d entries, want %d", got, cacheAccountEntries)
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE cache_key = decode(md5('1') || md5('2'), 'hex')"); got != 1 {
		t.Error("the newest old row was evicted by the count limit")
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE cache_key = decode(md5('1030') || md5('1031'), 'hex')"); got != 0 {
		t.Error("the oldest row survived the count limit")
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE cache_key = decode('aa' || repeat('00', 31), 'hex')"); got != 1 {
		t.Error("the row just written was evicted by the count limit")
	}

	b := r.newAccount(0)
	r.insertResponseCacheRows(b, 70, 1<<20)
	r.cache.store(t.Context(), cacheEntry{
		scope: pg.NewScope(false, nil, []uuid.UUID{b.id}), site: siteID, account: b.id, key: [32]byte{0xbb},
		source: uuid.NewV7(), format: protocol.Chat, model: "m", endpoint: uuid.NewV7(), body: []byte("x"), ttl: time.Minute,
	})
	if got := r.count("SELECT coalesce(sum(body_size), 0) FROM gateway.response_cache WHERE account_id = '" + b.id.String() + "'"); got > cacheAccountBytes {
		t.Errorf("%d bytes, want at most %d", got, cacheAccountBytes)
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE account_id = '" + b.id.String() + "' AND cache_key = decode(md5('1') || md5('2'), 'hex')"); got != 1 {
		t.Error("the newest old row was evicted by the byte limit")
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE account_id = '" + b.id.String() + "' AND cache_key = decode(md5('70') || md5('71'), 'hex')"); got != 0 {
		t.Error("the oldest row survived the byte limit")
	}
	if got := r.count("SELECT count(*) FROM gateway.response_cache WHERE account_id = '" + b.id.String() + "' AND cache_key = decode('bb' || repeat('00', 31), 'hex')"); got != 1 {
		t.Error("the row just written was evicted by the byte limit")
	}
}

func (r *rig) insertResponseCacheRows(a account, n, size int) {
	r.t.Helper()
	r.sql(fmt.Sprintf(`
		INSERT INTO gateway.response_cache
			(id, site_id, account_id, cache_key, source_id, format, model, endpoint, ciphertext, nonce, wrapped_dek,
			 key_version, body_size, created_at, expires_at)
		SELECT gen_random_uuid(), '%s', '%s', decode(md5(g::text) || md5((g+1)::text), 'hex'), gen_random_uuid(),
		       0, 'm', gen_random_uuid(), decode('00', 'hex'), decode(repeat('00', 12), 'hex'),
		       decode(repeat('00', 60), 'hex'), 1, %d, now() - make_interval(secs => g), now() + interval '1 hour'
		FROM generate_series(1, %d) AS g`, siteID, a.id, size, n))
}

// TestACacheLookupReservesTheNodesMemory: a hit charges the ciphertext, plaintext and copy it holds before allocating
// them, and a node without room for that peak treats the entry as a miss instead of overdrawing its budget.
func TestACacheLookupReservesTheNodesMemory(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	a := r.newAccount(0)
	scope := pg.NewScope(false, nil, []uuid.UUID{a.id})
	key := [32]byte{4}
	body := bytes.Repeat([]byte("x"), 1024)
	r.cache.store(t.Context(), cacheEntry{
		scope: scope, site: siteID, account: a.id, key: key, source: uuid.NewV7(),
		format: protocol.Chat, model: "m", endpoint: uuid.NewV7(), body: body, ttl: time.Minute,
	})
	tiny := protocol.NewMemory(1)
	if got, ok := r.cache.Lookup(t.Context(), scope, a.id, key, tiny); ok {
		got.release(tiny)
		t.Fatal("a lookup with no room succeeded")
	}
	mem := protocol.NewMemory(4 << 20)
	got, ok := r.cache.Lookup(t.Context(), scope, a.id, key, mem)
	if !ok {
		t.Fatal("the entry was not readable with room for it")
	}
	if got.held <= int64(len(body)) {
		t.Errorf("the lookup held %d bytes for a %d-byte body, want its whole peak", got.held, len(body))
	}
	got.release(mem)
}

// TestTheCachedStateIsEncryptedWithTheBody: the turn state is content-adjacent, so it travels in the same sealed
// payload as the upstream bytes and never appears as a plaintext column.
func TestTheCachedStateIsEncryptedWithTheBody(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	a := r.newAccount(0)
	scope := pg.NewScope(false, nil, []uuid.UUID{a.id})
	key := [32]byte{3}
	r.cache.store(t.Context(), cacheEntry{
		scope: scope, site: siteID, account: a.id, key: key, source: uuid.NewV7(),
		format: protocol.Chat, model: "m", state: "secret-turn-state", body: []byte("upstream-bytes"), ttl: time.Minute,
	})
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 1 {
		t.Fatalf("%d entries, want one", n)
	}
	var ciphertext []byte
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ciphertext FROM gateway.response_cache`).Scan(&ciphertext)
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("secret-turn-state")) || bytes.Contains(ciphertext, []byte("upstream-bytes")) {
		t.Fatal("the cache row contains plaintext content or state")
	}
	got, ok := r.cache.Lookup(t.Context(), scope, a.id, key, protocol.NewMemory(4<<20))
	if !ok {
		t.Fatal("the entry was not readable")
	}
	if got.state != "secret-turn-state" || string(got.body) != "upstream-bytes" {
		t.Fatalf("got state %q body %q", got.state, got.body)
	}
}

// TestTheCacheStoreRefusesOversizedAndOverlongEntries: the unencrypted body and the TTL are bounded before the row exists.
func TestTheCacheStoreRefusesOversizedAndOverlongEntries(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	a := r.newAccount(0)
	scope := pg.NewScope(false, nil, []uuid.UUID{a.id})
	base := cacheEntry{
		scope: scope, site: siteID, account: a.id, key: [32]byte{1}, source: uuid.NewV7(),
		format: protocol.Chat, model: "m", ttl: time.Minute,
	}
	big := base
	big.body = bytes.Repeat([]byte("x"), maxCacheEntry+1)
	r.cache.store(t.Context(), big)
	over := base
	over.key = [32]byte{2}
	over.body = []byte("small")
	over.ttl = cacheMaxTTL + time.Second
	r.cache.store(t.Context(), over)
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 0 {
		t.Errorf("%d entries, want none", n)
	}
	ok := base
	ok.body = []byte("small")
	r.cache.store(t.Context(), ok)
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 1 {
		t.Fatalf("%d entries, want one", n)
	}
	r.cache.cleanup(t.Context())
	if n := r.count("SELECT count(*) FROM gateway.response_cache"); n != 1 {
		t.Errorf("cleanup removed a live entry")
	}
}
