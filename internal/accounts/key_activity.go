package accounts

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

type keyUse struct {
	key uuid.UUID
	at  time.Time
}

// observeKeyUse never waits for storage or a background writer. Activity is a hint, not an audit record.
func (s *Service) observeKeyUse(c Caller) {
	select {
	case s.keyUses <- keyUse{uuid.UUID(c.key), s.now()}:
	default:
	}
}

func (s *Service) trackKeyUses(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.flushKeyUses(ctx); err != nil && ctx.Err() == nil {
				logger.Error("write API key activity", "err", err)
			}
		}
	}
}

func (s *Service) flushKeyUses(ctx context.Context) error {
	n := len(s.keyUses)
	if n == 0 {
		return nil
	}
	keys, times := make([]uuid.UUID, n), make([]string, n)
	for i := range n {
		use := <-s.keyUses
		keys[i], times[i] = use.key, use.at.Format(time.RFC3339Nano)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.db.Tx(ctx, s.platform, func(tx pg.Tx) error {
		return s.queries.TouchKeys(ctx, tx, TouchKeysParams{Keys: keys, UsedAt: times})
	})
}

func (s *Service) readKeyMetadata(ctx context.Context, tx pg.Tx, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(keys))
	for i, k := range keys {
		ids[i] = uuid.UUID(k.ID)
	}
	rows, err := s.queries.ReadKeyMetadata(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("read API key metadata: %w", err)
	}
	byID := make(map[uuid.UUID]ReadKeyMetadataRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for i := range keys {
		row := byID[ids[i]]
		keys[i].CreatorName, keys[i].LastUsedAt = row.CreatorName, row.LastUsedAt
	}
	return nil
}
