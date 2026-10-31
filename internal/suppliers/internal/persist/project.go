package persist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// The projection writes onto the supplier's own tables what routing and the back office read of the engine's state
// (internal/suppliers/README.md「持久化」): the groups the supplier sells, the models of each with the catalog model it maps
// to, and an endpoint for every one of those that is on sale and has a cost plan, with that plan as its cost schedule.
// Only differences are written, so upstream names, formats and plans that came back the same leave every row as it was;
// that is what makes a listing read twice write nothing outside sync_events.

// projector writes one supplier's listing onto its tables, in the transaction that stores the state it came from.
type projector struct {
	writes        store.ProjectionWrites
	q             *store.Queries
	tx            pg.Tx
	id            uuid.UUID
	st            syncer.State
	snap          *catalog.Snapshot
	nativeUnit    string
	now           time.Time
	directFormats bool                         // whether direct formats may be written; see Config.DirectFormats
	aliases       map[aliasKey]catalog.ModelID // the supplier's aliases, by the group they hold in
	plans         map[string]store.PlanVersion
}

// aliasKey is the scope of an alias: the group it holds in, or the zero id for the whole supplier.
type aliasKey struct {
	group uuid.UUID
	name  string
}

// modelKey names an endpoint: a group and the catalog model one of its names stands for.
type modelKey struct {
	group, model uuid.UUID
}

// nameKey names a model as the supplier does: a group and one of its upstream names.
type nameKey struct {
	group uuid.UUID
	name  string
}

// nameState is what the listing says of one upstream name: the catalog model it stands for, the formats it speaks, and
// why it stands for no model.
type nameState struct {
	model      catalog.ModelID
	formats    []string
	unverified bool // the name is only a candidate of the version it maps to
	why        syncer.Reason
}

// desiredEndpoint is what the listing says an endpoint should be: the name a call sends upstream, the formats it speaks,
// and the cost plan it is routed by, or none.
type desiredEndpoint struct {
	upstream string
	formats  []string
	plan     *store.PlanVersion
	exact    bool // the name is evidence of the model's version, so it keeps the endpoint over a candidate
}

// projectListing writes the groups, models, endpoints and cost schedules of a listing already read, and leaves every row
// that the listing agrees with alone.
func projectListing(ctx context.Context, tx pg.Tx, id uuid.UUID, st syncer.State, cur store.LockSupplierRow, snap *catalog.Snapshot, now time.Time, directFormats bool) error {
	p := &projector{q: store.New(), tx: tx, id: id, st: st, snap: snap, nativeUnit: cur.NativeUnit, now: now, directFormats: directFormats}
	if err := p.read(ctx); err != nil {
		return err
	}
	groups, err := p.groups(ctx)
	if err != nil {
		return err
	}
	states, err := p.models(ctx, groups)
	if err != nil {
		return err
	}
	if err := p.endpoints(ctx, groups, states); err != nil {
		return err
	}
	return p.writes.Apply(ctx, tx)
}

// read reads the supplier's aliases and the schedules of the plans in force, which the writes below compare and copy.
func (p *projector) read(ctx context.Context) (err error) {
	if p.aliases, err = p.readAliases(ctx); err != nil {
		return err
	}
	p.plans, err = p.readPlans(ctx)
	return err
}

// readAliases reads the aliases of the supplier, which mapping an upstream name tries first. A group's alias is kept in
// the group's scope, so the same name may stand for different models in different groups.
func (p *projector) readAliases(ctx context.Context) (map[aliasKey]catalog.ModelID, error) {
	rows, err := p.q.ListProjectedAliases(ctx, p.tx, p.id)
	if err != nil {
		return nil, fmt.Errorf("read the aliases: %w", err)
	}
	out := make(map[aliasKey]catalog.ModelID, len(rows))
	for _, r := range rows {
		out[aliasKey{group: derefUUID(r.SupplierGroupID), name: r.UpstreamName}] = catalog.ModelID(r.ModelID)
	}
	return out, nil
}

// readPlans reads the schedules of the cost plans in force. The engine keeps only their fingerprints in its state, and a
// name mapped after its plan was worked out must still find that plan: it is read back from where every version is kept.
func (p *projector) readPlans(ctx context.Context) (map[string]store.PlanVersion, error) {
	var keys []store.PlanVersion
	for _, g := range p.st.Groups {
		for _, m := range g.Models {
			if m.Plan != "" {
				keys = append(keys, store.PlanVersion{Endpoint: syncer.PlanKey(g.Ref, m.Name), Fingerprint: m.Plan})
			}
		}
	}
	rows, err := store.PlanVersions(ctx, p.tx, p.id, keys)
	if err != nil {
		return nil, fmt.Errorf("read the cost plans: %w", err)
	}
	out := make(map[string]store.PlanVersion, len(rows))
	for _, r := range rows {
		out[r.Endpoint] = r
	}
	return out, nil
}

