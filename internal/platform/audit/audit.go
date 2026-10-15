// Package audit records who changed what in the audit log, platform.audit_log (internal/platform/README.md「审计日志」).
// It lives in platform because every module must record its changes and none may import operations, which reads the
// log. A module's own change history, such as catalog's change table, is its domain data and does not replace it.
package audit

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Actor is who made a change. The zero value is no one, which Record refuses.
type Actor struct{ kind, id string }

// User is a person signed in to the console or the admin.
func User(id uuid.UUID) Actor { return Actor{"user", id.String()} }

// System is a job of ours acting on its own, named <module>.<job> such as catalog.sync.
func System(job string) Actor { return Actor{"system", job} }

// Target is what a change was made to. It names no tenant: the table refuses tenant entries until accounts can tie
// them to one, and the tenant fields come with that (internal/platform/README.md「审计日志」).
type Target struct{ Module, Kind, ID string }

// Event is one change. Action is a stable name, <noun>.<verb> such as api_key.revoke, for searching and replay.
// Reason is the actor's own words, if a rule asks for them. Detail is small and structured: IDs, names, before and
// after values. Neither may hold a secret, a password, a key, call content or personal data beyond IDs.
type Event struct {
	Actor  Actor
	Action string
	Target Target
	Reason string
	Detail map[string]any
}

// Record appends event in tx, so it commits or rolls back with the change it describes. Its error must fail the
// change: an unrecorded change is not allowed to stand. The table's constraints refuse what is missing or malformed,
// and its policy a transaction outside the platform scope. The request ID comes from ctx, when a request has one.
func Record(ctx context.Context, tx pg.Tx, event Event) error {
	detail, err := jsonx.MarshalCanonical(event.Detail)
	if err != nil {
		return fmt.Errorf("audit %s: encode the detail: %w", event.Action, err)
	}
	t := event.Target
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_log (actor_kind, actor_id, request_id, action, module,
		target_kind, target_id, reason, detail) VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, $6, $7, nullif($8, ''), $9)`,
		event.Actor.kind, event.Actor.id, httpserver.RequestID(ctx), event.Action, t.Module, t.Kind, t.ID, event.Reason,
		detail); err != nil {
		return fmt.Errorf("audit %s: %w", event.Action, err)
	}
	return nil
}
