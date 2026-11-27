package suppliers

import (
	"fmt"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/suppliers/internal/store"
)

// Keys that are wallets (docs/domains/suppliers-direct.md「模型重读与余额」): a key of a type that reads balances pays
// from its own balance, so it is that balance, not the supplier's, that says what it can pay. Its funds are in the
// plan's terms, balance × line ratio, since the plan is the type's price × the ratio and the provider charges its own
// price: a request whose cost bound at the plan is b costs the key b ÷ ratio, which its balance pays exactly when
// balance × ratio ≥ b.

// walletFunds is the funds of the row's key: its balance times the line's ratio, rounded down to an exact amount so
// that a bound it covers is one the balance pays; zero for a key whose balance was never read.
func walletFunds(r store.ListRoutableRow) (*platform.Exact, error) {
	if r.KeyBalance == "" {
		return new(platform.Exact{}), nil
	}
	balance, ok1 := new(big.Rat).SetString(r.KeyBalance)
	ratio, ok2 := new(big.Rat).SetString(r.LineRatio)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("credential %s: its balance %q or its line's ratio %q is not a number", r.CredentialID, r.KeyBalance, r.LineRatio)
	}
	funds, err := floorExact(balance.Mul(balance, ratio))
	if err != nil {
		return nil, fmt.Errorf("credential %s: its funds: %w", r.CredentialID, err)
	}
	return &funds, nil
}

// recover ends the rest a refusal for want of funds put the key in once a balance read after it shows some.
func (c *credential) recover(observed *time.Time) {
	broke := c.rt.broke.Load()
	if broke != 0 && observed != nil && observed.UnixNano() > broke && c.funds.Cmp(platform.Exact{}) > 0 {
		c.rt.broke.CompareAndSwap(broke, 0)
	}
}

// fundWallets gives each endpoint of a supplier whose keys are wallets the funds of the richest of them: routing
// filters by what one key can pay, and the key is chosen by the same test (AcquireWithin).
func (s *Service) fundWallets(next *snapshot) {
	richest := map[uuid.UUID]platform.Exact{}
	for _, ep := range next.endpoints {
		for _, c := range ep.creds {
			if most, seen := richest[ep.supplier]; ep.wallets && (!seen || c.funds.Cmp(most) > 0) {
				richest[ep.supplier] = *c.funds
			}
		}
	}
	for _, ep := range next.endpoints {
		if ep.wallets {
			ep.funds = s.fundsOf(ep.supplier, richest[ep.supplier], nil, false)
		}
	}
}

// floorExact is r rounded down to a whole number of the 10⁻¹⁵ units an exact amount counts.
func floorExact(r *big.Rat) (platform.Exact, error) {
	units := new(big.Rat).Mul(r, big.NewRat(1e15, 1))
	whole := new(big.Int).Div(units.Num(), units.Denom()) // Euclidean, so down for a negative balance too
	return platform.ParseExact(new(big.Rat).SetFrac(whole, big.NewInt(1e15)).FloatString(15))
}

// exactText is an exact amount as the numeric columns take it.
func exactText(e platform.Exact) string { return string(e.AppendDecimal(nil)) }