// groups makes and changes the groups of the listing and returns their ids by the supplier's ref. A group is made
// unselected: which groups to use is a person's choice, and the projection never touches it or the codename.
func (p *projector) groups(ctx context.Context) (map[string]uuid.UUID, error) {
	rows, err := p.q.ListProjectedGroups(ctx, p.tx, p.id)
	if err != nil {
		return nil, fmt.Errorf("read the groups: %w", err)
	}
	seen := make(map[string]bool, len(p.st.Groups))
	ids := make(map[string]uuid.UUID, len(p.st.Groups))
	stored := make(map[string]store.ListProjectedGroupsRow, len(rows))
	for _, r := range rows {
		stored[r.UpstreamRef] = r
	}
	pool := make(map[string]syncer.PoolCapacity, len(p.st.Pool))
	for _, g := range p.st.Pool {
		pool[g.GroupRef] = g
	}
	for _, g := range p.st.Groups {
		seen[g.Ref] = true
		id, err := p.group(g, stored[g.Ref])
		if err != nil {
			return nil, err
		}
		ids[g.Ref] = id
		capacity, ok := pool[g.Ref]
		p.pool(id, stored[g.Ref], &capacity, ok)
	}
	// A group the listing no longer has is kept, with its endpoints and codename, and marked absent: nothing here is
	// deleted, and the supplier may sell it again. Its pool reading is cleared: it is not in the pool's answer any more.
	for _, r := range rows {
		if !seen[r.UpstreamRef] {
			if r.Present {
				p.writes.AbsentGroups = append(p.writes.AbsentGroups, r.ID)
			}
			p.pool(r.ID, r, nil, false)
		}
	}
	return ids, nil
}

// group writes one group of the listing, if what the tables hold differs from it, and returns its id. found is the zero
// row of a group that is not in the tables yet.
func (p *projector) group(g syncer.GroupSnap, found store.ListProjectedGroupsRow) (uuid.UUID, error) {
	facts, err := factsObject(g.Facts)
	if err != nil {
		return uuid.Nil(), fmt.Errorf("group %s: %w", g.Ref, err)
	}
	if found.ID == uuid.Nil() {
		id := uuid.NewV7()
		p.writes.Groups = append(p.writes.Groups, store.InsertProjectedGroupParams{
			ID:         id,
			SupplierID: p.id, UpstreamRef: g.Ref, Name: g.Name, Ratio: g.Ratio, Facts: facts, SeenAt: &p.now,
		})
		return id, nil
	}
	was, err := factsObject(found.Facts)
	if err != nil {
		return uuid.Nil(), fmt.Errorf("group %s: what the table holds: %w", g.Ref, err)
	}
	if found.Name == g.Name && sameRatio(found.Ratio, g.Ratio) && found.Present && bytes.Equal(was, facts) {
		return found.ID, nil
	}
	p.writes.ChangedGroups = append(p.writes.ChangedGroups, store.UpdateProjectedGroupParams{
		ID: found.ID, Name: g.Name, Ratio: g.Ratio, Facts: facts, SeenAt: &p.now,
	})
	return found.ID, nil
}

// pool writes what the management API of a pool last said of a group, or clears it when the pool no longer reports the
// group. now is the reading and ok whether there is one; the row and the reading are compared so a pool whose numbers
// did not change writes nothing, like the listing it belongs to.
func (p *projector) pool(id uuid.UUID, found store.ListProjectedGroupsRow, now *syncer.PoolCapacity, ok bool) {
	if !ok {
		if found.PoolAccounts != nil {
			p.writes.ClearedPool = append(p.writes.ClearedPool, id)
		}
		return
	}
	if found.PoolAccounts != nil && int(*found.PoolAccounts) == now.Accounts && int(*found.PoolFailed) == now.Failed &&
		int(*found.PoolUsable) == now.Usable && samePoolInt(found.PoolHeadroom, now.Headroom) &&
		samePoolInt(found.PoolCapacity, now.Capacity) && found.PoolLow == now.Low {
		return
	}
	params := store.SetProjectedGroupPoolParams{
		ID: id, Accounts: poolInt(now.Accounts), Failed: poolInt(now.Failed), Usable: poolInt(now.Usable),
		Headroom: poolIntPtr(now.Headroom), Capacity: poolIntPtr(now.Capacity),
		Low: now.Low, ObservedAt: &p.now,
	}
	p.writes.Pool = append(p.writes.Pool, params)
}

