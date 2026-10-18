package syncer

import (
	"axiom/internal/suppliers/internal/adapter"
)

// EndpointFacts are the conditions for putting an endpoint in the routing snapshot. Some come from the supplier's
// state (SupplierFacts fills them), the rest from the tables of groups, models and credentials.
type EndpointFacts struct {
	Normal            bool           // the supplier is polled and not stopped or being removed
	Wallet            bool           // calls are paid from the wallet
	Balance           adapter.Amount // the last balance seen
	Reserve           adapter.Amount // held back from it for calls in flight
	GroupSelected     bool
	Mapped            bool // the model has a name in the catalog
	CostKnown         bool // there is a cost plan
	UsableCredentials int
}

// Reason says which condition keeps an endpoint out, the first that fails; empty when none does.
type Reason string

// The reasons.
const (
	Routable         Reason = ""
	SupplierStopped  Reason = "the supplier is stopped or being removed"
	NotWallet        Reason = "calls are not paid from the wallet"
	NoBalance        Reason = "the balance does not cover the reserve"
	GroupNotSelected Reason = "the group is not selected"
	Unmapped         Reason = "the model is not mapped"
	NotListed        Reason = "the model is not listed in the catalog"
	CostUnknown      Reason = "the cost is not known"
	NoUsableKey      Reason = "no credential can be used"
	// Gone is an endpoint whose model the listing no longer offers: the row is kept for when it comes back and is taken
	// out of routing meanwhile.
	Gone Reason = "the listing no longer offers the model"
	// NoFormat is a name the listing still has in a group nothing can be called from — one sold by subscription, or one
	// whose price changes by the hour: its formats are none, so it is not a model of the supplier at all.
	NoFormat Reason = "no call format is served here"
)

// Check applies the conditions for an endpoint to enter the routing snapshot (internal/suppliers/README.md 「同步」): the supplier is normal, funds are the
// wallet and the balance less the reserve is above zero, the group is selected, the model mapped, the cost known, and
// at least one credential usable. An endpoint that meets all of them is returned as Routable.
func (f EndpointFacts) Check() Reason {
	switch {
	case !f.Normal:
		return SupplierStopped
	case !f.Wallet:
		return NotWallet
	case rat(f.Balance).Cmp(rat(f.Reserve)) <= 0:
		return NoBalance
	case !f.GroupSelected:
		return GroupNotSelected
	case !f.Mapped:
		return Unmapped
	case !f.CostKnown:
		return CostUnknown
	case f.UsableCredentials < 1:
		return NoUsableKey
	}
	return Routable
}

// Facts fills the conditions that the supplier's state decides. The caller adds selection, mapping and credentials.
func (s State) Facts(reserve adapter.Amount, endpoint string) EndpointFacts {
	return EndpointFacts{
		Normal: s.Status == Normal, Wallet: s.Funding == adapter.FundingWallet, Balance: s.Balance, Reserve: reserve,
		CostKnown: s.Plans[endpoint] != "",
	}
}
