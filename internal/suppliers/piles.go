package suppliers

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// Pile is where an upstream name stands for a person sorting a supplier's names (docs/domains/suppliers.md「供应商别名与
// 待映射」).
type Pile string

// The piles.
const (
	// PileReview is a name the catalog does not recognise and no alias covers: the supplier's part of PendingMappings.
	PileReview Pile = "review"
	// PileListed is a name of a model on sale, which has or will get an endpoint.
	PileListed Pile = "listed"
	// PileKnown is a name of a draft model: the catalog knows it, and nobody has listed it.
	PileKnown Pile = "known"
	// PileOutdated is a name nothing needs to be done about: a retired model's, a dated snapshot of a model the catalog
	// knows by its undated name only, or a fine-tune.
	PileOutdated Pile = "outdated"
)

// SupplierModel is one name a group of the supplier lists and the pile it is in. Model is the model it stands for, zero
// in review and for a snapshot or a fine-tune; Suggested is the model a name in review most likely means, zero when none
// is close. CostReason is why the cost of a listed name is unknown, empty when it is not.
type SupplierModel struct {
	Group        uuid.UUID
	UpstreamName string
	Formats      []string
	FirstSeen    time.Time
	Pile         Pile
	Model        catalog.Known
	Endpoint     *uuid.UUID
	Suggested    catalog.Known
	CostReason   CostReason
}

// SupplierModels sorts every name the supplier's groups list now into piles, by group and name. It reuses where a name
// already stands rather than resolving it again: the projection's mapping (a name of a model on sale), the supplier's
// aliases at their current revision (what PendingMappings leaves out), and the catalog's internal recognition, drafts
// included (catalog.RecognizeIn). Only the review pile's suggestion, and telling a snapshot from an unknown name, look at
// other spellings of a name, and neither maps anything.
func (a *Admin) SupplierModels(ctx context.Context, tx pg.Tx, supplier uuid.UUID, owners Owners) ([]SupplierModel, error) {
	if _, err := a.view(ctx, tx, supplier, owners); err != nil {
		return nil, err
	}
	q := store.New()
	rows, err := q.ListSupplierNames(ctx, tx, supplier)
	if err != nil {
		return nil, fmt.Errorf("read the names: %w", err)
	}
	aliasRows, err := q.ListProjectedAliases(ctx, tx, supplier)
	if err != nil {
		return nil, fmt.Errorf("read the aliases: %w", err)
	}
	aliases := make(map[aliasKey]catalog.ModelID, len(aliasRows))
	for _, r := range aliasRows {
		aliases[aliasKey{group: derefID(r.SupplierGroupID), name: r.UpstreamName}] = catalog.ModelID(r.ModelID)
	}
	s := sorter{aliases: aliases}
	if err := s.read(ctx, tx, rows); err != nil {
		return nil, err
	}
	reasons, err := readCostReasons(ctx, tx, supplier)
	if err != nil {
		return nil, err
	}
	models := make([]SupplierModel, len(rows))
	for i, r := range rows {
		models[i] = s.sort(r)
		if m := models[i]; m.Pile == PileListed {
			models[i].CostReason = reasons.of(r.UpstreamRef, r.UpstreamName, uuid.UUID(m.Model.Model), r.EndpointCostState, r.EndpointCostReason)
		}
	}
	return models, nil
}

// aliasKey is an alias's scope: its group, or the zero ID for one of the whole supplier.
type aliasKey struct {
	group uuid.UUID
	name  string
}

func derefID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.UUID{}
	}
	return *id
}

// sorter is what one sorting read: the supplier's aliases, the models they and the projection name, and the catalog's
// recognition of every spelling asked for.
type sorter struct {
	aliases map[aliasKey]catalog.ModelID
	models  map[catalog.ModelID]catalog.Known
	known   map[string]catalog.Known
}

// read reads, in two queries, every model a row names and every spelling sort will ask the catalog about.
func (s *sorter) read(ctx context.Context, tx pg.Tx, rows []store.ListSupplierNamesRow) (err error) {
	var ids []catalog.ModelID
	var names []string
	for _, r := range rows {
		if id, ok := s.mapped(r); ok {
			ids = append(ids, id)
			continue
		}
		names = append(names, r.UpstreamName)
		if undated, dated := catalog.Undated(r.UpstreamName); dated {
			names = append(names, undated)
		}
		names = append(names, spellings(r.UpstreamName)...)
	}
	if s.models, err = catalog.ModelsIn(ctx, tx, ids); err != nil {
		return err
	}
	s.known, err = catalog.RecognizeIn(ctx, tx, names)
	return err
}

// mapped is the model the projection or an alias gives the name, the group's alias before the supplier's, as the
// projection maps it.
func (s *sorter) mapped(r store.ListSupplierNamesRow) (catalog.ModelID, bool) {
	if r.ModelID != nil {
		return catalog.ModelID(*r.ModelID), true
	}
	if id, ok := s.aliases[aliasKey{group: r.SupplierGroupID, name: r.UpstreamName}]; ok {
		return id, true
	}
	id, ok := s.aliases[aliasKey{name: r.UpstreamName}]
	return id, ok
}

// sort puts one name in its pile.
func (s *sorter) sort(r store.ListSupplierNamesRow) SupplierModel {
	m := SupplierModel{Group: r.SupplierGroupID, UpstreamName: r.UpstreamName, Formats: r.Formats, FirstSeen: r.FirstSeenAt, Endpoint: r.EndpointID}
	if id, ok := s.mapped(r); ok {
		m.Model = s.models[id]
	} else {
		m.Model = s.known[r.UpstreamName]
	}
	if m.Model.Model != (catalog.ModelID{}) {
		m.Pile = pileOf(m.Model.Status)
		return m
	}
	m.Model = catalog.Known{} // an alias to a model the catalog does not have maps nothing: the name is to be looked at
	if strings.HasPrefix(strings.ToLower(r.UpstreamName), "ft:") {
		m.Pile = PileOutdated
		return m
	}
	if undated, dated := catalog.Undated(r.UpstreamName); dated {
		if _, ok := s.known[undated]; ok {
			m.Pile = PileOutdated
			return m
		}
	}
	m.Pile = PileReview
	for _, spelling := range spellings(r.UpstreamName) {
		if k, ok := s.known[spelling]; ok && k.Status != catalog.StatusRetired {
			m.Suggested = k
			break
		}
	}
	return m
}

// pileOf is the pile of a name the catalog knows, by its model's status.
func pileOf(status catalog.Status) Pile {
	switch status {
	case catalog.StatusListed:
		return PileListed
	case catalog.StatusDraft:
		return PileKnown
	case catalog.StatusRetired:
	}
	return PileOutdated
}

// spellings are the other ways a relay writes a catalog name, most specific first: without the path a relay puts in
// front (deepseek-ai/DeepSeek-V3, Pro/…), and without a suffix after a colon. They only suggest a model for a person to
// map; nothing is resolved by them.
func spellings(name string) []string {
	var out []string
	if i := strings.LastIndexByte(name, '/'); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
		out = append(out, name)
	}
	if i := strings.IndexByte(name, ':'); i > 0 {
		out = append(out, name[:i])
	}
	return out
}

// PileCounts is how many names each pile has.
type PileCounts struct{ Review, Listed, Known, Outdated int64 }

// Count counts the names of each pile.
func Count(models []SupplierModel) PileCounts {
	var c PileCounts
	for _, m := range models {
		switch m.Pile {
		case PileReview:
			c.Review++
		case PileListed:
			c.Listed++
		case PileKnown:
			c.Known++
		case PileOutdated:
			c.Outdated++
		}
	}
	return c
}
