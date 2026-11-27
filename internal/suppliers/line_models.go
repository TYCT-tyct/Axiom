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
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// The models a direct or cloud line serves (docs/domains/suppliers-direct.md「接入」): each name its keys or regions
// list, mapped by the catalog, with an endpoint priced at the line's ratio.

// lineOffer is what a connection lists on a line of one: each name, the name the catalog knows it by (known; nil for
// the name itself), the formats an endpoint of a catalog model serves there, what a name the catalog does not map yet
// is listed as speaking, and the catalog authors whose official prices are the line's own prices (serves): a model of
// another author is sold there at a price of the platform's, which the catalog does not hold. A type that serves no
// author by name (compatible, an aggregator) sells every model at a price of its own, so none is priced from the catalog.
type lineOffer struct {
	names    []string
	known    func(name string) string
	formats  func(m catalog.Model, name string) []protocol.Format
	unmapped []protocol.Format
	serves   []string
}

// price is the line's own price of m in the currency: the catalog's reference price, for a model of an author the type
// serves, unless the price came from OpenRouter's dataset. That one is an aggregator's selling price (for an open-weight
// model a third-party provider's, often far below the author's own), and pricing the author's own API from it would
// understate the line's cost; such an endpoint's cost is unknown instead. The author's page, LiteLLM's compilation of
// vendors' list prices and a person's confirmed figure are the author's price.
func (o lineOffer) price(snap *catalog.Snapshot, m catalog.Model, currency string) (catalog.ReferencePrice, bool) {
	ref, ok := snap.ReferencePrice(m.ID, currency)
	return ref, ok && ref.Source != catalog.SourceOpenRouter && slices.Contains(o.serves, m.Author)
}

// offer is what the passed keys list.
func (p *DirectPlan) offer() lineOffer {
	return lineOffer{
		names: p.names(), formats: func(m catalog.Model, name string) []protocol.Format { return p.direct.Formats(p.family, m, name) },
		unmapped: familyFormats(p.family), serves: p.direct.Type().ServesAt(p.origin, p.r.Currency.String()),
	}
}

// insertLineModels writes every name of the offer on the line. A name the catalog maps to a model on sale gets an
// endpoint in the formats the line serves the model in, costed at the model's official price in the settlement
// currency times the line's ratio (cost_schedules basis published: the provider's own published price); a name with no
// such price keeps an endpoint whose cost is unknown, out of routing. A name the catalog does not map waits for a
// person, as a relay's does. Two names of one model are one endpoint: the name that is evidence of the version, else
// the first. A model in served has an endpoint already and keeps it.
func insertLineModels(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, currency platform.Currency, o lineOffer, served map[catalog.ModelID]bool, ratio *big.Rat) (models, priced int, err error) {
	fallback, err := store.FormatNames(o.unmapped)
	if err != nil {
		return 0, 0, err
	}
	endpoints := map[catalog.ModelID]string{}
	exact := map[catalog.ModelID]bool{}
	rows := make(map[string]store.UpsertProjectedModelParams, len(o.names))
	for _, name := range o.names {
		row := store.UpsertProjectedModelParams{SupplierID: id, SupplierGroupID: group, UpstreamName: name, Formats: fallback}
		m, serves := mapLineName(snap, o, name, &row)
		if _, taken := endpoints[m.Model]; serves && !served[m.Model] && (!taken || !m.Unverified && !exact[m.Model]) {
			endpoints[m.Model], exact[m.Model] = name, !m.Unverified
		}
		rows[name] = row
		if err := q.UpsertProjectedModel(ctx, tx, row); err != nil {
			return 0, 0, fmt.Errorf("list %s: %w", name, err)
		}
	}
	for _, model := range slices.SortedFunc(maps.Keys(endpoints), func(a, b catalog.ModelID) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }) {
		name := endpoints[model]
		m, _ := snap.Model(model) // mapLineName mapped the name to a model of the snapshot
		ref, has := o.price(snap, m, currency.String())
		ok, err := insertDirectEndpoint(ctx, tx, q, id, group, model, name, rows[name].Formats, ref, has, ratio)
		if err != nil {
			return 0, 0, err
		}
		if ok {
			priced++
		}
	}
	return len(o.names), priced, nil
}

// mapLineName maps one name of the offer into row: the model on sale it stands for, and the formats the line serves
// that model in. It reports whether an endpoint can serve the name; one the catalog does not map keeps no model.
func mapLineName(snap *catalog.Snapshot, o lineOffer, name string, row *store.UpsertProjectedModelParams) (syncer.Mapping, bool) {
	m := syncer.MapUpstream(snap, nil, o.knownAs(name))
	model, ok := snap.Model(m.Model)
	if !m.Mapped || !ok {
		return m, false
	}
	mapped := uuid.UUID(m.Model)
	row.ModelID = &mapped
	formats, err := store.FormatNames(o.formats(model, name))
	if err != nil || len(formats) == 0 {
		return m, false
	}
	row.Formats = formats
	return m, true
}

// insertDirectEndpoint makes one endpoint and, when the line has its own price of the model (priced, lineOffer.price),
// its cost schedule of ref at the line's ratio; it reports whether it priced it. An endpoint left unpriced has an
// unknown cost and is not routed.
func insertDirectEndpoint(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, model catalog.ModelID, name string, formats []string, ref catalog.ReferencePrice, priced bool, ratio *big.Rat) (bool, error) {
	endpoint, err := q.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
		SupplierID: id, SupplierGroupID: group, ModelID: uuid.UUID(model), UpstreamName: name, Formats: formats,
	})
	if err != nil {
		return false, fmt.Errorf("make the endpoint for %s: %w", name, err)
	}
	if !priced {
		return false, nil
	}
	if err := priceEndpoint(ctx, tx, q, id, endpoint, ref, ratio); err != nil {
		return false, fmt.Errorf("price the endpoint for %s: %w", name, err)
	}
	return true, nil
}

// familyFormats is what a name the catalog cannot map yet is listed as speaking: what every API of the family serves.
func familyFormats(f adapter.Family) []protocol.Format {
	if f == adapter.FamilyAnthropic {
		return []protocol.Format{protocol.Messages}
	}
	return []protocol.Format{protocol.Chat}
}