func poolInt(v int) *int32 { n := int32(v); return &n } //nolint:gosec // G115: bounded by maxPoolPages*poolPageSize, far below 2³¹

func poolIntPtr(v *int) *int32 {
	if v == nil {
		return nil
	}
	return poolInt(*v)
}

func samePoolInt(stored *int32, now *int) bool {
	if stored == nil || now == nil {
		return stored == nil && now == nil
	}
	return int(*stored) == *now
}

// models writes every name of the listing with the catalog model it stands for, and returns what the listing says of
// each. A name that maps to no model is kept with a null model, which is what the queue of names to map lists; one that
// left the listing is marked absent rather than deleted.
func (p *projector) models(ctx context.Context, groups map[string]uuid.UUID) (map[nameKey]nameState, error) {
	rows, err := p.q.ListProjectedModels(ctx, p.tx, p.id)
	if err != nil {
		return nil, fmt.Errorf("read the models: %w", err)
	}
	stored := make(map[nameKey]store.ListProjectedModelsRow, len(rows))
	for _, r := range rows {
		stored[nameKey{group: r.SupplierGroupID, name: r.UpstreamName}] = r
	}
	seen := make(map[nameKey]bool, len(stored))
	states := make(map[nameKey]nameState, len(stored))
	for _, g := range p.st.Groups {
		if err := p.groupModels(g, groups[g.Ref], stored, seen, states); err != nil {
			return nil, err
		}
	}
	for key, r := range stored {
		if seen[key] || !r.Present {
			continue
		}
		p.writes.AbsentModels = append(p.writes.AbsentModels, store.SetProjectedModelAbsentParams{SupplierGroupID: key.group, UpstreamName: key.name})
	}
	return states, nil
}

// groupModels writes the names of one group, filling what the listing says of each into seen and states.
func (p *projector) groupModels(g syncer.GroupSnap, id uuid.UUID, stored map[nameKey]store.ListProjectedModelsRow, seen map[nameKey]bool, states map[nameKey]nameState) error {
	for _, m := range g.Models {
		key := nameKey{group: id, name: m.Name}
		// A group the supplier will not sell us — a subscription group, one priced by the hour — still lists its models,
		// with no call format: nothing could be called there. Such a name is not a row (the table keeps at least one
		// format) and not something to map, so a row it had before is marked absent below; an endpoint of it says why.
		if len(m.Formats) == 0 {
			states[key] = nameState{why: syncer.NoFormat}
			continue
		}
		seen[key] = true
		formats := writableFormats(formatsOf(m.Formats), p.directFormats)
		if len(formats) == 0 {
			states[key] = nameState{why: syncer.DirectFormatOff}
			continue
		}
		names, err := store.FormatNames(formats)
		if err != nil {
			return fmt.Errorf("model %s/%s: %w", g.Ref, m.Name, err)
		}
		state := p.mapping(key)
		state.formats = names
		states[key] = state
		if was, ok := stored[key]; ok && sameName(was, state) {
			continue
		}
		var model *uuid.UUID
		if state.model != (catalog.ModelID{}) {
			mapped := uuid.UUID(state.model)
			model = &mapped
		}
		p.writes.Models = append(p.writes.Models, store.UpsertProjectedModelParams{
			SupplierID: p.id, SupplierGroupID: id, UpstreamName: m.Name, ModelID: model, Formats: names,
		})
	}
	return nil
}

// sameName reports whether a stored model says what the listing says of it.
func sameName(was store.ListProjectedModelsRow, state nameState) bool {
	return was.Present && derefUUID(was.ModelID) == uuid.UUID(state.model) && slices.Equal(was.Formats, state.formats)
}

// mapping is where an upstream name stands: the supplier's alias for it, else the catalog's own names, and only for a
// model that is on sale (syncer.MapUpstream).
func (p *projector) mapping(key nameKey) nameState {
	var alias *catalog.ModelID
	if id, ok := p.aliases[aliasKey(key)]; ok {
		alias = &id
	} else if id, ok := p.aliases[aliasKey{group: uuid.Nil(), name: key.name}]; ok {
		alias = &id
	}
	m := syncer.MapUpstream(p.snap, alias, key.name)
	if !m.Mapped {
		return nameState{why: m.Why}
	}
	return nameState{model: m.Model, unverified: m.Unverified}
}

