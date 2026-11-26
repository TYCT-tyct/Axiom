package suppliers

import (
	"cmp"
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
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
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
	// ErrCloudDeployments is deployments named for a platform that lists its models, or none for one that does not.
	ErrCloudDeployments = errors.New("suppliers: Azure OpenAI needs its deployments named, and the others take none")
	// ErrNotCloud is a credential replacement for a supplier that is not a cloud platform's.
	ErrNotCloud = errors.New("suppliers: the supplier is not a cloud platform")
)

type (
	// CloudInput is what a person pasted (CloudCredentialInput in the contract).
	CloudInput = adapter.CloudInput
	// CloudAccount is what is not secret about a cloud credential.
	CloudAccount = adapter.CloudAccount
	// CredentialError is a credential that cannot be one of the platform, by the member at fault.
	CredentialError = adapter.CredentialFormError
)

// CloudRequest is a cloud platform to connect, or the limits of a replacement's new regions. Origin is as
// NormalizeOrigin returns it and only for a platform whose address a person gives; Regions only for one that serves by
// region; Deployments only for one whose models a person names, and not for a replacement, which keeps them; a zero
// Currency is USD. Input is plaintext and is only sealed.
type CloudRequest struct {
	Kind, Name, Origin string
	Input              CloudInput
	Regions            []string
	Deployments        []Deployment
	Currency           platform.Currency
	Limits             Limits
	Reason             string
}

// Deployment is a model a person names for a platform that does not list them (Azure OpenAI): the name its calls
// send, and the model it deploys, empty when it is the name.
type Deployment struct{ Name, Model string }

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
	// lineNames is, for a name a region lists, the other name the line's endpoint of its model is stored by: set once
	// the line's endpoints are written, so that each region's credential can serve them (mapLine).
	lineNames map[string]string
}

// cloudRegion is what a plan holds of one region beside its check: the ref its credential is stored and found by, and
// the models its check listed or a person named (listed false: neither, and the catalog offers them).
type cloudRegion struct {
	ref    string
	models []adapter.CloudModel
	listed bool
}

// Account is what is not secret about the credential.
func (p *CloudPlan) Account() CloudAccount { return p.cred.Account }

// Currency is the settlement currency the supplier will have.
func (p *CloudPlan) Currency() platform.Currency { return p.r.Currency }

// limits are the request's limits as the columns hold them.
func (r CloudRequest) limits() (concurrency int32, perMinute *int32) { return r.Limits.columns() }

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
	if !r.Limits.valid() {
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
	if c.Named() != (len(r.Deployments) > 0) {
		return nil, ErrCloudDeployments
	}
	p, err := planCredential(c, r.Kind, origin, r.Input, r.Regions, deployed(r.Deployments))
	if err != nil {
		return nil, err
	}
	p.r = r
	return p, p.skipStored(ctx, tx)
}

// deployed is the models deployments name, each name once: the first naming it says its model.
func deployed(ds []Deployment) []adapter.CloudModel {
	seen := map[string]bool{}
	var models []adapter.CloudModel
	for _, d := range ds {
		if !seen[d.Name] {
			seen[d.Name] = true
			models = append(models, adapter.CloudModel{Name: d.Name, Known: d.Model})
		}
	}
	return models
}

