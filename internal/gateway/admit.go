package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// errWait is what the admission's function returns when every candidate endpoint is full: Admit rolls back, with
// nothing reserved, and the caller waits outside the transaction.
type errWait struct{ after time.Duration }

func (*errWait) Error() string { return "every candidate endpoint is full" }

// errCommitUnknown is a failure after the reservation was written, so its COMMIT may or may not have happened; the
// request is answered 503 and not sent upstream, and SettleOrphan settles whatever the database has.
var errCommitUnknown = errors.New("gateway: the admission's commit was not acknowledged")

// admit is step 6: the account's gate, an admission slot and the transaction, with the rate check, the route and the
// reservation inside it. When every endpoint is full it waits in the queue, holding no gate and no connection, and
// redoes only this step: the body was read once and the rate counted once.
func (h *Handler) admit(ctx context.Context, c *Call) error {
	var deadline time.Time // fixed when the call first queues: the limit is on all its waits together
	for {
		epoch := h.Limits.Queue.Epoch() // before the load is looked at, so a slot freed meanwhile is not missed
		err := h.admitOnce(ctx, c)
		wait, ok := errors.AsType[*errWait](err)
		if !ok {
			return err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(h.Limits.cfg.QueueWait)
		}
		if err := h.Limits.Queue.Wait(ctx, keyKey(c.caller), epoch, deadline); err != nil {
			if limit, ok := errors.AsType[*Refusal](err); ok {
				full := *limit
				full.RetryAfter = seconds(wait.after)
				return &full
			}
			return err
		}
	}
}

// seconds rounds a wait up to whole seconds, at least one.
func seconds(d time.Duration) int { return max(1, int((d+time.Second-1)/time.Second)) }

func (h *Handler) admitOnce(ctx context.Context, c *Call) error {
	ctx, release, err := h.Limits.Funds.Admit(ctx, accountKey(c.caller))
	if err != nil {
		return err
	}
	defer release()
	reserved := false
	err = h.admitDB(ctx, c.caller, accounts.AdmitRequest{Domain: c.host, ClientIP: c.ip}, func(tx pg.Tx, a accounts.Admission) error {
		if err := h.reserve(ctx, tx, a, c); err != nil {
			return err
		}
		reserved = true
		return nil
	})
	if err != nil && reserved {
		h.Logger.Warn("an admission's commit was not acknowledged", "request_id", c.ID.String(), "err", err)
		h.settleOrphan(ctx, c)
		return fmt.Errorf("%w: %w", errCommitUnknown, err)
	}
	return err
}

// reserve is the admission's function: judge the rate, plan the route from memory, and reserve against the route in
// the same transaction. The route is cut to the entries the reservation covers; its first entry is always among them,
// or the request is refused 402.
func (h *Handler) reserve(ctx context.Context, tx pg.Tx, a accounts.Admission, c *Call) error {
	if c.rate > h.Limits.cfg.KeyRate {
		return errKeyRate
	}
	plan, wait, err := h.Router.Plan(routing.Input{
		Admission: routingAdmission(a, c), Req: &c.Request, Models: c.models, Override: c.over, Sticky: c.sticky,
		// Compaction has no Chat counterpart; generation can use the protocol conversions.
		NativeOnly: c.Compact,
		// The compact call is answered whole, so nothing of it is metered: its reservation is made in full.
		NoSegments: c.Compact,
	})
	if wait != nil {
		return &errWait{wait.RetryAfter}
	}
	if err != nil {
		return err
	}
	h.outage.recovered(depRouting)
	admitted := billingAdmitted(a, c)
	hold, kept, err := h.Billing.Reserve(ctx, tx, admitted, c.ID, routing.DemandOf(&c.Request), quotables(plan.Entries))
	if err != nil {
		return err
	}
	c.Admission, c.Admitted, c.Hold, c.Plan = a, admitted, hold, trim(plan, kept)
	return nil
}

