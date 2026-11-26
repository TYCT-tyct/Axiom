package suppliers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// Cloud platforms (docs/domains/suppliers.md「云平台」): Vertex AI, Bedrock or Azure OpenAI reached with a credential a
// person pastes. A cloud supplier is a direct API's rows, made the same way: one selected line, the catalog's official
// price as each endpoint's cost, no balance; what differs is that each region the credential passed in is a
// credential row of its own, with its own limits, and that the credential is replaced whole.

// The ways a cloud platform is refused before anything is asked of it.
var (
	// ErrCloudProvider is a kind this node has no cloud platform adapter for.
	ErrCloudProvider = errors.New("suppliers: no cloud platform of that kind")
	// ErrCloudOrigin is an address given for a platform whose address is its own, or none for one whose is not.
	ErrCloudOrigin = errors.New("suppliers: a platform with an address of its own takes none, and Azure OpenAI needs one")
	// ErrCloudRegions is regions given to a platform without them, none to one with them, or one that is not a region.
	ErrCloudRegions = errors.New("suppliers: Vertex AI and Bedrock need regions, and Azure OpenAI takes none")
	// ErrNotCloud is a credential replacement for a supplier that is not a cloud platform's.
	ErrNotCloud = errors.New("suppliers: the supplier is not a cloud platform")
)

// CloudInput is what a person pasted (CloudCredentialInput); CloudAccount is what is not secret about it; a
// *CredentialError is a credential that cannot be one of the platform, by the member at fault.
type (
	CloudInput      = adapter.CloudInput
	CloudAccount    = adapter.CloudAccount
	CredentialError = adapter.CredentialFormError
)

// CloudRequest is a cloud platform to connect, or the limits of a replacement's new regions. Origin is as
// NormalizeOrigin returns it and only for a platform whose address a person gives; Regions only for one that serves by
// region; a zero Currency is USD. Input is plaintext and is only sealed.
type CloudRequest struct {
	Kind, Name, Origin string
	Input              CloudInput
	Regions            []string
	Currency           platform.Currency
	MaxConcurrency     int32
	RequestsPerMinute  *int32
	Reason             string
}

// CloudPlan is a cloud credential about to be stored: the request as checked, the credential as read, and each
// region's check once CheckCloud has asked the platform. It holds the credential in memory only.
type CloudPlan struct {
	r        CloudRequest
	cloud    adapter.Cloud
	kind     string
	origin   string
	cred     adapter.CloudCredential
	supplier uuid.UUID // the supplier a replacement is for; zero for a new one
	Checks   []KeyCheck
	regions  []cloudRegion // one for each of Checks
}

// cloudRegion is what a plan holds of one region beside its check: the ref its credential is stored and found by, and
// the models its check listed (listed false: the platform lists none, and the catalog offers them).
type cloudRegion struct {
	ref    string
	models []adapter.CloudModel
	listed bool
}

// Account is what is not secret about the credential.
func (p *CloudPlan) Account() CloudAccount { return p.cred.Account }

// Currency is the settlement currency the supplier will have.
func (p *CloudPlan) Currency() platform.Currency { return p.r.Currency }

// region is a region's spelling, as the contract's CloudRegion.
var region = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// cloudOf is the cloud platform adapter of kind.
func (a *Admin) cloudOf(kind string) (adapter.Cloud, bool) {
	ad, ok := a.registry.Get(adapter.Kind(kind))
	c, cloud := ad.(adapter.Cloud)
	return c, ok && cloud
}

// PlanCloud checks a request to connect a cloud platform against the platform it names and against itself, and reads
// the credential: a region named twice, or one whose credential another supplier holds now, is a duplicate and is not
// checked. Nothing is asked of the platform and nothing is written.
func (a *Admin) PlanCloud(ctx context.Context, tx pg.Tx, r CloudRequest) (*CloudPlan, error) {
	c, ok := a.cloudOf(r.Kind)
	if !ok {
		return nil, ErrCloudProvider
	}
	if n := len([]rune(strings.TrimSpace(r.Name))); n == 0 || n > 200 {
		return nil, ErrDirectName
	}
	if r.MaxConcurrency < 1 || r.RequestsPerMinute != nil && (*r.RequestsPerMinute < 1 || *r.RequestsPerMinute > MaxRequestsPerMinute) {
		return nil, ErrLimits
	}
	origin := c.Origin()
	if (origin == "") == (r.Origin == "") {
		return nil, ErrCloudOrigin
	}
	if origin == "" {
		origin = r.Origin
	}
	if r.Currency == (platform.Currency{}) {
		usd, err := platform.ParseCurrency("USD")
		if err != nil {
			return nil, err
		}
		r.Currency = usd
	}
	p, err := planCredential(c, r.Kind, origin, r.Input, r.Regions)
	if err != nil {
		return nil, err
	}
	p.r = r
	return p, p.skipStored(ctx, tx)
}

