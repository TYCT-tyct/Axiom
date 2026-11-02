package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
)

const (
	// cacheQueueSize bounds the replies waiting to be sealed and written. A full queue drops the new reply: a cache is
	// never worth failing or slowing the call that filled it.
	cacheQueueSize = 64
	// cacheAccountBytes and cacheAccountEntries bound one account's stored replies. The oldest rows are evicted first.
	cacheAccountBytes   = 64 << 20
	cacheAccountEntries = 1024
	cacheStateLimit     = 8192
	// cacheLookupTimeout keeps a slow database from turning a cache lookup into a slow call: a timeout is a miss.
	cacheLookupTimeout = 2 * time.Second
	cacheWriteTimeout  = 5 * time.Second
	cacheCleanupEvery  = time.Minute
	cacheCleanupBatch  = 1000
)

// ResponseCache stores successful upstream replies for an account, sealed with the platform keyring. It is optional on an
// Executor: a nil cache leaves every call uncached, which is how tests and a node without the feature run.
type ResponseCache struct {
	db       *pg.DB
	keys     *secret.Keyring
	platform pg.Scope // the scope of expiry cleanup, built by cmd/ with the platform's authority
	logger   *slog.Logger
	now      func() time.Time
	queue    chan cacheEntry
	events   metric.Int64Counter
	opts     map[string]metric.MeasurementOption
}

// cacheEntry is one reply waiting to be written, or read back. The body is the upstream's own reply, before this node
// rewrote a byte of it.
type cacheEntry struct {
	scope         pg.Scope
	site, account uuid.UUID
	key           [32]byte
	source        uuid.UUID
	format        protocol.Format
	model, state  string
	endpoint      uuid.UUID
	body          []byte
	ttl           time.Duration
}

// cachedResponse is a live row after it has been opened.
type cachedResponse struct {
	id                   uuid.UUID
	source               uuid.UUID
	format               protocol.Format
	model, state         string
	endpoint             uuid.UUID
	body                 []byte
	held                 int64
	createdAt, expiresAt time.Time
}

// release gives back the node's budget the lookup charged for this entry. The caller must hold it until the replay has
// consumed the body.
func (r *cachedResponse) release(memory *protocol.Memory) {
	if r.held > 0 && memory != nil {
		memory.Refund(r.held)
		r.held = 0
	}
}

// NewResponseCache builds the cache and its counters. It does not start the writer; Run owns it.
func NewResponseCache(db *pg.DB, keys *secret.Keyring, platform pg.Scope, logger *slog.Logger, meter metric.Meter) (*ResponseCache, error) {
	if db == nil || keys == nil {
		return nil, errors.New("gateway: the response cache needs a database and a keyring")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("gateway")
	}
	c := &ResponseCache{
		db: db, keys: keys, platform: platform, logger: logger, now: time.Now,
		queue: make(chan cacheEntry, cacheQueueSize),
	}
	events, err := meter.Int64Counter("axiom.gateway.response_cache.requests",
		metric.WithDescription("Response-cache operations, by result: hit, miss, store, store_error, lookup_error, replay_error, dropped."))
	if err != nil {
		return nil, err
	}
	c.events = events
	c.opts = make(map[string]metric.MeasurementOption, 7)
	for _, result := range []string{"hit", "miss", "store", "store_error", "lookup_error", "replay_error", "dropped"} {
		c.opts[result] = metric.WithAttributes(attribute.String("result", result))
	}
	return c, nil
}

// Run writes queued replies and deletes expired rows until ctx ends. A deployment starts it with the other loops.
func (c *ResponseCache) Run(ctx context.Context) {
	go c.writeLoop(ctx)
	tick := time.NewTicker(cacheCleanupEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.cleanup(ctx)
		}
	}
}

func (c *ResponseCache) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-c.queue:
			c.store(ctx, entry)
		}
	}
}

// Offer queues a reply for writing without waiting. It copies the body, so the caller may release its own buffer as soon
// as it returns. A full queue drops the copy and counts the loss, and never touches the call; the queue's worst case is
// cacheQueueSize replies of maxCacheEntry.
func (c *ResponseCache) Offer(ctx context.Context, entry cacheEntry) {
	if c == nil || len(entry.body) == 0 {
		return
	}
	entry.body = append([]byte(nil), entry.body...)
	select {
	case c.queue <- entry:
	default:
		c.count(ctx, "dropped")
		c.logger.Warn("a response-cache entry was dropped: the write queue is full", "account_id", entry.account)
	}
}