// planCredential reads the credential and lays out a check for each region, in order; a region named earlier is a
// duplicate. A platform without regions has one check, of no region. named is the models a person named, which every
// region serves; nil for a platform whose check lists them or whose catalog offers them.
func planCredential(c adapter.Cloud, kind, origin string, in CloudInput, regions []string, named []adapter.CloudModel) (*CloudPlan, error) {
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
		p.regions = append(p.regions, cloudRegion{ref: keyRef(cred.Identity + "\x00" + at), models: named, listed: named != nil})
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
			if got.Models != nil { // a platform whose models are named lists none
				p.regions[i].models, p.regions[i].listed = got.Models, true // each goroutine writes only its own
			}
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
// has no supplier. A region another transaction stored since PlanCloud read, or is storing now, is a duplicate here
// too. snap is the catalog the models are mapped by and the official prices come from.
func (a *Admin) CreateCloud(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *CloudPlan) (Connected, error) {
	if err := takeStoreTurn(ctx, tx); err != nil {
		return Connected{}, err
	}
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
	models, priced, err := insertLineModels(ctx, tx, q, id, group, snap, p.r.Currency, p.offer(snap), nil)
	if err != nil {
		return Connected{}, err
	}
	if err := p.mapLine(ctx, tx, q, group, snap); err != nil {
		return Connected{}, err
	}
	if err := a.insertRegions(ctx, tx, q, id, group, p); err != nil {
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

// insertRegions stores each region that passed as a new credential with the request's limits, and the supplier's unit
// value of 1.
func (a *Admin) insertRegions(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, p *CloudPlan) error {
	concurrency, perMinute := p.r.limits()
	for i := range p.Checks {
		if p.Checks[i].Outcome == CheckPassed {
			p.Checks[i].Credential = uuid.NewV7()
			if err := a.insertRegion(ctx, tx, q, id, group, p, i, concurrency, perMinute); err != nil {
				return err
			}
		}
	}
	if err := q.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{
		SupplierID: id, Value: "1", Actor: "cloud", Reason: "a cloud platform's costs are its official prices in the settlement currency",
	}); err != nil {
		return fmt.Errorf("set the cloud platform's unit value: %w", err)
	}
	return nil
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
// serves (none listed is every name, as the catalog offers them) and the ones its calls name otherwise, and the line's
// endpoints it lists by another name (lineServes).
func (a *Admin) sealRegion(p *CloudPlan, i int, credential uuid.UUID) (sealedRegion, error) {
	s := sealedRegion{sealed: a.keys.Seal(p.cred.Key, keyAt(credential))}
	s.keyVersion = int64(s.sealed.KeyVersion)
	r := p.regions[i]
	if !r.listed {
		return s, nil
	}
	names := make([]string, 0, len(r.models))
	calls := map[string]string{}
	for _, m := range r.models {
		names = append(names, m.Name)
		if m.Call != "" && m.Call != m.Name {
			calls[m.Name] = m.Call
		}
	}
	var err error
	s.allowed, s.callNames, err = lineServes(names, calls, p.lineNames)
	return s, err
}

// lineServes is what a credential that lists names serves on a line: the names, and the endpoint's name of each model
// it lists by another name (line, as lineNames reads it), called by the name it lists. calls holds the names its calls
// send otherwise, and callNames is them as the column holds them, none when there are none. Routing pairs a credential
// with an endpoint by the endpoint's name, so a credential that lists a model only by another name would not serve it.
func lineServes(names []string, calls, line map[string]string) (allowed []string, callNames []byte, err error) {
	allowed = slices.Clone(names)
	served := make(map[string]bool, len(names))
	for _, n := range names {
		served[n] = true
	}
	if calls == nil {
		calls = map[string]string{}
	}
	for _, n := range names {
		if e, ok := line[n]; ok && !served[e] {
			allowed, served[e], calls[e] = append(allowed, e), true, cmp.Or(calls[n], n)
		}
	}
	if len(calls) == 0 {
		return allowed, nil, nil
	}
	callNames, err = jsonx.MarshalCanonical(calls)
	return allowed, callNames, err
}

// insertRegion stores region i's credential as a new row, p.Checks[i].Credential, with the limits given.
func (a *Admin) insertRegion(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, p *CloudPlan, i int, concurrency int32, perMinute *int32) error {
	c := &p.Checks[i]
	s, err := a.sealRegion(p, i, c.Credential)
	if err != nil {
		return err
	}
	status := statusColumn(c.UpstreamStatus)
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

// mapLine keeps, for each name a region that passed lists, the other name the line's endpoint of the same model is
// stored by (lineNames).
func (p *CloudPlan) mapLine(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, snap *catalog.Snapshot) error {
	known := map[string]string{}
	var names []string
	for i, c := range p.Checks {
		if c.Outcome == CheckPassed {
			for _, m := range p.regions[i].models {
				names, known[m.Name] = append(names, m.Name), cmp.Or(m.Known, m.Name)
			}
		}
	}
	var err error
	p.lineNames, err = lineNames(ctx, tx, q, group, snap, lineOffer{names: names, known: func(n string) string { return known[n] }})
	return err
}

// lineNames reads the line's endpoints once they are written and gives, for each name of the offer whose model's
// endpoint is stored by another name, that name: a line has one endpoint a model, by the name the first key, region or
// credential to list it gave, and another may list the model by another name.
func lineNames(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, snap *catalog.Snapshot, o lineOffer) (map[string]string, error) {
	line, err := q.LineEndpoints(ctx, tx, group)
	if err != nil {
		return nil, fmt.Errorf("read the line's endpoints: %w", err)
	}
	stored := make(map[catalog.ModelID]string, len(line))
	for _, e := range line {
		stored[catalog.ModelID(e.ModelID)] = e.UpstreamName
	}
	other := map[string]string{}
	for _, name := range o.names {
		mapped := syncer.MapUpstream(snap, nil, o.knownAs(name))
		if e, ok := stored[mapped.Model]; mapped.Mapped && ok && e != name {
			other[name] = e
		}
	}
	return other, nil
}

// knownAs is the name the catalog knows name by.
func (o lineOffer) knownAs(name string) string {
	if o.known == nil {
		return name
	}
	return o.known(name)
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
	detail := map[string]any{"kind": p.kind, "last4": p.cred.Last4, "regions": regions, "max_concurrency": p.r.Limits.MaxConcurrency}
	if p.r.Limits.RequestsPerMinute != nil {
		detail["requests_per_minute"] = *p.r.Limits.RequestsPerMinute
	}
	return detail
}