// planCredential reads the credential and lays out a check for each region, in order; a region named earlier is a
// duplicate. A platform without regions has one check, of no region.
func planCredential(c adapter.Cloud, kind, origin string, in CloudInput, regions []string) (*CloudPlan, error) {
	if c.Regional() != (len(regions) > 0) {
		return nil, ErrCloudRegions
	}
	cred, err := c.ReadCredential(in)
	if err != nil {
		return nil, err
	}
	if !c.Regional() {
		regions = []string{""}
	}
	p := &CloudPlan{cloud: c, kind: kind, origin: origin, cred: cred}
	seen := map[string]bool{}
	for _, at := range regions {
		if at != "" && !region.MatchString(at) {
			return nil, ErrCloudRegions
		}
		check := KeyCheck{Last4: cred.Last4, Region: at}
		if seen[at] {
			check.Outcome = CheckDuplicate
		}
		seen[at] = true
		p.Checks = append(p.Checks, check)
		p.regions = append(p.regions, cloudRegion{ref: keyRef(cred.Identity + "\x00" + at)})
	}
	return p, nil
}

// skipStored marks a region whose credential another supplier holds now as a duplicate: two suppliers of one account's
// region would each count its quota as their own.
func (p *CloudPlan) skipStored(ctx context.Context, tx pg.Tx) error {
	refs := make([]string, len(p.regions))
	for i, r := range p.regions {
		refs[i] = r.ref
	}
	stored, err := store.New().StoredRefsOutside(ctx, tx, store.StoredRefsOutsideParams{Refs: refs, SupplierID: p.supplier})
	if err != nil {
		return fmt.Errorf("read the credentials already stored: %w", err)
	}
	for i := range p.Checks {
		if _, found := slices.BinarySearch(stored, p.regions[i].ref); found {
			p.Checks[i] = KeyCheck{Last4: p.Checks[i].Last4, Region: p.Checks[i].Region, Outcome: CheckDuplicate}
			p.regions[i].models, p.regions[i].listed = nil, false
		}
	}
	return nil
}

// CheckCloud asks the platform about every region that is not a duplicate, a few at once, through the outbound client,
// which refuses private and internal addresses; an address a person gave that it refuses fails the whole request. It
// writes nothing.
func (a *Admin) CheckCloud(ctx context.Context, p *CloudPlan) error {
	if p.cloud.Origin() == "" {
		if err := a.CheckOrigin(ctx, p.origin); err != nil {
			return err
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, checkParallel)
	for i := range p.Checks {
		if p.Checks[i].Outcome == CheckDuplicate {
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			got := p.cloud.CheckRegion(ctx, a.egress, p.origin, p.Checks[i].Region, p.cred.Key, a.now())
			p.Checks[i] = checkOf(adapter.KeyCheck{Outcome: got.Outcome, Cause: got.Cause, Status: got.Status}, p.Checks[i], a.now())
			p.regions[i].models, p.regions[i].listed = got.Models, got.Models != nil // each goroutine writes only its own
		})
	}
	wg.Wait()
	return nil
}

// passed says some region passed.
func (p *CloudPlan) passed() bool {
	return slices.ContainsFunc(p.Checks, func(c KeyCheck) bool { return c.Outcome == CheckPassed })
}

