package syncer

import (
	"slices"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// groupOf is an offer of one group with the given facts and models.
func groupOf(facts string, models ...adapter.Model) func() (adapter.Offer, error) {
	return func() (adapter.Offer, error) {
		return adapter.Offer{Groups: []adapter.Group{{Ref: "1", Name: "g", Ratio: "1", Facts: adapter.GroupFacts(facts), Models: models}}}, nil
	}
}

// TestRoutingRelevantCatalogChangesAreEvents: what decides whether and how an endpoint is served is kept in the snapshot
// and its change is an event; the same answer again is silent. A model's capability limits are kept with it, also
// through a round in which its group could not be read.
func TestRoutingRelevantCatalogChangesAreEvents(t *testing.T) {
	t.Parallel()
	m := healthy()
	m.offer = groupOf(`{"peak":false}`, adapter.Model{UpstreamName: "m", Formats: []protocol.Format{protocol.Chat}, Without: protocol.Images})
	e, st, clk := engine(m)
	round(t, e)
	if got := st.state.Groups[0].Models[0].Without; got != protocol.Images {
		t.Fatalf("the snapshot keeps Without %v, want %v", got, protocol.Images)
	}
	step := func(offer func() (adapter.Offer, error), want ...Kind) {
		t.Helper()
		m.offer = offer
		clk.advance(10 * time.Minute)
		before := len(st.events)
		round(t, e)
		if got := st.kinds()[before:]; !slices.Equal(got, want) {
			t.Errorf("events %v, want %v", got, want)
		}
	}
	chat := adapter.Model{UpstreamName: "m", Formats: []protocol.Format{protocol.Chat}, Without: protocol.Images}
	step(groupOf(`{"peak":false}`, chat))                                                                                          // the same answer
	step(groupOf(`{"peak":true}`, chat), GroupChanged)                                                                             // peak pricing on, ratio unchanged
	step(groupOf(`{"peak":true}`, adapter.Model{UpstreamName: "m", Formats: []protocol.Format{protocol.Chat}}), ModelChanged)      // the limit lifted
	step(groupOf(`{"peak":true}`, adapter.Model{UpstreamName: "m", Formats: []protocol.Format{protocol.Responses}}), ModelChanged) // another format
	m.offer = func() (adapter.Offer, error) {
		return adapter.Offer{Groups: []adapter.Group{{Ref: "1", Name: "g", Ratio: "1", Facts: adapter.GroupFacts(`{"peak":true}`), ModelsUnread: true}}}, nil
	}
	clk.advance(10 * time.Minute)
	round(t, e)
	if g := st.state.Groups[0]; len(g.Models) != 1 || g.Models[0].Formats[0] != int(protocol.Responses) {
		t.Errorf("an unread group lost what it had: %+v", g.Models)
	}
}

// TestBalanceAndFundingChangesAreEvents: a balance that rises over the reserve, or falls under it, and a change of
// where calls are paid from, are routing facts and each makes an event; a repeat makes none.
func TestBalanceAndFundingChangesAreEvents(t *testing.T) {
	t.Parallel()
	m := healthy()
	balance, funding := adapter.Amount("0"), adapter.FundingWallet
	m.account = func() (adapter.Account, error) { return adapter.Account{Balance: balance, Funding: funding}, nil }
	e, st, clk := engine(m)
	step := func(b adapter.Amount, f adapter.Funding, want ...Kind) {
		t.Helper()
		balance, funding = b, f
		clk.advance(2 * time.Minute)
		before := len(st.events)
		round(t, e)
		if got := st.kinds()[before:]; !slices.Equal(got, want) {
			t.Errorf("balance %s funding %v: events %v, want %v", b, f, got, want)
		}
	}
	round(t, e) // the first read: low, nothing crossed
	step("0", adapter.FundingWallet)
	step("100", adapter.FundingWallet, BalanceCrossed, BalanceRecovered)
	step("100", adapter.FundingOther, FundingChanged)
	step("100", adapter.FundingOther)
	step("0.5", adapter.FundingOther, BalanceCrossed, BalanceLow)
}

// rescanning is a supplier that can be told to read again from a time.
type rescanning struct {
	*supplier
	since []time.Time
}

func (r *rescanning) Rescan(since time.Time) adapter.ScanState {
	r.since = append(r.since, since)
	return adapter.ScanState(`{"rescan":true}`)
}

// TestRemovalReadsAgainFromBeforeTheGracePeriod: the second read of a removal starts from the time the removal began less
// the grace period, not from where the adapter would go on, so a record that appears late within the grace period is
// within it.
func TestRemovalReadsAgainFromBeforeTheGracePeriod(t *testing.T) {
	t.Parallel()
	r := &rescanning{supplier: healthy()}
	var seen []string
	e, _, clk := engine(r)
	r.charges = func(scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
		seen = append(seen, string(scan))
		return adapter.ChargePage{Done: true, Through: clk.now()}, scan, nil
	}
	started := clk.now()
	removeAll(t, e, clk, &hooks{})
	if len(r.since) != 1 || !r.since[0].Equal(started.Add(-30*time.Minute)) {
		t.Errorf("rescanned from %v, want %v (the removal began, less the grace period)", r.since, started.Add(-30*time.Minute))
	}
	if !slices.Contains(seen, `{"rescan":true}`) {
		t.Errorf("the rescan position was not the one read from: %v", seen)
	}
}

// TestEveryEventKindHasAName: events are stored and delivered under their names, so a kind without one would be written
// as a panic or as a name that belongs to another.
func TestEveryEventKindHasAName(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for k := GroupAdded; k <= RemovalSlow; k++ {
		name := k.String()
		if name == "" || seen[name] {
			t.Errorf("kind %d is named %q, which is empty or another's", k, name)
		}
		seen[name] = true
	}
	if len(kindNames) != int(RemovalSlow)+1 {
		t.Errorf("%d names for %d kinds", len(kindNames), int(RemovalSlow)+1)
	}
}