// endpoints makes and changes the endpoints of the listing's models and the cost schedules they are routed by. An endpoint
// is made only when its model is on sale and has a cost plan: one without a plan is not routable, and its row appears when
// the plan does. A row that is no longer routable keeps its id and its switch, and is taken out of routing.
func (p *projector) endpoints(ctx context.Context, groups map[string]uuid.UUID, states map[nameKey]nameState) error {
	desired := p.desired(groups, states)
	rows, err := p.q.ListProjectedEndpoints(ctx, p.tx, p.id)
	if err != nil {
		return fmt.Errorf("read the endpoints: %w", err)
	}
	for _, r := range rows {
		p.endpoint(r, desired, states)
		delete(desired, modelKey{group: r.SupplierGroupID, model: r.ModelID})
	}
	// What is left is an endpoint of a model that is on sale with a cost plan, and no row for it yet.
	for _, key := range slices.SortedFunc(maps.Keys(desired), compareModelKeys) {
		p.makeEndpoint(key, desired[key])
	}
	return nil
}

func compareModelKeys(a, b modelKey) int {
	if c := a.group.Compare(b.group); c != 0 {
		return c
	}
	return a.model.Compare(b.model)
}

// desired is what the listing says each endpoint of a model on sale should be, by group and catalog model. Two names of
// one group may stand for the same model, but an endpoint is a group's model: the name that is evidence of the version
// keeps it, and between two of a kind the first by name does, so that the choice does not depend on the order the listing
// was read in.
func (p *projector) desired(groups map[string]uuid.UUID, states map[nameKey]nameState) map[modelKey]desiredEndpoint {
	out := map[modelKey]desiredEndpoint{}
	for _, g := range p.st.Groups {
		id := groups[g.Ref]
		for _, m := range g.Models {
			key, d, ok := p.wanted(g.Ref, id, m, states[nameKey{group: id, name: m.Name}])
			if !ok {
				continue
			}
			// The name that is evidence of the version takes the endpoint; between two of a kind the one the listing
			// reads first keeps it.
			if had, ok := out[key]; ok && (had.exact || !d.exact) {
				continue
			}
			out[key] = d
		}
	}
	return out
}

// wanted is the endpoint one name of the listing asks for: not one at all when the name maps to no model, whatever its
// plan says.
func (p *projector) wanted(ref string, group uuid.UUID, m syncer.ModelSnap, state nameState) (modelKey, desiredEndpoint, bool) {
	if state.model == (catalog.ModelID{}) {
		return modelKey{}, desiredEndpoint{}, false
	}
	d := desiredEndpoint{upstream: m.Name, formats: state.formats, exact: !state.unverified}
	if m.Plan != "" {
		if v, ok := p.plans[syncer.PlanKey(ref, m.Name)]; ok && v.Fingerprint == m.Plan {
			d.plan = &v
		}
	}
	return modelKey{group: group, model: uuid.UUID(state.model)}, d, true
}

// endpoint brings one stored endpoint in line with the listing: the name and formats a call uses, and the cost schedule
// in force. A row the listing no longer has at all is taken out of routing, with the reason its name gives.
func (p *projector) endpoint(r store.ListProjectedEndpointsRow, desired map[modelKey]desiredEndpoint, states map[nameKey]nameState) {
	d, ok := desired[modelKey{group: r.SupplierGroupID, model: r.ModelID}]
	if ok {
		if r.UpstreamName != d.upstream || !slices.Equal(r.Formats, d.formats) {
			p.writes.ChangedEndpoints = append(p.writes.ChangedEndpoints, store.UpdateProjectedEndpointParams{
				ID: r.ID, UpstreamName: d.upstream, Formats: d.formats,
			})
		}
		p.setCost(r, d)
		return
	}
	why := states[nameKey{group: r.SupplierGroupID, name: r.UpstreamName}].why
	if why == syncer.Routable {
		why = syncer.Gone
	}
	p.clearCost(r, why)
}

// setCost puts the endpoint's cost plan in force. The plan in force is named by the fingerprint of its schedule: a row
// that already has this plan is left alone, and a new version of the plan replaces the old one, which stays in
// cost_schedules because what a settled attempt was priced by must remain readable.
func (p *projector) setCost(r store.ListProjectedEndpointsRow, d desiredEndpoint) {
	if d.plan == nil {
		p.clearCost(r, syncer.CostUnknown)
		return
	}
	if r.Fingerprint == d.plan.Fingerprint {
		// The plan is the one in force. A row the projection itself took down comes back; a row reconciliation took down
		// stays down, because its verdict is that this estimate is too low, and the estimate has not changed.
		if r.CostState != "known" && ours(r.CostStateReason) {
			p.putCost(r.ID, *d.plan)
			return
		}
		return
	}
	p.putCost(r.ID, *d.plan)
}