// CreateCloud stores what the checks let in, in the caller's transaction: when no region passed, nothing, and Connected
// has no supplier. A region another transaction stored since PlanCloud read is a duplicate here too. snap is the
// catalog the models are mapped by and the official prices come from.
func (a *Admin) CreateCloud(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *CloudPlan) (Connected, error) {
	if err := p.skipStored(ctx, tx); err != nil {
		return Connected{}, err
	}
	if !p.passed() {
		return Connected{Checks: p.Checks}, nil
	}
	account, err := jsonx.MarshalCanonical(p.cred.Account)
	if err != nil {
		return Connected{}, err
	}
	q := store.New()
	id, err := q.InsertCloudSupplier(ctx, tx, store.InsertCloudSupplierParams{
		Name: strings.TrimSpace(p.r.Name), Kind: p.kind, Origin: p.origin, UpstreamAccount: uuid.NewV7().String(),
		SettlementCurrency: p.r.Currency.String(), CloudAccount: account,
	})
	if err != nil {
		return Connected{}, fmt.Errorf("connect the cloud platform: %w", err)
	}
	group, err := insertLine(ctx, tx, q, id, p.kind, p.origin)
	if err != nil {
		return Connected{}, err
	}
	for i := range p.Checks {
		if p.Checks[i].Outcome == CheckPassed {
			p.Checks[i].Credential = uuid.NewV7()
			if err := a.insertRegion(ctx, tx, q, id, group, p, i, p.r.MaxConcurrency, p.r.RequestsPerMinute); err != nil {
				return Connected{}, err
			}
		}
	}
	if err := q.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{
		SupplierID: id, Value: "1", Actor: "cloud", Reason: "a cloud platform's costs are its official prices in the settlement currency",
	}); err != nil {
		return Connected{}, fmt.Errorf("set the cloud platform's unit value: %w", err)
	}
	models, priced, err := insertLineModels(ctx, tx, q, id, group, snap, p.r.Currency, p.offer(snap), nil)
	if err != nil {
		return Connected{}, err
	}
	detail := cloudAudit(p)
	detail["origin"], detail["settlement_currency"], detail["models"], detail["priced_endpoints"] = p.origin, p.r.Currency.String(), models, priced
	if err := a.record(ctx, tx, actor, "supplier.cloud_connect", id, p.r.Reason, detail); err != nil {
		return Connected{}, err
	}
	s, err := a.view(ctx, tx, id, EveryOwner)
	if err != nil {
		return Connected{}, err
	}
	return Connected{Supplier: &s, Checks: p.Checks}, nil
}

// sealedRegion is the columns of a region's credential that a check decides: the sealed credential, its ref and
// last four characters, the names the region serves and how its calls name them.
type sealedRegion struct {
	sealed     secret.Sealed
	keyVersion int64
	allowed    []string
	callNames  []byte
}

// sealRegion seals the plan's credential for the row credential, with what region i's check listed: the names it
// serves (none listed is every name, as the catalog offers them) and the ones its calls name otherwise.
func (a *Admin) sealRegion(p *CloudPlan, i int, credential uuid.UUID) (sealedRegion, error) {
	s := sealedRegion{sealed: a.keys.Seal(p.cred.Key, keyAt(credential))}
	s.keyVersion = int64(s.sealed.KeyVersion)
	r := p.regions[i]
	if !r.listed {
		return s, nil
	}
	s.allowed = make([]string, 0, len(r.models))
	calls := map[string]string{}
	for _, m := range r.models {
		s.allowed = append(s.allowed, m.Name)
		if m.Call != "" && m.Call != m.Name {
			calls[m.Name] = m.Call
		}
	}
	if len(calls) > 0 {
		b, err := jsonx.MarshalCanonical(calls)
		if err != nil {
			return s, err
		}
		s.callNames = b
	}
	return s, nil
}

// insertRegion stores region i's credential as a new row, p.Checks[i].Credential, with the limits given.
func (a *Admin) insertRegion(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, p *CloudPlan, i int, concurrency int32, perMinute *int32) error {
	c := &p.Checks[i]
	s, err := a.sealRegion(p, i, c.Credential)
	if err != nil {
		return err
	}
	status := int32(c.UpstreamStatus) //nolint:gosec // an HTTP status
	if err := q.InsertCloudCredential(ctx, tx, store.InsertCloudCredentialParams{
		ID: c.Credential, SupplierID: id, SupplierGroupID: group, UpstreamKeyRef: p.regions[i].ref,
		Ciphertext: s.sealed.Ciphertext, Nonce: s.sealed.Nonce, WrappedDek: s.sealed.WrappedDEK, KeyVersion: &s.keyVersion,
		MaxConcurrency: concurrency, RequestsPerMinute: perMinute, AllowedModels: s.allowed, CallNames: s.callNames,
		Hint: &c.Last4, Region: regionColumn(c.Region), CheckedAt: &c.CheckedAt, CheckStatus: &status,
	}); err != nil {
		return fmt.Errorf("store the region %q: %w", c.Region, err)
	}
	return nil
}

