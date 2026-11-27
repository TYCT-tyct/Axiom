package suppliers

import (
	"context"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// Replacing a cloud supplier's credential (docs/domains/suppliers.md「云平台」): checked in the regions it is to serve
// from, and put in place of the old one in one change when it passes in any.

// PlanCloudReplace reads a replacement of a cloud supplier's credential: the new credential, and the regions to serve
// from (none: those it serves from now), each to be checked. A region of the supplier's own is not a duplicate. r
// carries the limits a new region takes and the reason; nothing is asked of the platform and nothing is written.
func (a *Admin) PlanCloudReplace(ctx context.Context, tx pg.Tx, supplier uuid.UUID, r CloudRequest) (*CloudPlan, error) {
	rows, err := store.New().GetSupplierView(ctx, tx, store.GetSupplierViewParams{ID: supplier})
	if err != nil {
		return nil, fmt.Errorf("read the supplier: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrNoSupplier
	}
	s := rows[0]
	c, err := a.replaceable(s, r)
	if err != nil {
		return nil, err
	}
	regions := r.Regions
	if regions == nil && c.Type().Has(adapter.FieldRegions) {
		if regions, err = servedRegions(ctx, tx, supplier); err != nil {
			return nil, err
		}
	}
	if r.Currency, err = platform.ParseCurrency(s.SettlementCurrency); err != nil {
		return nil, fmt.Errorf("the supplier's settlement currency: %w", err)
	}
	var named []adapter.CloudModel
	if c.Type().Has(adapter.FieldDeployments) {
		if named, err = servedNames(ctx, tx, supplier); err != nil {
			return nil, err
		}
	}
	p, err := planCredential(c, s.Kind, s.Origin, r, regions, named)
	if err != nil {
		return nil, err
	}
	p.r, p.supplier = r, supplier
	if err := a.lineExit(ctx, tx, p); err != nil {
		return nil, err
	}
	return p, p.skipStored(ctx, tx)
}

// servedNames is the models a person named for a supplier, as its credential serving now serves them: what a new
// credential of a platform that lists none serves.
func servedNames(ctx context.Context, tx pg.Tx, supplier uuid.UUID) ([]adapter.CloudModel, error) {
	rows, err := store.New().ServedNames(ctx, tx, supplier)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	calls := map[string]string{}
	if rows[0].CallNames != nil {
		if err := jsonx.Unmarshal(rows[0].CallNames, &calls); err != nil {
			return nil, fmt.Errorf("the credential's call names: %w", err)
		}
	}
	named := make([]adapter.CloudModel, len(rows[0].AllowedModels))
	for i, name := range rows[0].AllowedModels {
		named[i] = adapter.CloudModel{Name: name, Call: calls[name]}
	}
	return named, nil
}

// replaceable is the platform of a supplier whose credential can be replaced with r's limits for a new region.
func (a *Admin) replaceable(s store.GetSupplierViewRow, r CloudRequest) (adapter.Cloud, error) {
	switch {
	case s.Mode != "cloud":
		return nil, ErrNotCloud
	case Status(s.Status) == Removing || Status(s.Status) == Removed:
		return nil, fmt.Errorf("%w: it is %s", ErrStatusLocked, s.Status)
	case !r.Limits.valid():
		return nil, ErrLimits
	}
	c, ok := a.cloudOf(s.Kind)
	if !ok {
		return nil, ErrNoAdapter
	}
	return c, nil
}

// servedRegions is the regions a cloud supplier's credentials serve from now.
func servedRegions(ctx context.Context, tx pg.Tx, supplier uuid.UUID) ([]string, error) {
	creds, err := store.New().ListCredentialsOf(ctx, tx, supplier)
	if err != nil {
		return nil, fmt.Errorf("read the credentials: %w", err)
	}
	var regions []string
	for _, c := range creds {
		if c.Status != "erased" && c.Region != nil {
			regions = append(regions, *c.Region)
		}
	}
	slices.Sort(regions)
	return regions, nil
}

// lineExit sets the exit a replacement is checked through and a region it adds is stored with: the one the supplier's
// credentials share now, direct when they share none.
func (a *Admin) lineExit(ctx context.Context, tx pg.Tx, p *CloudPlan) error {
	creds, err := store.New().ListCredentialsOf(ctx, tx, p.supplier)
	if err != nil {
		return fmt.Errorf("read the credentials: %w", err)
	}
	shared, first := "", true
	for _, c := range creds {
		if c.Status == "erased" {
			continue
		}
		if id := deref(c.EgressID); first {
			shared, first = id, false
		} else if id != shared {
			shared = ""
			break
		}
	}
	p.r.EgressID = shared
	p.exit, err = a.exitFor(ctx, tx, shared)
	return err
}

// ReplaceCloud puts the new credential in place in the caller's transaction, under the supplier's lock: each region
// that passed is the supplier's from then on, re-sealed in the row made for the credential or in a new one, at the
// limits the region has now (a region not served now takes those given); every other region is erased. When no region
// passed nothing changes and Connected has no supplier. Calls in flight end with the credential they hold. The models a
// region lists get endpoints, and the line's endpoints it lists by another name are served by it (mapLine).
func (a *Admin) ReplaceCloud(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *CloudPlan) (Connected, error) {
	if _, err := a.locked(ctx, tx, p.supplier); err != nil {
		return Connected{}, err
	}
	if err := takeStoreTurn(ctx, tx); err != nil {
		return Connected{}, err
	}
	if err := p.skipStored(ctx, tx); err != nil {
		return Connected{}, err
	}
	if !p.passed() {
		return Connected{Checks: p.Checks}, nil
	}
	q := store.New()
	group, ratio, err := lineOf(ctx, tx, q, p.supplier)
	if err != nil {
		return Connected{}, err
	}
	p.ratio = ratio
	if err := p.refreshLine(ctx, tx, q, group, snap); err != nil {
		return Connected{}, err
	}
	if err := p.mapLine(ctx, tx, q, group, snap); err != nil {
		return Connected{}, err
	}
	erased, err := a.replaceRegions(ctx, tx, q, group, p)
	if err != nil {
		return Connected{}, err
	}
	detail := cloudAudit(p)
	detail["erased_regions"] = erased
	if err := a.record(ctx, tx, actor, "supplier.cloud_credential_replace", p.supplier, p.r.Reason, detail); err != nil {
		return Connected{}, err
	}
	if err := persist.ObserveInbox(ctx, tx, p.supplier, a.now(), persist.InboxKeys, false); err != nil {
		return Connected{}, err
	}
	s, err := a.view(ctx, tx, p.supplier, EveryOwner)
	if err != nil {
		return Connected{}, err
	}
	return Connected{Supplier: &s, Checks: p.Checks}, nil
}

// refreshLine puts the new credential's account on the supplier and gives the line an endpoint for each model a
// region now lists that has none; a platform whose models a person named serves the ones it serves now.
func (p *CloudPlan) refreshLine(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, snap *catalog.Snapshot) error {
	account, err := jsonx.MarshalCanonical(p.cred.Account)
	if err != nil {
		return err
	}
	if err := q.SetCloudAccount(ctx, tx, store.SetCloudAccountParams{CloudAccount: account, ID: p.supplier}); err != nil {
		return fmt.Errorf("set the cloud account: %w", err)
	}
	if p.cloud.Type().Has(adapter.FieldDeployments) {
		return nil // the models a person named are the line's already, mapped by the models they named
	}
	served, err := q.LineEndpoints(ctx, tx, group)
	if err != nil {
		return fmt.Errorf("read the line's endpoints: %w", err)
	}
	has := make(map[catalog.ModelID]bool, len(served))
	for _, m := range served {
		has[catalog.ModelID(m.ModelID)] = true
	}
	_, _, err = insertLineModels(ctx, tx, q, p.supplier, group, snap, p.r.Currency, p.offer(snap), has, p.ratio)
	return err
}

// replaceRegions stores each region that passed and erases the supplier's other credentials; it returns the regions
// erased. A row's identity is its credential and region, so a region that passed with the credential a row of the
// supplier was made for is sealed again in that row, and any other gets a new row: with the limits of the row it
// replaces in its region, or the limits given for a region not served now. The rows replaced are erased first, since a
// region is served by one row at a time.
func (a *Admin) replaceRegions(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, p *CloudPlan) ([]string, error) {
	rows, err := q.LockCloudCredentials(ctx, tx, p.supplier)
	if err != nil {
		return nil, fmt.Errorf("lock the credentials: %w", err)
	}
	byRef := make(map[string]store.LockCloudCredentialsRow, len(rows))
	serving := map[string]store.LockCloudCredentialsRow{} // by region, the rows not erased
	for _, r := range rows {
		byRef[r.UpstreamKeyRef] = r
		if r.Status != "erased" {
			serving[deref(r.Region)] = r
		}
	}
	erased, err := p.eraseReplaced(ctx, tx, q, serving, byRef)
	if err != nil {
		return nil, err
	}
	for i := range p.Checks {
		if p.Checks[i].Outcome == CheckPassed {
			if err := a.storeRegion(ctx, tx, q, group, p, i, byRef, serving); err != nil {
				return nil, err
			}
		}
	}
	return erased, nil
}

// eraseReplaced erases each row serving now that no region passed with the credential it was made for, and returns
// the regions left without a row.
func (p *CloudPlan) eraseReplaced(ctx context.Context, tx pg.Tx, q *store.Queries, serving, byRef map[string]store.LockCloudCredentialsRow) ([]string, error) {
	kept := map[uuid.UUID]bool{}
	served := map[string]bool{}
	for i, c := range p.Checks {
		if c.Outcome != CheckPassed {
			continue
		}
		served[c.Region] = true
		if row, ok := byRef[p.regions[i].ref]; ok {
			kept[row.ID] = true
		}
	}
	var erased []string
	for _, region := range slices.Sorted(maps.Keys(serving)) {
		r := serving[region]
		if kept[r.ID] {
			continue
		}
		if err := q.EraseCredentialOf(ctx, tx, store.EraseCredentialOfParams{SupplierID: p.supplier, ID: r.ID}); err != nil {
			return nil, fmt.Errorf("erase the credential: %w", err)
		}
		if !served[region] {
			erased = append(erased, region)
		}
	}
	return erased, nil
}

// storeRegion stores region i of a replacement: sealed again in the row made for the same credential, or in a new row.
// A region served now keeps its limits, whichever row serves it from then on; one not served now takes those given.
func (a *Admin) storeRegion(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, p *CloudPlan, i int, byRef, serving map[string]store.LockCloudCredentialsRow) error {
	c := &p.Checks[i]
	concurrency, perMinute := p.r.limits()
	if row, ok := serving[c.Region]; ok {
		concurrency, perMinute = row.MaxConcurrency, row.RequestsPerMinute
	}
	row, ok := byRef[p.regions[i].ref]
	if !ok {
		c.Credential = uuid.NewV7()
		return a.insertRegion(ctx, tx, q, p.supplier, group, p, i, concurrency, perMinute)
	}
	c.Credential = row.ID
	if err := a.resealRegion(ctx, tx, q, p, i); err != nil || row.Status != "erased" {
		return err
	}
	// the row serves its region again, at the region's limits
	if _, err := q.SetCredentialLimits(ctx, tx, store.SetCredentialLimitsParams{
		SupplierID: p.supplier, ID: c.Credential, MaxConcurrency: concurrency, RequestsPerMinute: perMinute,
	}); err != nil {
		return fmt.Errorf("set the region %q's limits: %w", c.Region, err)
	}
	return nil
}

// resealRegion seals the credential again in the row made for it, p.Checks[i].Credential.
func (a *Admin) resealRegion(ctx context.Context, tx pg.Tx, q *store.Queries, p *CloudPlan, i int) error {
	c := &p.Checks[i]
	s, err := a.sealRegion(p, i, c.Credential)
	if err != nil {
		return err
	}
	status := statusColumn(c.UpstreamStatus)
	if err := q.ResealCloudCredential(ctx, tx, store.ResealCloudCredentialParams{
		Ciphertext: s.sealed.Ciphertext, Nonce: s.sealed.Nonce, WrappedDek: s.sealed.WrappedDEK, KeyVersion: &s.keyVersion,
		AllowedModels: s.allowed, CallNames: s.callNames, CheckedAt: &c.CheckedAt, CheckStatus: &status, SupplierID: p.supplier, ID: c.Credential,
	}); err != nil {
		return fmt.Errorf("re-seal the region %q: %w", c.Region, err)
	}
	return nil
}

// lineOf is a direct or cloud supplier's one line, and its ratio.
func lineOf(ctx context.Context, tx pg.Tx, q *store.Queries, supplier uuid.UUID) (uuid.UUID, *big.Rat, error) {
	groups, err := q.ListGroupsOf(ctx, tx, supplier)
	if err != nil {
		return uuid.UUID{}, nil, fmt.Errorf("read the line: %w", err)
	}
	if len(groups) != 1 {
		return uuid.UUID{}, nil, fmt.Errorf("%w: the supplier has %d lines", ErrNoGroup, len(groups))
	}
	ratio, ok := new(big.Rat).SetString(groups[0].Ratio)
	if !ok {
		return uuid.UUID{}, nil, fmt.Errorf("the line's ratio %q is not a number", groups[0].Ratio)
	}
	return groups[0].ID, ratio, nil
}
