package pg

import (
	"context"

	"axiom/internal/platform/jsonx"
)

// HasPendingJob lets an outbox repair a lost or cancelled job without reading River's tables itself.
// Arguments is the identifying subset of a job's parameters, not its content or secrets.
func HasPendingJob(ctx context.Context, tx Tx, kind string, arguments any) (bool, error) {
	raw, err := jsonx.MarshalCanonical(arguments)
	if err != nil {
		return false, err
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM river.river_job WHERE kind=$1 AND args @> $2::jsonb AND state IN ('available','pending','running','retryable','scheduled'))`, kind, raw).Scan(&pending)
	return pending, err
}
