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
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
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
	return h.admitDBBatch(ctx, calls, func(tx pg.Tx, as []accounts.Admission, refusals []error) error {
		return h.reserveBatch(ctx, tx, live, as, refusals)
	})
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
	if err != nil {
		return err
	}
	h.outage.recovered(depRouting)
	it.admitted = billingAdmitted(it.admission, c)
	plan, err = h.resolveSearch(c, plan, func() (routing.Plan, error) {
		// The backend's injected results are input the context check must see. The request is the same pointer, so a
		// second plan reads the SearchBytes the backend path just reserved and recomputes the output limit with it.
		return h.planRoute(c, it.admission)
	})
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
		Admission: routingAdmission(admission, c), Req: &c.Request, Models: c.models, Auto: c.auto, Free: c.free, Override: c.over, Sticky: c.sticky,
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

// resolveSearch decides the request's one search path, now that routing has chosen the entry whose model and format
// decide what the provider can do. The plugin is one search, never two (docs/domains/gateway.md「联网搜索」): a request
// that already declares the provider's own search tool, and a model that searches on every call, keep that path and
// drop the plugin; otherwise the plugin is either the provider's tool or one backend, and never both.
func (h *Handler) resolveSearch(c *Call, plan routing.Plan, replan func() (routing.Plan, error)) (routing.Plan, error) {
	if c.Request.ServerSearch != nil {
		return h.resolveServerTool(c, plan)
	}
	if c.Request.Web == nil || len(plan.Entries) == 0 {
		return plan, nil
	}
	entry := plan.Entries[0]
	choice := searchChoice{MaxUses: entry.Limits.MaxUses[catalog.ItemWebSearch]}
	if model, ok := c.snap.Model(entry.Model); ok {
		choice.Native = servesWebSearch(model) && protocol.NativeSearchSupported(entry.Target.Format)
		choice.Always = model.AlwaysSearches()
	}
	decision, err := chooseSearch(c.Request.Web, choice, c.Request.Needs.Has(protocol.WebSearch), h.Search)
	if err != nil {
		return plan, err
	}
	if decision.native > 0 {
		return nativeSearch(c, plan, decision)
	}
	if decision.backend != nil {
		return backendSearch(c, plan, decision, replan)
	}
	if choice.Always {
		// The first entry searches without a tool. A normal fallback has neither that tool nor injected results, so
		// keeping it would silently turn a failed automatic search into an answer with no search at all.
		plan = autoSearchRoute(c, plan)
		if len(plan.Entries) == 0 {
			return plan, invalid("no candidate endpoint searches automatically")
		}
	}
	c.Request.Web = nil
	c.Request.SearchBytes = 0
	return plan, nil
}

// resolveServerTool decides how the openrouter:web_search tool runs: the provider's own search when the routed entry
// can serve it and the engine is native, otherwise the model-side loop through the chosen backend. The engine choice
// and its default are the web plugin's (docs/domains/gateway.md「联网搜索」「服务端工具」).
func (h *Handler) resolveServerTool(c *Call, plan routing.Plan) (routing.Plan, error) {
	tool := c.Request.ServerSearch
	if len(plan.Entries) == 0 {
		return plan, nil
	}
	cap := tool.MaxUses
	if cap == 0 {
		cap = protocol.DefaultServerToolCalls
	}
	if c.Request.HasMaxToolCalls {
		cap = min(cap, c.Request.MaxToolCalls)
	}
	if cap <= 0 {
		return plan, invalid("max_tool_calls is 0: the web search tool would have no step to run")
	}
	entry := plan.Entries[0]
	choice := searchChoice{MaxUses: cap}
	if model, ok := c.snap.Model(entry.Model); ok {
		choice.Native = servesWebSearch(model) && protocol.NativeSearchSupported(entry.Target.Format)
		choice.Always = model.AlwaysSearches()
	}
	if choice.Always {
		// The model searches without a tool on every call; the tool's declaration is dropped and its searches are the
		// model's own, as the web plugin treats such a model. A fallback without the automatic search would answer
		// without one, so only models that search stay.
		tool.Loop = false
		c.Request.Needs |= protocol.WebSearch
		c.Request.WebSearches = min(cap, maxNativeSearches)
		c.Request.SearchBytes, c.Request.Web = 0, nil
		plan = autoSearchRoute(c, plan)
		if len(plan.Entries) == 0 {
			return plan, invalid("no candidate endpoint searches automatically")
		}
		return plan, nil
	}
	decision, err := chooseSearchFor(&protocol.WebPlugin{Engine: tool.Engine, MaxResults: tool.MaxResults}, choice, false, h.Search, "tool")
	if err != nil {
		return plan, err
	}
	if decision.native > 0 {
		tool.Loop = false
		return nativeSearch(c, plan, decision)
	}
	// The loop runs one search per step and samples the model again after each: its input re-reads and per-use fees
	// are what the reservation covers, at the cap above.
	tool.Loop = true
	c.serverLoop, c.searchBackend, c.searchPrice = true, decision.backend, decision.price
	c.Request.Needs |= protocol.Tools
	c.Request.WebSearches = cap
	c.Request.SearchBytes = 0
	c.Request.Web = nil
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the selected search engine")
	}
	return plan, nil
}

