package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// CacheQuery selects response-cache rows for a controlled content export. The range is [Since, Until), optionally
// narrowed to one site or account; a zero UUID means every tenant.
type CacheQuery struct {
	Since, Until  time.Time
	Site, Account uuid.UUID
}

// CacheMeta is one response-cache row. The cache stores a turn state and the upstream reply in one sealed payload;
// Size is the plaintext payload size and Path is the export's unique tar entry.
type CacheMeta struct {
	ID        uuid.UUID
	Site      uuid.UUID
	Account   uuid.UUID
	Source    uuid.UUID
	Format    uint8
	Model     string
	CreatedAt time.Time
	ExpiresAt time.Time
	Size      int64
	Path      string
}

// CacheStats is what a cache metadata scan covered.
type CacheStats struct {
	Count, Bytes int64
}

// StreamCacheMeta walks response-cache rows in a stable order for the export's metadata manifest. The caller supplies
// the platform-scope transaction; the cache body is not read until the export reaches it.
func StreamCacheMeta(ctx context.Context, tx pg.Tx, q CacheQuery, each func(CacheMeta) error) (CacheStats, error) {
	if each == nil {
		return CacheStats{}, errors.New("gateway: cache export needs a callback")
	}
	if q.Since.IsZero() || !q.Until.After(q.Since) {
		return CacheStats{}, errors.New("gateway: cache export needs a non-empty time range")
	}
	rows, err := tx.Query(ctx, `
		SELECT id, site_id, account_id, source_id, format, model, created_at, expires_at,
		       octet_length(ciphertext) - 16
		FROM gateway.response_cache
		WHERE created_at >= $1 AND created_at < $2
		  AND ($3::uuid IS NULL OR site_id = $3)
		  AND ($4::uuid IS NULL OR account_id = $4)
		ORDER BY created_at, account_id, id`,
		q.Since, q.Until, optionalCacheUUID(q.Site), optionalCacheUUID(q.Account))
	if err != nil {
		return CacheStats{}, fmt.Errorf("gateway: read response cache: %w", err)
	}
	defer rows.Close()
	var stats CacheStats
	for rows.Next() {
		var m CacheMeta
		var format int16
		if err := rows.Scan(&m.ID, &m.Site, &m.Account, &m.Source, &format, &m.Model, &m.CreatedAt,
			&m.ExpiresAt, &m.Size); err != nil {
			return CacheStats{}, fmt.Errorf("gateway: read a response-cache row: %w", err)
		}
		if format < 0 || format > 255 {
			return CacheStats{}, fmt.Errorf("gateway: response cache %s has format %d", m.ID, format)
		}
		if m.Size <= 0 {
			return CacheStats{}, fmt.Errorf("gateway: response cache %s has size %d", m.ID, m.Size)
		}
		m.Format = uint8(format) // #nosec G115 -- the bounds check above keeps the conversion in range.
		m.Path = "cache/" + m.ID.String()
		stats.Count++
		stats.Bytes += m.Size
		if err := each(m); err != nil {
			return CacheStats{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return CacheStats{}, fmt.Errorf("gateway: read response cache: %w", err)
	}
	return stats, nil
}

// ReadCachePayload opens one cache row. The returned bytes are the complete plaintext payload: the turn state, a NUL,
// and the upstream reply. The caller verifies the manifest size and writes the bytes unchanged.
func ReadCachePayload(ctx context.Context, tx pg.Tx, keys *secret.Keyring, m CacheMeta) ([]byte, error) {
	if keys == nil {
		return nil, errors.New("gateway: cache export needs the platform keyring")
	}
	var (
		sealed   secret.Sealed
		bodySize int
	)
	if err := tx.QueryRow(ctx, `
		SELECT ciphertext, nonce, wrapped_dek, key_version, body_size
		FROM gateway.response_cache
		WHERE id = $1 AND site_id = $2 AND account_id = $3`, m.ID, m.Site, m.Account).
		Scan(&sealed.Ciphertext, &sealed.Nonce, &sealed.WrappedDEK, &sealed.KeyVersion, &bodySize); err != nil {
		return nil, fmt.Errorf("gateway: read response cache %s: %w", m.ID, err)
	}
	opened, err := keys.Open(sealed, secret.Location{Table: "gateway.response_cache", RowID: m.ID.String(), Purpose: "body"})
	if err != nil {
		return nil, fmt.Errorf("gateway: open response cache %s: %w", m.ID, err)
	}
	payload := []byte(opened.Reveal())
	nul := bytes.IndexByte(payload, 0)
	if nul < 0 || len(payload)-nul-1 != bodySize {
		return nil, fmt.Errorf("gateway: response cache %s has an invalid payload", m.ID)
	}
	if int64(len(payload)) != m.Size {
		return nil, fmt.Errorf("gateway: response cache %s is %d bytes, the manifest says %d", m.ID, len(payload), m.Size)
	}
	return payload, nil
}

func optionalCacheUUID(id uuid.UUID) any {
	if id == (uuid.UUID{}) {
		return nil
	}
	return id
}