// regionColumn is a region as the column holds it: none for a platform without regions.
func regionColumn(r string) *string {
	if r == "" {
		return nil
	}
	return &r
}

// offer is what the regions that passed serve: the names their checks listed, and for a platform that lists none,
// every model on sale it serves by the names the catalog's give it.
func (p *CloudPlan) offer(snap *catalog.Snapshot) lineOffer {
	known := map[string]string{}
	var names []string
	offered := false
	for i, c := range p.Checks {
		if c.Outcome != CheckPassed {
			continue
		}
		r := p.regions[i]
		if !r.listed && !offered {
			offered = true
			names = append(names, p.offered(snap, known)...)
		}
		for _, m := range r.models {
			names = append(names, m.Name)
			if m.Known != "" {
				known[m.Name] = m.Known
			}
		}
	}
	slices.Sort(names)
	return lineOffer{
		names: slices.Compact(names), formats: p.cloud.Formats, unmapped: p.cloud.Unmapped(),
		known: func(name string) string {
			if k, ok := known[name]; ok {
				return k
			}
			return name
		},
	}
}

// offered is the name the platform gives each model on sale it serves, from the name the model's vendor knows it by,
// which is put in known.
func (p *CloudPlan) offered(snap *catalog.Snapshot, known map[string]string) []string {
	var names []string
	for m := range snap.Models() {
		official, ok := snap.OfficialName(m.ID)
		if !ok || len(p.cloud.Formats(m)) == 0 {
			continue
		}
		if name, ok := p.cloud.Offer(m, official); ok {
			names, known[name] = append(names, name), official
		}
	}
	return names
}

// cloudAudit is what an audit entry says of a credential's checks: the platform, the credential's last four
// characters, each region's outcome and the limits. No credential is in it.
func cloudAudit(p *CloudPlan) map[string]any {
	regions := make([]map[string]any, len(p.Checks))
	for i, c := range p.Checks {
		regions[i] = map[string]any{"region": c.Region, "outcome": string(c.Outcome)}
	}
	detail := map[string]any{"kind": p.kind, "last4": p.cred.Last4, "regions": regions, "max_concurrency": p.r.MaxConcurrency}
	if p.r.RequestsPerMinute != nil {
		detail["requests_per_minute"] = *p.r.RequestsPerMinute
	}
	return detail
}

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
	switch {
	case s.Mode != "cloud":
		return nil, ErrNotCloud
	case Status(s.Status) == Removing || Status(s.Status) == Removed:
		return nil, fmt.Errorf("%w: it is %s", ErrStatusLocked, s.Status)
	}
	c, ok := a.cloudOf(s.Kind)
	if !ok {
		return nil, ErrNoAdapter
	}
	if r.MaxConcurrency < 1 || r.RequestsPerMinute != nil && (*r.RequestsPerMinute < 1 || *r.RequestsPerMinute > MaxRequestsPerMinute) {
		return nil, ErrLimits
	}
	regions := r.Regions
	if regions == nil && c.Regional() {
		if regions, err = servedRegions(ctx, tx, supplier); err != nil {
			return nil, err
		}
	}
	if r.Currency, err = platform.ParseCurrency(s.SettlementCurrency); err != nil {
		return nil, fmt.Errorf("the supplier's settlement currency: %w", err)
	}
	p, err := planCredential(c, s.Kind, s.Origin, r.Input, regions)
	if err != nil {
		return nil, err
	}
	p.r, p.supplier = r, supplier
	return p, p.skipStored(ctx, tx)
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

