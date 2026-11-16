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
	var woken relay
	for {
		epoch := h.Limits.Queue.Epoch() // before the load is looked at, so a slot freed meanwhile is not missed
		err := h.admitOnce(ctx, c)
		if err != nil {
			h.Limits.Queue.Pass(woken) // not admitted: the slot it was woken for is the next waiter's to try
		}
		wait, ok := errors.AsType[*errWait](err)
		if !ok {
			return err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(h.Limits.cfg.QueueWait)
		}
		if woken, err = h.Limits.Queue.Wait(ctx, keyKey(c.caller), epoch, deadline); err != nil {
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

// admissionItem is one request in an admission batch: what the check reads, what the route plan decides, and what
// the reservation wrote. The batch's leader fills it; the request reads it after the gate wakes it.
type admissionItem struct {
	gate      gateItem
	call      *Call
	caller    accounts.Caller
	request   accounts.AdmitRequest
	admission accounts.Admission
	admitted  billing.Admitted
	plan      routing.Plan
	hold      *billing.Hold
	// reserved is set once ReserveMany has written the rows. Only the batch's COMMIT can still fail after
	// that, and a failed or unacknowledged COMMIT may have landed, so the request settles an orphan rather
	// than assuming nothing was written; an earlier failure rolled the transaction back.
	reserved bool
}

func (h *Handler) admitOnce(ctx context.Context, c *Call) error {
	item := &admissionItem{call: c, caller: c.caller, request: accounts.AdmitRequest{Domain: c.host, ClientIP: c.ip}}
	item.gate.done, item.gate.payload = make(chan struct{}), item
	err := h.Limits.Funds.Admit(ctx, accountKey(c.caller), &item.gate, h.admitBatch)
	if item.reserved {
		c.Admitted = item.admitted
	}
	if err != nil && item.reserved {
		h.Logger.Warn("an admission's commit was not acknowledged", "request_id", c.ID.String(), "err", err)
		h.settleOrphan(ctx, c)
		return fmt.Errorf("%w: %w", errCommitUnknown, err)
	}
	if err != nil {
		return err
	}
	c.Admission, c.Admitted, c.Hold, c.Plan = item.admission, item.admitted, item.hold, item.plan
	return nil
}

// admitBatch is one account's admission transaction: the key checks pipelined, the route of every request planned
// from memory, and every reservation written under one balance lock (README.md#账户闸). A request that the key
// check, the rate or the route refuses carries its own error and writes nothing; the rest of the batch still goes on.
func (h *Handler) admitBatch(ctx context.Context, batch []*gateItem) error {
	items := make([]*admissionItem, len(batch))
	for i, g := range batch {
		items[i] = g.payload.(*admissionItem)
	}
	calls := make([]accounts.AdmitCall, 0, len(items))
	live := make([]*admissionItem, 0, len(items))
	for _, it := range items {
		if err := it.gate.ctx.Err(); err != nil {
			it.gate.err = err
			continue
		}
		calls = append(calls, accounts.AdmitCall{Caller: it.caller, Request: it.request})
		live = append(live, it)
	}
	if len(calls) == 0 {
		return nil
	}
	err := h.admitBatchTx(ctx, calls, func(tx pg.Tx, as []accounts.Admission, refusals []error) error {
		return h.reserveBatch(ctx, tx, live, as, refusals)
	})
	if err == nil {
		h.outage.recovered(depDatabase)
	}
	return err
}

// reserveBatch plans every admitted request and writes the reservations of the planned ones under one balance lock.
func (h *Handler) reserveBatch(ctx context.Context, tx pg.Tx, live []*admissionItem, as []accounts.Admission, refusals []error) error {
	reqs := make([]billing.ReserveRequest, 0, len(live))
	quoted := make([]*admissionItem, 0, len(live))
	for i, it := range live {
		if refusals[i] != nil {
			it.gate.err = refusals[i]
			continue
		}
		it.admission = as[i]
		if err := h.planAdmission(it); err != nil {
			it.gate.err = err
			continue
		}
		reqs = append(reqs, billing.ReserveRequest{
			Admitted: it.admitted, ID: it.call.ID, At: it.call.At,
			Demand: routing.DemandOf(&it.call.Request), Quotables: quotables(it.plan.Entries, it.call),
			Segments: it.plan.Segments,
		})
		quoted = append(quoted, it)
	}
	if len(reqs) == 0 {
		return nil
	}
	holds, kept, errs, err := h.Billing.ReserveMany(ctx, tx, reqs)
	if err != nil {
		// No COMMIT was sent: the transaction can only roll back, so no reservation of this batch can have been
		// written, whatever the fallback read, the lock or an insert reported.
		return err
	}
	for i, it := range quoted {
		if errs[i] != nil {
			it.gate.err = errs[i]
			continue
		}
		// From here on the rows are written; the batch's COMMIT may still fail or lose its acknowledgement, so the
		// request must settle an orphan rather than assume nothing was written.
		it.reserved = true
		it.hold, it.plan = holds[i], trim(it.plan, kept[i])
	}
	return nil
}

// planAdmission judges the rate and plans the route from the admission and the in-memory route data; the reservation
// is written separately, after every request in the batch has a plan, so one request's refusal does not abort another.
func (h *Handler) planAdmission(it *admissionItem) error {
	c := it.call
	if err := h.judgeRate(it.admission, c, callDimension); err != nil {
		return err
	}
	// The first route chooses the search path, so it sees the request without a search's injected input. A backend
	// search re-plans below with that bound; a retry after a queued backend attempt starts here again without it.
	c.Request.SearchBytes = 0
	plan, err := h.planRoute(c, it.admission)
	if err == nil {
		h.outage.recovered(depRouting)
		it.admitted = billingAdmitted(it.admission, c)
		plan, err = h.resolveSearch(c, plan, func() (routing.Plan, error) {
			// The backend's injected results are input the context check must see. The request is the same pointer, so
			// a second plan reads the SearchBytes the backend path just reserved and recomputes the output limit with it.
			return h.planRoute(c, it.admission)
		})
	}
	// Once a call, with the plan it is admitted by: the first plan of a backend search only chooses the path.
	h.Router.Planned(plan, err)
	if err != nil {
		return err
	}
	it.plan = plan
	return nil
}

// planRoute is one route of the call's current request. It runs twice for a backend search: once to choose the search
// path from the first entry, and once after the injected-results bound is set, so a small-context first entry cannot
// be chosen only because the search results were not counted yet.
func (h *Handler) planRoute(c *Call, admission accounts.Admission) (routing.Plan, error) {
	plan, wait, err := h.Models.plan(h.Router, routing.Input{
		Admission: routingAdmission(admission, c.caller, c.site, c.snap), Req: &c.Request, Models: c.models, Auto: c.auto, Free: c.free, Override: c.over, Sticky: c.sticky,
		// Compaction has no Chat counterpart; only the three chat formats convert. Direct formats and legacy
		// completions have no counterpart either.
		NativeOnly: c.Compact || !c.Request.Format.Convertible(),
		// The compact call is answered whole, so nothing of it is metered: its reservation is made in full, and so is
		// a direct call's, which answers one body (docs/domains/gateway.md「转发规则」).
		NoSegments: c.Compact || c.Request.Format.Direct(),
		// A file id is the supplier account's resource: one attempt, to the provider the client pinned, so it can only
		// reach the account that can hold the file (protocol README.md「跨格式转换」).
		FileRef: c.Request.FileRefs > 0,
	})
	if wait != nil {
		return plan, &errWait{wait.RetryAfter}
	}
	return plan, err
}

// routingAdmission is what routing reads of the admission, for a call and for the model lists alike. A key with no
// preset is routed by the preset with the zero ID, which the routing snapshot must hold.
func routingAdmission(a accounts.Admission, caller accounts.Caller, site accounts.Site, snap *catalog.Snapshot) routing.Admission {
	ra := routing.Admission{
		Tier: a.Tier(), Account: routing.AccountID(caller.AccountID()), Site: routing.SiteID(site.ID),
		Workspace: routing.WorkspaceID(caller.WorkspaceID()), Currency: site.Currency,
	}
	if p, ok := a.Preset(); ok {
		ra.Preset = routing.PresetID(p)
	}
	if names := a.Models(); names != nil { // nil allows every model, an empty list none
		ra.Allowed = allowedModels(snap, names)
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
		Preference: a.BillingPreference(),
	}
}

func limitOf(amount int64, reset string, ok bool) *billing.Limit {
	if !ok {
		return nil
	}
	return &billing.Limit{Amount: amount, Reset: billing.Reset(reset)}
}

func quotables(entries []routing.Entry, c *Call) []billing.Quotable {
	q := make([]billing.Quotable, len(entries))
	for i, e := range entries {
		price := e.Price.Schedule
		if c.searchBackend != nil && !c.searchPrice.IsZero() {
			// The backend's published per-search price replaces the model's web_search rate for this call. The
			// reservation locks the schedule it quotes, so settlement charges exactly the price reserved.
			price = price.WithRate(catalog.ItemWebSearch, c.searchPrice)
		}
		q[i] = billing.Quotable{
			Exchange: e.Exchange,
			Version:  e.Price.Version, Price: price, Model: e.Limits, MaxOutput: e.MaxOutput, Segmentable: e.Segmentable,
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
	h.Outstanding.Go(func() {
		defer c.Done()
		never := func(error) bool { return false }
		if err := h.Outstanding.retry(time.Now().Add(h.Limits.cfg.SettleBackground), never, func() error { return h.orphanOnce(ctx, c) }); err != nil {
			h.Logger.Error("a lost admission was not settled; the cleanup takes it", "request_id", c.ID.String(), "err", err)
		}
	})
}

func (h *Handler) orphanOnce(ctx context.Context, c *Call) error {
	ctx, cancel := h.Outstanding.settlementContext(ctx, settleAttempt)
	defer cancel()
	item := &completionItem{scope: c.Admitted.Scope, work: func(ctx context.Context, tx pg.Tx) (func(), error) {
		return nil, h.Billing.SettleOrphanIn(ctx, tx, c.Admitted, c.ID)
	}}
	item.gate.done, item.gate.payload = make(chan struct{}), item
	if err := h.Limits.Funds.Complete(ctx, accountKey(c.caller), &item.gate, h.runOrphans); err != nil {
		return err
	}
	return item.gate.err
}

// runOrphans writes a batch of the account's orphan settlements in one transaction.
func (h *Handler) runOrphans(ctx context.Context, batch []*gateItem) error {
	return runCompletionBatch(pg.CompletionContext(ctx), batch, h.Billing.RunBatch)
}