// nativeSearch turns on the model's own tool and keeps only entries that can run it.
func nativeSearch(c *Call, plan routing.Plan, decision searchDecision) (routing.Plan, error) {
	// A request that already carries Responses' call limit keeps the stricter of it and the model's bound; the same
	// number goes into the body below, so the reservation and the upstream limit cannot disagree. Inspect records
	// the cap for a tool the client declared; a plugin turns the tool on after Inspect, so read the cap here for that
	// path too.
	if c.Request.Format == protocol.Responses && c.Request.WebSearches == 0 {
		if n, err := protocol.ResponsesMaxToolCalls(c.Body); err == nil {
			c.Request.WebSearches = n
		}
	}
	if c.Request.WebSearches > 0 {
		decision.native = min(decision.native, int(c.Request.WebSearches))
	}
	c.nativeSearch = decision.native
	c.Request.Needs |= protocol.WebSearch
	c.Request.WebSearches = int64(decision.native)
	c.Request.SearchBytes = 0
	c.Request.Web = nil
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the model's native web search")
	}
	return plan, nil
}

// backendSearch reads the query once, keeps the plugin for DemandOf and webSearch, and keeps only entries that can
// follow that one external search.
func backendSearch(c *Call, plan routing.Plan, decision searchDecision, replan func() (routing.Plan, error)) (routing.Plan, error) {
	query, err := protocol.ChatSearchQuery(c.Body)
	if err != nil {
		return plan, invalid(err.Error())
	}
	if query == "" {
		return plan, invalid(protocol.ErrEmptySearchQuery.Error())
	}
	if len(query) > protocol.MaxWebSearchQueryBytes {
		return plan, invalid(fmt.Sprintf("The web plugin's search query is longer than %d bytes.", protocol.MaxWebSearchQueryBytes))
	}
	// The backend search runs before the attempt, so the request keeps its plugin (and the input bound the injected
	// results reserve) for DemandOf and webSearch; only its price changes.
	c.search, c.searchBackend, c.searchPrice = query, decision.backend, decision.price
	c.Request.SearchBytes = protocol.WebSearchMaxBytes
	// The first route ran with no injected input. Route again with the bound the backend will add; otherwise a small
	// context endpoint can win now and only fail after the search has already run and been charged.
	plan, err = replan()
	if err != nil {
		return plan, err
	}
	plan = searchRoute(c, plan, decision)
	if len(plan.Entries) == 0 {
		return plan, invalid("no candidate endpoint can run the selected search engine")
	}
	return plan, nil
}

// autoSearchRoute keeps only models that search without a tool. The first entry chose that path; a normal fallback
// would answer without the search the client asked for.
func autoSearchRoute(c *Call, plan routing.Plan) routing.Plan {
	kept := make([]int, 0, len(plan.Entries))
	for i := range plan.Entries {
		model, ok := c.snap.Model(plan.Entries[i].Model)
		if ok && model.AlwaysSearches() {
			kept = append(kept, i)
		}
	}
	return trim(plan, kept)
}

