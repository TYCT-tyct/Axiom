package gateway

import (
	"uuid"

	"axiom/internal/routing"
)

// The model lists as a key's routing preset leaves them (docs/domains/routing.md「请求覆盖与组合」): the published list
// is narrowed per request by the key's view of the snapshot published with it.

// keyView is the last list and the key's view of the routing snapshot published with it, read under the lock Publish
// holds, so the endpoints the list was built from are the ones the view knows.
func (m *ModelList) keyView(router *routing.Router, a routing.Admission) (*openRouterListing, routing.KeyView, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys, err := router.KeyView(a)
	return m.lastLocked(), keys, err
}

// entry is model i as this key sees it, or nil when the list leaves it out for the key: the build served it, the key may
// call it, its audience has a price for it, and its preset leaves it an endpoint (narrow).
func (l *openRouterListing) entry(v visibility, i int) *orModel {
	e := l.models[i].forAudience(v.audience)
	if e == nil || !l.shows(v, e.model) {
		return nil
	}
	return l.narrow(v, e)
}

// narrow is e as the key's preset leaves it (docs/domains/routing.md「请求覆盖与组合」): nil when the preset passes none
// of the model's endpoints, e itself when it passes all or the endpoints share one price, and otherwise an entry encoded
// now and priced at the cheapest endpoint the preset passes, which is the cheapest the key can be routed to. Only that
// last case allocates, so a key whose preset narrows nothing costs the list no more than before.
func (l *openRouterListing) narrow(v visibility, e *orModel) *orModel {
	if v.keys == nil {
		return e
	}
	kept, narrowed := v.kept(e)
	switch {
	case !narrowed:
		return e
	case len(kept) == 0:
		return nil
	case e.sold.Endpoints == nil:
		return e
	}
	m, _ := l.snap.Model(e.model)
	price, ok := e.sold.ModelPrice(kept)
	if !ok {
		return nil
	}
	// The build encoded this model's object from a price of the same shape, so encoding cannot fail here; a price that
	// cannot fill the object leaves the model out, as the build would.
	out, ok, err := newORModel(e.ID, m, e.serving, price)
	if err != nil || !ok {
		return nil
	}
	return &out
}

// kept is the endpoints of e the key's preset passes, and whether that leaves any out. It allocates only when one is
// left out: a preset that passes all of them returns e's own list.
func (v visibility) kept(e *orModel) (kept []uuid.UUID, narrowed bool) {
	for i, id := range e.served {
		switch allowed := v.keys.Allows(e.model, routing.EndpointID(id)); {
		case allowed && narrowed:
			kept = append(kept, id)
		case !allowed && !narrowed:
			narrowed, kept = true, append(make([]uuid.UUID, 0, len(e.served)-1), e.served[:i]...)
		}
	}
	if !narrowed {
		return e.served, false
	}
	return kept, true
}