// ReplaceCloud puts the new credential in place in the caller's transaction, under the supplier's lock: each region
// that passed is the supplier's from then on, re-sealed in the region's own row with that row's limits, or a new row
// with the limits given; every other region is erased. When no region passed nothing changes and Connected has no
// supplier. Calls in flight end with the credential they hold. The models a new region lists get endpoints.
func (a *Admin) ReplaceCloud(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *CloudPlan) (Connected, error) {
	if _, err := a.locked(ctx, tx, p.supplier); err != nil {
		return Connected{}, err
	}
	if err := p.skipStored(ctx, tx); err != nil {
		return Connected{}, err
	}
	if !p.passed() {
		return Connected{Checks: p.Checks}, nil
	}
	q := store.New()
	group, err := lineOf(ctx, tx, q, p.supplier)
	if err != nil {
		return Connected{}, err
	}
	erased, err := a.replaceRegions(ctx, tx, q, group, p)
	if err != nil {
		return Connected{}, err
	}
	account, err := jsonx.MarshalCanonical(p.cred.Account)
	if err != nil {
		return Connected{}, err
	}
	if err := q.SetCloudAccount(ctx, tx, store.SetCloudAccountParams{CloudAccount: account, ID: p.supplier}); err != nil {
		return Connected{}, fmt.Errorf("set the cloud account: %w", err)
	}
	served, err := q.LineEndpointModels(ctx, tx, group)
	if err != nil {
		return Connected{}, fmt.Errorf("read the line's endpoints: %w", err)
	}
	has := make(map[catalog.ModelID]bool, len(served))
	for _, m := range served {
		has[catalog.ModelID(m)] = true
	}
	if _, _, err := insertLineModels(ctx, tx, q, p.supplier, group, snap, p.r.Currency, p.offer(snap), has); err != nil {
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
	kept := map[uuid.UUID]bool{}
	for i, c := range p.Checks {
		if row, ok := byRef[p.regions[i].ref]; ok && c.Outcome == CheckPassed {
			kept[row.ID] = true
		}
	}
	var erased []string
	for _, r := range rows {
		if r.Status == "erased" || kept[r.ID] {
			continue
		}
		if err := q.EraseCredentialOf(ctx, tx, store.EraseCredentialOfParams{SupplierID: p.supplier, ID: r.ID}); err != nil {
			return nil, fmt.Errorf("erase the credential: %w", err)
		}
		if served := deref(r.Region); !slices.ContainsFunc(p.Checks, func(c KeyCheck) bool { return c.Outcome == CheckPassed && c.Region == served }) {
			erased = append(erased, served)
		}
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

// storeRegion stores region i of a replacement: sealed again in the row made for the same credential, or in a new row
// with the limits of the one it replaces, or those given.
func (a *Admin) storeRegion(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, p *CloudPlan, i int, byRef, serving map[string]store.LockCloudCredentialsRow) error {
	c := &p.Checks[i]
	concurrency, perMinute := p.r.MaxConcurrency, p.r.RequestsPerMinute
	if row, ok := byRef[p.regions[i].ref]; ok {
		c.Credential = row.ID
		if err := a.resealRegion(ctx, tx, q, p, i); err != nil {
			return err
		}
		if row.Status != "erased" {
			return nil
		}
	} else {
		if row, ok := serving[c.Region]; ok {
			concurrency, perMinute = row.MaxConcurrency, row.RequestsPerMinute
		}
		c.Credential = uuid.NewV7()
		return a.insertRegion(ctx, tx, q, p.supplier, group, p, i, concurrency, perMinute)
	}
	// a region served again is a new one, which takes the limits given
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
	status := int32(c.UpstreamStatus) //nolint:gosec // an HTTP status
	if err := q.ResealCloudCredential(ctx, tx, store.ResealCloudCredentialParams{
		Ciphertext: s.sealed.Ciphertext, Nonce: s.sealed.Nonce, WrappedDek: s.sealed.WrappedDEK, KeyVersion: &s.keyVersion,
		AllowedModels: s.allowed, CallNames: s.callNames, CheckedAt: &c.CheckedAt, CheckStatus: &status, SupplierID: p.supplier, ID: c.Credential,
	}); err != nil {
		return fmt.Errorf("re-seal the region %q: %w", c.Region, err)
	}
	return nil
}

// lineOf is a direct or cloud supplier's one line.
func lineOf(ctx context.Context, tx pg.Tx, q *store.Queries, supplier uuid.UUID) (uuid.UUID, error) {
	groups, err := q.ListGroupsOf(ctx, tx, supplier)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("read the line: %w", err)
	}
	if len(groups) != 1 {
		return uuid.UUID{}, fmt.Errorf("%w: the supplier has %d lines", ErrNoGroup, len(groups))
	}
	return groups[0].ID, nil
}