// searchRoute cuts the fallbacks to the entries that can follow the one search path the first entry chose. A native
// search on a model that cannot run it, or on one that searches on every call, would fail or search twice; a backend
// search on a model that always searches would double-search the same way. Trimming here, before Quote, means the
// reservation covers exactly the entries an attempt may use.
func searchRoute(c *Call, plan routing.Plan, decision searchDecision) routing.Plan {
	kept := make([]int, 0, len(plan.Entries))
	for i := range plan.Entries {
		entry := &plan.Entries[i]
		model, ok := c.snap.Model(entry.Model)
		if !ok || model.AlwaysSearches() {
			continue
		}
		if decision.native > 0 && (!servesWebSearch(model) || !protocol.NativeSearchSupported(entry.Target.Format)) {
			continue
		}
		kept = append(kept, i)
	}
	return trim(plan, kept)
}

// maxNativeSearches bounds what one plugin request may turn on at a provider; a model's spec may allow more, but one
// plugin search should not spend an unbounded amount of the account's money.
const maxNativeSearches = 10

// searchChoice is what the routed entry's model and format allow: its own web search, and the model's default most
// searches. Always is a model that searches without a tool, which the plugin must not search again.
type searchChoice struct {
	Native  bool
	Always  bool
	MaxUses int64
}

// searchDecision is the one path a plugin request takes: native searches at the provider, or one backend, never both.
type searchDecision struct {
	native  int
	backend *websearch.Backend
	price   platform.UnitPrice
}

// chooseSearch is resolveSearch's decision, apart from the catalog it reads: declared is a request that already turns
// the provider's own search on, and all are the node's enabled engines.
func chooseSearch(web *protocol.WebPlugin, choice searchChoice, declared bool, all SearchBackends) (searchDecision, error) {
	return chooseSearchFor(web, choice, declared, all, "plugin")
}

// chooseSearchFor is chooseSearch with the word the refusal uses for the request's member: a plugin or the tool.
//
//nolint:gocognit // one branch per engine choice, which is the rule being expressed
func chooseSearchFor(web *protocol.WebPlugin, choice searchChoice, declared bool, all SearchBackends, noun string) (searchDecision, error) {
	if declared || choice.Always {
		return searchDecision{}, nil
	}
	native := func() (searchDecision, error) {
		uses := max(choice.MaxUses, 1)
		uses = min(uses, maxNativeSearches)
		return searchDecision{native: int(uses)}, nil
	}
	backend := func(b *websearch.Backend) (searchDecision, error) {
		price, err := b.Price(web.MaxResults)
		if err != nil {
			return searchDecision{}, invalid("the search engine's published price is out of range")
		}
		return searchDecision{backend: b, price: price}, nil
	}
	switch web.Engine {
	case "native":
		if !choice.Native {
			return searchDecision{}, invalid(fmt.Sprintf("%s option \"native\" is not supported: the selected model has no native web search on this endpoint", noun))
		}
		return native()
	case "", "auto":
		if choice.Native {
			return native()
		}
		b, ok := defaultBackend(all)
		if !ok {
			if web.Online {
				return searchDecision{}, invalid(errOnline)
			}
			return searchDecision{}, invalid(fmt.Sprintf("%s %q is not supported: %s", noun, "web", protocol.NoWebSearch))
		}
		return backend(b)
	default:
		b, ok := findBackend(all, web.Engine)
		if !ok {
			return searchDecision{}, invalid(fmt.Sprintf("%s option %q is not an enabled search engine", noun, web.Engine))
		}
		return backend(b)
	}
}

// servesWebSearch reports whether the model's spec declares the provider's web search ability.
func servesWebSearch(m catalog.Model) bool {
	serving, ok := m.Serving()
	return ok && serving.Abilities.Has(catalog.AbilityWebSearch)
}

// routingAdmission is what routing reads of the admission. A key with no preset is routed by the preset with the zero
// ID, which the routing snapshot must hold.
func routingAdmission(a accounts.Admission, c *Call) routing.Admission {
	ra := routing.Admission{Tier: a.Tier(), Account: routing.AccountID(c.caller.AccountID()), Site: routing.SiteID(c.site.ID)}
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
