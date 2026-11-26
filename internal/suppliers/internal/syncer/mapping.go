package syncer

import "axiom/internal/catalog"

// Catalog is the part of a catalog snapshot that maps an upstream name: *catalog.Snapshot is one.
type Catalog interface {
	Recognize(name string) (catalog.Recognition, bool)
	Model(id catalog.ModelID) (catalog.Model, bool)
}

// Mapping is where an upstream model name stands in the catalog.
type Mapping struct {
	Model catalog.ModelID
	// Mapped is set when the name stands for a listed model. An endpoint of a name that is not mapped is not made, and
	// never reaches routing; the name waits in the queue of names to map.
	Mapped bool
	// Unverified is set when the name is only a candidate: the catalog says it is not evidence of the version, because
	// the vendor may have moved it to a newer one. The endpoint is made but must pass the authenticity check before the
	// mapping is confirmed (docs/domains/suppliers.md「供应商别名与待映射」).
	Unverified bool
	// Why is why the name is not mapped, Routable when it is: the catalog does not know the name, or knows the model
	// and it is not on sale. It is written with the endpoint the name loses, so a person reading the row sees why.
	Why Reason
}

// MapUpstream maps an upstream name by the first two steps of docs/domains/suppliers.md「供应商别名与待映射」: the
// supplier's own alias for the name, which the caller has looked up in its tables (nil if it has none), and then the
// catalog's internal recognition, public and internal names alike. The third step, a suggestion from the operating
// agent, writes an alias and so comes back as one.
//
// A name that the catalog recognises as a retired model, or does not know, is not mapped. A suffix such as :free is not
// recognised, so it is mapped only by an alias, which says what it stands for.
func MapUpstream(snap Catalog, alias *catalog.ModelID, name string) Mapping {
	return MapUpstreamDrafts(snap, alias, name, false)
}

// MapUpstreamDrafts is MapUpstream that, with drafts, also maps a name of a model the catalog has as a draft: the intake
// rule `known` takes such a name, and its endpoint waits until the model is listed, because routing serves only listed
// models. A retired model's name is not mapped either way.
func MapUpstreamDrafts(snap Catalog, alias *catalog.ModelID, name string, drafts bool) Mapping {
	var m Mapping
	switch {
	case alias != nil:
		m.Model = *alias
	default:
		rec, ok := snap.Recognize(name)
		if !ok {
			return Mapping{Why: Unmapped}
		}
		m.Model, m.Unverified = rec.Model, !rec.Exact
	}
	model, ok := snap.Model(m.Model)
	switch {
	case !ok:
		return Mapping{Why: Unmapped}
	case model.Status != catalog.StatusListed && (!drafts || model.Status != catalog.StatusDraft):
		return Mapping{Why: NotListed}
	}
	m.Mapped = true
	return m
}