// routingAdmission is what routing reads of the admission. A key with no preset is routed by the preset with the zero
// ID, which the routing snapshot must hold.
func routingAdmission(a accounts.Admission, c *Call) routing.Admission {
	ra := routing.Admission{Account: routing.AccountID(c.caller.AccountID()), Site: routing.SiteID(c.site.ID)}
	if p, ok := a.Preset(); ok {
		ra.Preset = routing.PresetID(p)
	}
	if names := a.Models(); names != nil { // nil allows every model, an empty list none
		ra.Allowed = allowedModels(c.snap, names)
	}
	return ra
}

// allowedModels are the models the key's names stand for; a name the catalog does not know allows nothing.
func allowedModels(snap *catalog.Snapshot, names []string) map[catalog.ModelID]struct{} {
	allowed := make(map[catalog.ModelID]struct{}, len(names))
	for _, name := range names {
		if m, err := snap.ResolvePublic(name); err == nil {
			allowed[m.Model] = struct{}{}
		}
	}
	return allowed
}

func billingAdmitted(a accounts.Admission, c *Call) billing.Admitted {
	return billing.Admitted{
		Scope: a.Scope(), Site: uuid.UUID(c.site.ID), Account: uuid.UUID(c.caller.AccountID()),
		Workspace: uuid.UUID(c.caller.WorkspaceID()), Key: uuid.UUID(c.caller.KeyID()),
		WorkspaceBudget: limitOf(a.Budget()), KeyLimit: limitOf(a.SpendLimit()),
	}
}

func limitOf(amount int64, reset string, ok bool) *billing.Limit {
	if !ok {
		return nil
	}
	return &billing.Limit{Amount: amount, Reset: billing.Reset(reset)}
}

func quotables(entries []routing.Entry) []billing.Quotable {
	q := make([]billing.Quotable, len(entries))
	for i, e := range entries {
		q[i] = billing.Quotable{
			Exchange: e.Exchange,
			Price:    e.Price.Schedule, Model: e.Limits, MaxOutput: e.MaxOutput, Segmentable: e.Segmentable,
			Endpoint: uuid.UUID(e.Endpoint),
		}
	}
	return q
}

// trim cuts the route to the entries kept, in their order. A fallback the reservation does not cover is never tried:
// the attempt would spend more than was reserved.
func trim(p routing.Plan, kept []int) routing.Plan {
	entries := make([]routing.Entry, len(kept))
	route := make([]routing.EndpointID, len(kept))
	for i, k := range kept {
		entries[i], route[i] = p.Entries[k], p.Entries[k].Endpoint
	}
	p.Entries, p.Record.Route = entries, route
	return p
}

// settleOrphan ends the reservation of a lost admission in the background, retrying a database error with a growing
// pause until it is settled or the cleanup would have it anyway (SettleBackground). The call's slots stay held until
// then, so the settlements waiting are bounded by the node's limit. Drain waits for it.
func (h *Handler) settleOrphan(ctx context.Context, c *Call) {
	h.background.Go(func() {
		defer c.Done()
		never := func(error) bool { return false }
		if err := h.background.retry(time.Now().Add(h.Limits.cfg.SettleBackground), never, func() error { return h.orphanOnce(ctx, c) }); err != nil {
			h.Logger.Error("a lost admission was not settled; the cleanup takes it", "request_id", c.ID.String(), "err", err)
		}
	})
}

func (h *Handler) orphanOnce(ctx context.Context, c *Call) error {
	ctx, cancel := SettleContext(ctx, settleAttempt)
	defer cancel()
	release, err := h.Limits.Funds.Complete(ctx, accountKey(c.caller))
	if err != nil {
		return err
	}
	defer release()
	return h.Billing.SettleOrphan(ctx, c.Admitted, c.ID)
}

// Drain waits for the settlements still running in the background, until ctx ends; then it stops them, and what is
// left is the cleanup's.
func (h *Handler) Drain(ctx context.Context) { h.background.drain(ctx) }
