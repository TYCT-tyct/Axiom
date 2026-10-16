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
	// never reaches routing (EndpointFacts.Mapped); the name waits in the queue of names to map.
	Mapped bool
	// Unverified is set when the name is only a candidate: the catalog says it is not evidence of the version, because
	// the vendor may have moved it to a newer one. The endpoint is made but must pass the authenticity check before the
	// mapping is confirmed (docs/domains/suppliers.md「供应商别名与待映射」).
	Unverified bool
}

// MapUpstream maps an upstream name by the first two steps of docs/domains/suppliers.md「供应商别名与待映射」: the
// supplier's own alias for the name, which the caller has looked up in its tables (nil if it has none), and then the
// catalog's internal recognition, public and internal names alike. The third step, a suggestion from the operating
// agent, writes an alias and so comes back as one.
//
// A name that the catalog recognises as a retired model, or does not know, is not mapped. A suffix such as :free is not
// recognised, so it is mapped only by an alias, which says what it stands for.
func MapUpstream(snap Catalog, alias *catalog.ModelID, name string) Mapping {
	var m Mapping
	switch {
	case alias != nil:
		m.Model = *alias
	default:
		rec, ok := snap.Recognize(name)
		if !ok {
			return Mapping{}
		}
		m.Model, m.Unverified = rec.Model, !rec.Exact
	}
	if model, ok := snap.Model(m.Model); !ok || model.Status != catalog.StatusListed {
		return Mapping{}
	}
	m.Mapped = true
	return m
}
