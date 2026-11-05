package routing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// ErrPresetsSuspended is a preset or default write, or a key's choice of a preset, while the platform has suspended
// routing presets (SetSuspension).
var ErrPresetsSuspended = errors.New("routing: routing presets are suspended by the platform")

// ReadSuspension is since when the platform has suspended routing presets, zero when it has not. Every scope may read
// it.
func ReadSuspension(ctx context.Context, tx pg.Tx) (time.Time, error) {
	var since time.Time
	err := tx.QueryRow(ctx, `SELECT since FROM routing.preset_suspension WHERE singleton`).Scan(&since)
	if errors.Is(err, pg.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("routing: read the preset suspension: %w", err)
	}
	return since, nil
}

// SetSuspension is the platform's kill switch on routing presets (docs/domains/routing.md「滚动发布、失败与性能」):
// suspended, every node's next snapshot holds the system preset alone, so the keys bound to any other preset are
// refused rather than routed by a wider one, and no preset, default or key's choice of a preset is written until it is
// resumed. It runs in the platform scope, records a change with the actor's reason, and returns since when presets are
// suspended, zero when they are not. Asking for the state they are in changes and records nothing. It takes the budget
// row first, as every preset write does (lockWrites), so a write either commits before the suspension or is refused.
func SetSuspension(ctx context.Context, tx pg.Tx, actor audit.Actor, suspend bool, reason string) (time.Time, error) {
	if err := lockBudget(ctx, tx); err != nil {
		return time.Time{}, err
	}
	before, err := ReadSuspension(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	if suspend == !before.IsZero() {
		return before, nil
	}
	query, action := `DELETE FROM routing.preset_suspension`, "routing.presets.resume"
	if suspend {
		query, action = `INSERT INTO routing.preset_suspension DEFAULT VALUES`, "routing.presets.suspend"
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		return time.Time{}, presetWriteErr("change the preset suspension", err)
	}
	after, err := ReadSuspension(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	return after, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "routing", Kind: "preset_suspension", ID: uuid.Nil().String()},
	})
}

// lockWrites takes the budget row, which orders every preset and default write with the others and with
// SetSuspension, and refuses the write while presets are suspended.
func lockWrites(ctx context.Context, tx pg.Tx) error {
	if err := lockBudget(ctx, tx); err != nil {
		return err
	}
	return refuseSuspended(ctx, tx)
}

func refuseSuspended(ctx context.Context, tx pg.Tx) error {
	since, err := ReadSuspension(ctx, tx)
	if err != nil {
		return err
	}
	if !since.IsZero() {
		return ErrPresetsSuspended
	}
	return nil
}