// putCost puts a cost schedule of an endpoint in force, making the row when the endpoint has none for this plan.
func (p *projector) putCost(endpoint uuid.UUID, plan store.PlanVersion) {
	p.writes.Costs = append(p.writes.Costs, store.EnsureCostScheduleParams{
		SupplierID: p.id, EndpointID: endpoint, NativeUnit: p.nativeUnit,
		Schedule: []byte(plan.Schedule), Basis: plan.Basis, Fingerprint: plan.Fingerprint,
	})
}

// clearCost takes an endpoint with no cost plan out of routing, saying why. The row, its id and its enabled switch stay:
// what an attempt was priced by is a schedule of this endpoint, and a person may have switched it off.
func (p *projector) clearCost(r store.ListProjectedEndpointsRow, why syncer.Reason) {
	if r.CostState != "known" {
		return
	}
	p.writes.UnknownCosts = append(p.writes.UnknownCosts, store.UnsetProjectedEndpointCostParams{ID: r.ID, Reason: reason(why)})
}

// makeEndpoint makes an endpoint of a model that is on sale with a cost plan, and puts the plan in force. enabled takes
// its default, which is on: the projection makes a row once and never changes the switch afterwards.
func (p *projector) makeEndpoint(key modelKey, d desiredEndpoint) {
	if d.plan == nil {
		return
	}
	id := uuid.NewV7()
	p.writes.Endpoints = append(p.writes.Endpoints, store.InsertProjectedEndpointParams{
		ID:         id,
		SupplierID: p.id, SupplierGroupID: key.group, ModelID: key.model, UpstreamName: d.upstream, Formats: d.formats,
	})
	p.putCost(id, *d.plan)
}

// ours reports whether the reason an endpoint carries is one the projection wrote: every reason it clears an endpoint
// with, and nothing else. Any other is reconciliation's: it takes an endpoint down when the supplier's charges came out
// above our estimate, and only a new estimate or a person lifts that. A reason left out here is one the projection can
// write and never lift, so the endpoint would stay out of routing after its model came back.
func ours(found *string) bool {
	if found == nil {
		return true
	}
	for _, why := range [...]syncer.Reason{syncer.Unmapped, syncer.NotListed, syncer.NoFormat, syncer.CostUnknown, syncer.Gone} {
		if *found == string(why) {
			return true
		}
	}
	return false
}

// sameRatio compares two multipliers by value, not by text: the column is numeric, so it holds 1 and 1.00 alike, and a
// person who typed either should not have it rewritten by every sync.
func sameRatio(a, b string) bool {
	x, ok := new(big.Rat).SetString(a)
	if !ok {
		return a == b
	}
	y, ok := new(big.Rat).SetString(b)
	if !ok {
		return a == b
	}
	return x.Cmp(y) == 0
}

// reason is the text of why stored with the endpoint; it is nil when there is nothing to say.
func reason(why syncer.Reason) *string {
	if why == syncer.Routable || why == "" {
		return nil
	}
	text := string(why)
	return &text
}

// preP18Formats are the formats a node released before P18 can read: the closed gate writes no other.
var preP18Formats = []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat}

// writableFormats drops the formats a pre-P18 node cannot read while the compatibility gate is closed, so this release
// cannot write a name that node would refuse to read. It names what that node knows rather than what is new, so a
// format added later is held back too until the gate opens.
func writableFormats(formats []protocol.Format, direct bool) []protocol.Format {
	if direct {
		return formats
	}
	return slices.DeleteFunc(formats, func(f protocol.Format) bool { return !slices.Contains(preP18Formats, f) })
}

// formatsOf reads the formats an engine snapshot stores, which protocol's are.
func formatsOf(formats []int) []protocol.Format {
	out := make([]protocol.Format, len(formats))
	for i, f := range formats {
		out[i] = protocol.Format(f) //nolint:gosec // the engine stores protocol.Format's numbers
	}
	return out
}

// factsObject is a group's facts as the jsonb column holds them, so that what the table gives back compares equal to
// what the listing says. Facts that are absent are the empty object the column defaults to.
func factsObject(raw string) ([]byte, error) {
	if raw == "" {
		return []byte("{}"), nil
	}
	var v any
	if err := jsonx.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("the facts are not JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("the facts are not a JSON object")
	}
	return jsonx.MarshalCanonical(v)
}

func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil()
	}
	return *id
}
