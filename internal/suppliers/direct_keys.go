package suppliers

import (
	"context"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// More keys for a direct API that is connected already (docs/domains/suppliers.md「直连接口」): checked and stored as the
// keys of a new one are, on its one line.

// Mode is how Axiom reaches a platform supplier: relay, direct or cloud. It never changes.
func (a *Admin) Mode(ctx context.Context, tx pg.Tx, supplier uuid.UUID) (string, error) {
	s, err := a.view(ctx, tx, supplier, PlatformOwned)
	return s.Mode, err
}

// PlanDirectKeys checks pasted keys for a connected direct API as PlanDirect checks a new one's, against the supplier's
// own address, call format and settlement currency: a key sent twice, or one any credential holds now, is a duplicate
// and is not checked. A supplier of another mode is ErrStatusLocked: a cloud supplier's credential is replaced whole.
// Nothing is asked of the provider and nothing is written.
func (a *Admin) PlanDirectKeys(ctx context.Context, tx pg.Tx, supplier uuid.UUID, keys []string, limits Limits, reason string) (*DirectPlan, error) {
	s, err := a.view(ctx, tx, supplier, PlatformOwned)
	if err != nil {
		return nil, err
	}
	if s.Mode != "direct" {
		return nil, fmt.Errorf("%w: a %s supplier's keys are not pasted one by one", ErrStatusLocked, s.Mode)
	}
	if !limits.valid() {
		return nil, ErrLimits
	}
	currency, err := platform.ParseCurrency(s.SettlementCurrency)
	if err != nil {
		return nil, fmt.Errorf("read the settlement currency: %w", err)
	}
	concurrency, perMinute := limits.columns()
	r := DirectRequest{
		Kind: s.Kind, Name: s.Name, Currency: currency, Keys: keys, MaxConcurrency: concurrency, RequestsPerMinute: perMinute, Reason: reason,
	}
	if d, ok := a.registry.Get(adapter.Kind(s.Kind)); ok {
		if d, ok := d.(adapter.Direct); ok && d.Origin() == "" {
			r.Origin, r.Family = s.Origin, s.APIFormat
		}
	}
	p, err := a.planDirect(r)
	if err != nil {
		return nil, err
	}
	return p, p.skipStored(ctx, tx)
}

// AddDirectKeys stores the keys of a connected direct API that passed their checks, in the caller's transaction: each
// its own credential on the line with the plan's limits and the models it lists, and an endpoint, at the official
// price, for each model a key now lists that the line has none for. A key another transaction stored since
// PlanDirectKeys read is a duplicate here too. The checks are in the order of the keys.
func (a *Admin) AddDirectKeys(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, supplier uuid.UUID, p *DirectPlan) ([]KeyCheck, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return nil, err
	}
	if err := p.skipStored(ctx, tx); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(p.Checks, func(c KeyCheck) bool { return c.Outcome == CheckPassed }) {
		return p.Checks, nil
	}
	q := store.New()
	group, err := lineOf(ctx, tx, q, supplier)
	if err != nil {
		return nil, err
	}
	if err := a.insertDirectKeys(ctx, tx, q, supplier, group, p); err != nil {
		return nil, err
	}
	served, err := q.LineEndpointModels(ctx, tx, group)
	if err != nil {
		return nil, fmt.Errorf("read the line's endpoints: %w", err)
	}
	has := make(map[catalog.ModelID]bool, len(served))
	for _, m := range served {
		has[catalog.ModelID(m)] = true
	}
	if _, _, err := insertLineModels(ctx, tx, q, supplier, group, snap, p.r.Currency, p.offer(), has); err != nil {
		return nil, err
	}
	var stored []string
	for _, c := range p.Checks {
		if c.Outcome == CheckPassed {
			stored = append(stored, c.Credential.String())
		}
	}
	if err := a.record(ctx, tx, actor, "credential.paste", supplier, p.r.Reason, map[string]any{"group": group.String(), "credentials": stored}); err != nil {
		return nil, err
	}
	return p.Checks, nil
}
