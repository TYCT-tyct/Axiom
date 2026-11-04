package persist

import (
	"context"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

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
	declared   []protocol.Format // formats the alias declares beyond the adapter's
	unverified bool              // the name is only a candidate of the version it maps to
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
		state := p.mapping(key)
		formats, err := store.FormatNames(slices.Concat(formatsOf(m.Formats), state.declared))
		if err != nil {
			return fmt.Errorf("model %s/%s: %w", g.Ref, m.Name, err)
		}
		state.formats = formats
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
			SupplierID: p.id, SupplierGroupID: id, UpstreamName: m.Name, ModelID: model, Formats: formats,
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
	var found alias
	var mapped *catalog.ModelID
	if a, ok := p.aliases[aliasKey(key)]; ok {
		found = a
		mapped = &a.model
	} else if a, ok := p.aliases[aliasKey{group: uuid.Nil(), name: key.name}]; ok {
		found = a
		mapped = &a.model
	}
	m := syncer.MapUpstream(p.snap, mapped, key.name)
	if !m.Mapped {
		return nameState{why: m.Why}
	}
	return nameState{model: m.Model, declared: found.formats, unverified: m.Unverified}
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