func (c *ResponseCache) count(ctx context.Context, result string) {
	if c == nil || c.events == nil {
		return
	}
	if opt, ok := c.opts[result]; ok {
		c.events.Add(ctx, 1, opt)
	}
}

// Lookup opens one account's entry. A missing, expired, corrupt or unreadable row is a miss, not a failure of the call:
// the cache may only make a reply cheaper, never turn a valid request into an error.
//
//nolint:cyclop,funlen // one lookup: metadata, memory admission, decrypt, bounds check
func (c *ResponseCache) Lookup(ctx context.Context, scope pg.Scope, account uuid.UUID, key [32]byte, memory *protocol.Memory) (*cachedResponse, bool) {
	if c == nil || memory == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, cacheLookupTimeout)
	defer cancel()
	var (
		row       cachedResponse
		size      int
		cipherLen int
		format    int16
	)
	// Read only the metadata first: the node's budget is charged before the ciphertext and the plaintext are allocated.
	err := c.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, source_id, format, model, endpoint, body_size, octet_length(ciphertext), created_at, expires_at
			FROM gateway.response_cache
			WHERE account_id = $1 AND cache_key = $2 AND expires_at > $3`,
			account, key[:], c.now(),
		).Scan(&row.id, &row.source, &format, &row.model, &row.endpoint, &size, &cipherLen,
			&row.createdAt, &row.expiresAt)
	})
	if errors.Is(err, pg.ErrNoRows) {
		c.count(ctx, "miss")
		return nil, false
	}
	if err != nil {
		c.count(ctx, "lookup_error")
		c.logger.Warn("a response-cache lookup failed; treating it as a miss", "err", err)
		return nil, false
	}
	// ciphertext, the opened payload and its []byte copy are all alive together; the state and the decoder are bounded
	// by cacheStateLimit. Charge for that whole peak, not for the plaintext alone.
	hold := int64(cipherLen + 2*size + 2*cacheStateLimit + 64<<10)
	if !memory.Charge(hold) {
		c.count(ctx, "miss")
		return nil, false
	}
	kept := false
	defer func() {
		if !kept {
			memory.Refund(hold)
		}
	}()
	var (
		seal    secret.Sealed
		payload []byte
		again   uuid.UUID
		againN  int
	)
	err = c.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, body_size, ciphertext, nonce, wrapped_dek, key_version
			FROM gateway.response_cache
			WHERE account_id = $1 AND cache_key = $2 AND expires_at > $3`,
			account, key[:], c.now(),
		).Scan(&again, &againN, &seal.Ciphertext, &seal.Nonce, &seal.WrappedDEK, &seal.KeyVersion)
	})
	if errors.Is(err, pg.ErrNoRows) {
		c.count(ctx, "miss")
		return nil, false
	}
	if err != nil {
		c.count(ctx, "lookup_error")
		c.logger.Warn("a response-cache lookup failed; treating it as a miss", "err", err)
		return nil, false
	}
	loc := secret.Location{Table: "gateway.response_cache", RowID: row.id.String(), Purpose: "body"}
	opened, err := c.keys.Open(seal, loc)
	if err == nil {
		payload = []byte(opened.Reveal())
	}
	state, body, ok := decodeCachedPayload(payload, size)
	if err != nil || !ok || !cacheFormat(format) {
		c.count(ctx, "lookup_error")
		c.logger.Warn("a response-cache entry could not be opened; treating it as a miss",
			"row_id", row.id, "err", err)
		c.deleteRow(ctx, scope, row.id)
		return nil, false
	}
	row.format, row.state, row.body = protocol.Format(uint8(format)), state, body //nolint:gosec // cacheFormat bounds format to the three known formats
	row.held = hold
	kept = true
	return &row, true
}

// decodeCachedPayload splits the sealed payload into the turn state and the upstream bytes, checking both bounds before
// either is used. A malformed payload is a miss, never an error of the call.
func decodeCachedPayload(payload []byte, size int) (state string, body []byte, ok bool) {
	sep := bytes.IndexByte(payload, 0)
	if sep < 0 || sep > cacheStateLimit {
		return "", nil, false
	}
	body = payload[sep+1:]
	if len(body) != size || len(body) == 0 || len(body) > maxCacheEntry {
		return "", nil, false
	}
	return string(payload[:sep]), body, true
}

