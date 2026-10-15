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

// Target is what a change was made to, and the tenant it belongs to: Site and Account for account data, Site alone
// for site data, neither for platform and public data. The pair must be registered (RegisterSite, RegisterAccount).
type Target struct {
	Module, Kind, ID string
	Site, Account    uuid.UUID // the zero UUID is none
}

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
	if err := New().InsertAuditEntry(ctx, tx, InsertAuditEntryParams{
		ActorKind:  event.Actor.kind,
		ActorID:    event.Actor.id,
		RequestID:  httpserver.RequestID(ctx),
		Action:     event.Action,
		Module:     t.Module,
		TargetKind: t.Kind,
		TargetID:   t.ID,
		SiteID:     optional(t.Site),
		AccountID:  optional(t.Account),
		Reason:     event.Reason,
		Detail:     detail,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", event.Action, err)
	}
	return nil
}

// optional spells id for the query, which reads the empty string as none.
func optional(id uuid.UUID) string {
	if id == (uuid.UUID{}) {
		return ""
	}
	return id.String()
}

// RegisterSite records site as a tenant entries may name, in tx: the one that creates the site, so the site and its
// registration commit together.
func RegisterSite(ctx context.Context, tx pg.Tx, site uuid.UUID) error {
	return register(ctx, tx, site, site)
}

// RegisterAccount records account, of site, as a tenant entries may name, in the transaction that creates it. The
// pair is what an entry's site and account are checked against.
func RegisterAccount(ctx context.Context, tx pg.Tx, site, account uuid.UUID) error {
	return register(ctx, tx, site, account)
}

func register(ctx context.Context, tx pg.Tx, site, tenant uuid.UUID) error {
	if err := New().InsertTenant(ctx, tx, InsertTenantParams{SiteID: site.String(), TenantID: tenant.String()}); err != nil {
		return fmt.Errorf("register a tenant: %w", err)
	}
	return nil
}
