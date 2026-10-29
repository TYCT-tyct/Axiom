package alert

import (
	"context"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// LockPosition serializes a journal bridge. SavePosition and Persist must use this transaction,
// so a crash can neither skip an event nor replay it after its recovery was consumed.
func LockPosition(ctx context.Context, tx pg.Tx, source string) ([]byte, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO platform.alert_positions(source) VALUES($1) ON CONFLICT DO NOTHING`, source); err != nil {
		return nil, err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT position FROM platform.alert_positions WHERE source=$1 FOR UPDATE`, source).Scan(&raw)
	return raw, err
}

// SavePosition advances a bridge only with the signals emitted in this transaction.
func SavePosition(ctx context.Context, tx pg.Tx, source string, position any) error {
	raw, err := jsonx.MarshalCanonical(position)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE platform.alert_positions SET position=$2 WHERE source=$1`, source, raw)
	return err
}