// Delete removes one account's entry for key. A missing entry is success: the caller is about to overwrite it anyway.
func (c *ResponseCache) Delete(ctx context.Context, scope pg.Scope, account uuid.UUID, key [32]byte) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cacheLookupTimeout)
	defer cancel()
	err := c.db.Tx(ctx, scope, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM gateway.response_cache WHERE account_id = $1 AND cache_key = $2`,
			account, key[:])
		return err
	})
	if err != nil {
		c.logger.Warn("a response-cache clear failed; the old entry may remain until it expires", "err", err)
	}
}

func (c *ResponseCache) deleteRow(ctx context.Context, scope pg.Scope, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(ctx, cacheLookupTimeout)
	defer cancel()
	if err := c.db.Tx(ctx, scope, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM gateway.response_cache WHERE id = $1`, id)
		return err
	}); err != nil {
		c.logger.Warn("a corrupt response-cache row could not be deleted", "row_id", id, "err", err)
	}
}

// store seals and writes one reply, evicting this account's oldest rows to stay inside its bounds. It runs on the cache's
// own goroutine, never on a call's.
func (c *ResponseCache) store(ctx context.Context, entry cacheEntry) {
	if len(entry.body) == 0 || len(entry.body) > maxCacheEntry || len(entry.state) > cacheStateLimit ||
		entry.ttl < time.Second || entry.ttl > cacheMaxTTL {
		return
	}
	id := uuid.NewV7()
	loc := secret.Location{Table: "gateway.response_cache", RowID: id.String(), Purpose: "body"}
	// The state is an HTTP header value, so it cannot contain NUL: the first NUL separates it from the body.
	payload := make([]byte, 0, len(entry.state)+1+len(entry.body))
	payload = append(append(append(payload, entry.state...), 0), entry.body...)
	seal := c.keys.Seal(secret.New(string(payload)), loc)
	now := c.now()
	expires := now.Add(entry.ttl)
	ctx, cancel := context.WithTimeout(ctx, cacheWriteTimeout)
	defer cancel()
	err := c.db.Tx(ctx, entry.scope, func(tx pg.Tx) error {
		// One writer per account: the row count and byte total below cannot be raced by another store.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, entry.account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.response_cache WHERE account_id = $1 AND cache_key = $2`,
			entry.account, entry.key[:]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.response_cache WHERE account_id = $1 AND expires_at <= $2`,
			entry.account, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			WITH doomed AS (
				SELECT id, body_size,
				       row_number() OVER (ORDER BY created_at DESC, id DESC) AS rn,
				       sum(body_size) OVER (ORDER BY created_at DESC, id DESC ROWS UNBOUNDED PRECEDING) AS used
				FROM gateway.response_cache WHERE account_id = $1
			)
			DELETE FROM gateway.response_cache WHERE id IN (
				SELECT id FROM doomed WHERE rn > $2 OR used > $3
			)`, entry.account, cacheAccountEntries-1, cacheAccountBytes-len(entry.body)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO gateway.response_cache
				(id, site_id, account_id, cache_key, source_id, format, model, endpoint, ciphertext, nonce,
				 wrapped_dek, key_version, body_size, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			id, entry.site, entry.account, entry.key[:], entry.source, int16(entry.format), entry.model,
			entry.endpoint, seal.Ciphertext, seal.Nonce, seal.WrappedDEK, seal.KeyVersion, len(entry.body), now,
			expires)
		return err
	})
	if err != nil {
		c.count(ctx, "store_error")
		c.logger.Warn("a response-cache entry could not be written", "account_id", entry.account, "err", err)
		return
	}
	c.count(ctx, "store")
}

// cleanup deletes expired rows in bounded batches. A row that lives past its TTL is already invisible to Lookup; this
// only gives the space back.
func (c *ResponseCache) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, cacheWriteTimeout)
	defer cancel()
	for range 10 {
		var deleted int64
		err := c.db.Tx(ctx, c.platform, func(tx pg.Tx) error {
			tag, err := tx.Exec(ctx, `
				DELETE FROM gateway.response_cache WHERE id IN (
					SELECT id FROM gateway.response_cache WHERE expires_at <= $1 LIMIT $2
				)`, c.now(), cacheCleanupBatch)
			if err == nil {
				deleted = tag.RowsAffected()
			}
			return err
		})
		if err != nil {
			c.logger.Warn("expired response-cache rows could not be deleted", "err", err)
			return
		}
		if deleted < cacheCleanupBatch {
			return
		}
	}
}

// cacheFormat reports whether a stored format is one this node knows how to replay.
func cacheFormat(format int16) bool {
	switch format {
	case int16(protocol.Chat), int16(protocol.Responses), int16(protocol.Messages):
		return true
	default:
		return false
	}
}
