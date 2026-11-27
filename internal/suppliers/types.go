package suppliers

import (
	"cmp"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
)

// Channel types (docs/domains/suppliers-direct.md「渠道类型」): what the registry's adapters describe, with what the
// catalog can sell through each.

type (
	// Type is a channel type as its adapter describes it.
	Type = adapter.Type
	// TypeField is one field of a type.
	TypeField = adapter.Field
	// PriceEntry is one price list a type is connected at.
	PriceEntry = adapter.PriceEntry
	// Settings is what is not secret about one credential.
	Settings = adapter.Settings
)

// TypeView is a type with, for each of its price entries in order, how many listed catalog models of its makers have a
// reference price in the entry's currency; nil for a type that sells any maker's models, which the catalog cannot
// count for it.
type TypeView struct {
	Type
	Models []int
}

// FieldKind is the kind of a field of a type.
func FieldKind(f TypeField) string {
	k, _ := adapter.FieldKindOf(f.Name)
	return string(k)
}

// groupOrder is the order the picker lists the groups in.
var groupOrder = []adapter.TypeGroup{adapter.GroupOfficial, adapter.GroupDomestic, adapter.GroupCloud, adapter.GroupRelay}

// Types is every type the registry carries, by group and then name, with the models snap can sell through each.
func (a *Admin) Types(snap *catalog.Snapshot) []TypeView {
	types := a.registry.Types()
	slices.SortFunc(types, func(x, y adapter.Type) int {
		return cmp.Or(cmp.Compare(slices.Index(groupOrder, x.Group), slices.Index(groupOrder, y.Group)), cmp.Compare(x.Name, y.Name))
	})
	out := make([]TypeView, len(types))
	for i, t := range types {
		out[i] = TypeView{Type: t}
		if len(t.Serves) == 0 {
			continue
		}
		out[i].Models = make([]int, len(t.Entries))
		for m := range snap.Models() {
			for j, e := range t.Entries {
				if _, priced := (lineOffer{serves: t.ServesAt(e.Origin, e.Currency)}).price(snap, m, e.Currency); priced {
					out[i].Models[j]++
				}
			}
		}
	}
	return out
}

// TypeOf is the type of kind, false for a kind the registry does not carry.
func (a *Admin) TypeOf(kind string) (Type, bool) {
	ad, ok := a.registry.Get(adapter.Kind(kind))
	if !ok {
		return Type{}, false
	}
	return registry.TypeOf(ad), true
}
