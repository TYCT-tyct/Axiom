package operations

// ExpireAll is the work of one run of the expiry job, for the tests outside the package.
var ExpireAll = expireAll

// ChainAuditAfter is ChainAudit with entries linked settle after their transaction began.
var ChainAuditAfter = chainAudit

// SignalChainLag raises or resolves the chain's lag in tx, as the end of a run does.
var SignalChainLag = signalLag

// VerifyAuditChainBy is VerifyAuditChain with at most batches transactions a run.
var VerifyAuditChainBy = verifyAuditChain

// ErrReplicaConflict is a replica key that holds other bytes.
var ErrReplicaConflict = errReplicaConflict

// ReplicaAlertKey is a replica target's part of its alerts' keys.
var ReplicaAlertKey = replicaAlertKey
