package operations

import (
	"context"
	"time"

	"axiom/internal/platform/pg"
)

// ExpireAll is the work of one run of the expiry job, for the tests outside the package.
var ExpireAll = expireAll

// ChainAuditAfter is ChainAudit with entries linked settle after their transaction began.
var ChainAuditAfter = chainAudit

// SignalChainLag raises or resolves the chain's lag in tx, as the end of a run does.
var SignalChainLag = signalLag

// VerifyAuditChainBy is VerifyAuditChain with at most batches transactions a run.
var VerifyAuditChainBy = verifyAuditChain

// MakeAuditDigestsAt is MakeAuditDigests with the clock fixed at now and the frontier at frontier.
func MakeAuditDigestsAt(ctx context.Context, db *pg.DB, scope pg.Scope, now, frontier time.Time) (int, error) {
	return makeAuditDigests(ctx, db, scope, func(context.Context, pg.Tx) (time.Time, time.Time, error) { return now, frontier, nil })
}

// ErrReplicaConflict is a replica key that holds other bytes.
var ErrReplicaConflict = errReplicaConflict

// ReplicaAlertKey is a replica target's part of its alerts' keys.
var ReplicaAlertKey = replicaAlertKey
