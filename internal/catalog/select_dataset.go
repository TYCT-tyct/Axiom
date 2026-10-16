package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// SelectDatasetSourceRequest moves a model to a dataset as its price source.
type SelectDatasetSourceRequest struct {
	Op
	Model              ModelID     `json:"model"`
	Source             PriceSource `json:"source"`
	ConfirmBasisChange bool        `json:"confirm_basis_change"`
}

// SelectDatasetSource switches a model to litellm or openrouter: the dollar price is the dataset's entry for the model,
// and SelectPriceSource's checks apply to it, so no price the model has now is lost and the other currencies are
// withdrawn. The entry is found as the sync finds it, and must be unambiguous, usable and sane. Its tiers are taken
// only if the current price has the same thresholds, so a model with tiers that has no price yet starts from a
// published price, or from the other source.
func (c *Catalog) SelectDatasetSource(ctx context.Context, f Fetched, r SelectDatasetSourceRequest) (Result, error) {
	entries, err := parseDataset(f.Source, f.Body)
	if err != nil {
		return Result{}, err
	}
	if r.Source != f.Source {
		return Result{}, fmt.Errorf("%w: the request selects %s, the dataset is %s", ErrSourceMismatch, r.Source, f.Source)
	}
	m, err := c.followerByID(ctx, r.Model)
	if err != nil {
		return Result{}, err
	}
	entry, err := pickEntry(indexEntries(entries), m)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", m.row.Slug, err)
	}
	var current *Schedule
	if m.has {
		current = m.head.schedule
	}
	next, err := entry.schedule(current)
	if err == nil {
		err = sane(next)
	}
	if err != nil {
		return Result{}, fmt.Errorf("%s: entry %s: %w", m.row.Slug, entry.Key, err)
	}
	sum := sha256.Sum256(f.Body)
	bases := make(map[string]uuid.UUID, len(m.heads))
	for currency, head := range m.heads {
		bases[currency] = head.id
	}
	return c.selectSource(ctx, SelectPriceSourceRequest{
		Op: r.Op, Model: r.Model, Rev: m.row.Rev, Source: r.Source, Bases: bases, ConfirmBasisChange: r.ConfirmBasisChange,
		Prices: []SourcePrice{{Schedule: mustJSON(next), Evidence: datasetEvidence(f, hex.EncodeToString(sum[:]), entry)}},
	})
}

// followerByID reads one model as the sync does. The switch locks the row and compares rev and the heads again, so a
// change after this read is refused, not overwritten.
func (c *Catalog) followerByID(ctx context.Context, id ModelID) (syncModel, error) {
	var m syncModel
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) error {
		row, err := New().GetModel(ctx, tx, uuid.UUID(id))
		if err != nil {
			return err
		}
		m, err = followerOf(ctx, tx, row)
		return err
	})
	if errors.Is(err, pg.ErrNoRows) {
		return syncModel{}, ErrModelNotFound
	}
	return m, err
}
