package suppliers

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

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
