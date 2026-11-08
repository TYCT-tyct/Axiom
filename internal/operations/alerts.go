package operations

import (
	"context"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// AgentAlert is the kind of the alerts the agent raises and resolves (alert_raise, alert_resolve), keyed by AlertKey:
// one key is one incident until it is resolved, so a patrol that sees the same trouble again does not notify the owner
// again (docs/domains/observability.md「告警信号、事件与规则（E5-3）」).
const AgentAlert = "operations.agent_alert"

// AlertKey is the incident op raises or resolves: the agent's key under its credential's ID. The consumer merges and
// resolves by kind and key alone, and a credential acts only on its own record, so one credential can neither resolve
// nor overwrite another's alert under the same words.
func AlertKey(op Operation) string { return op.Credential.String() + "/" + op.Target.ID }

// RaiseAlert persists the alert op raises in tx: the operation and its signal commit together, and op's ID is the
// signal's, so a run that is retried cannot raise it twice. The summary is the agent's untrusted text; the operation's
// ID leads from the notification back to who raised it and why.
func RaiseAlert(ctx context.Context, tx pg.Tx, op Operation, severity, summary string) error {
	return alert.Persist(ctx, tx, op.ID, alert.Signal{
		Kind: AgentAlert, Key: AlertKey(op), Severity: severity,
		Fields: map[string]string{"summary": summary, "operation": op.ID.String()},
	})
}

// ResolveAlert persists the recovery of the credential's alert op names in tx, as RaiseAlert does; a key with no open
// incident is left as it is.
func ResolveAlert(ctx context.Context, tx pg.Tx, op Operation) error {
	return alert.Persist(ctx, tx, op.ID, alert.Signal{Kind: AgentAlert, Key: AlertKey(op), Severity: "info", Resolved: true})
}
