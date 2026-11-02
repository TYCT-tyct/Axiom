package billing

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestBillingPreferencePicksTheFundingSource pins the four orders and their fallbacks: a request is funded by one
// source, and a preference that allows the other source falls back to it only when the first is too small.
func TestBillingPreferencePicksTheFundingSource(t *testing.T) {
	t.Parallel()
	cases := []fundingCase{
		{"subscription first takes the plan", accounts.BillingSubscriptionFirst, 300, 200, 100, "plan", 0},
		{"subscription first falls back to the wallet", accounts.BillingSubscriptionFirst, 300, 400, 350, "wallet", 0},
		{"wallet first takes the wallet", accounts.BillingWalletFirst, 300, 200, 100, "wallet", 0},
		{"wallet first falls back to the plan", accounts.BillingWalletFirst, 300, 0, 100, "plan", 0},
		{"wallet first refuses when both are short", accounts.BillingWalletFirst, 300, 0, 500, "refused", SourceSubscription},
		{"subscription only refuses without a plan", accounts.BillingSubscriptionOnly, 0, 200, 100, "refused", SourceSubscription},
		{"subscription only refuses a short window", accounts.BillingSubscriptionOnly, 300, 200, 350, "refused", SourceSubscription},
		{"subscription only never uses the wallet", accounts.BillingSubscriptionOnly, 300, 400, 100, "plan", 0},
		{"wallet only refuses a short wallet", accounts.BillingWalletOnly, 300, 200, 350, "refused", SourceBalance},
		{"wallet only never uses the plan", accounts.BillingWalletOnly, 300, 400, 100, "wallet", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkFundingCase(t, tc)
		})
	}
}

type fundingCase struct {
	name       string
	preference string
	plan       int64 // 0 for no plan
	wallet     int64
	amount     int64
	want       string // "plan", "wallet" or "refused"
	source     Source
}

func checkFundingCase(t *testing.T, tc fundingCase) {
	t.Helper()
	b := newBank(t, tc.wallet, 0, Config{})
	b.admitted.Preference = tc.preference
	if tc.plan > 0 {
		b.subscribe(b.makePlan(tc.plan, 3600), uuid.NewV7())
	}
	h, err := b.tryHoldOn(b.exact, tc.amount)
	if tc.want == "refused" {
		var short *Insufficient
		if !errors.As(err, &short) || short.Source != tc.source {
			t.Fatalf("hold error = %v, want an insufficient %v", err, tc.source)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if h.funding != tc.want {
		t.Errorf("funding = %q, want %q", h.funding, tc.want)
	}
	if tc.want == "plan" && h.windowSeq == 0 {
		t.Error("a plan hold has no window")
	}
	if tc.want == "wallet" && h.windowSeq != 0 {
		t.Errorf("a wallet hold froze window %d", h.windowSeq)
	}
}

// TestBillingPreferenceBatchKeepsTheOrder checks that a batch with a wallet-first preference does not spend the plan
// on a request the wallet covers and does spend it after the wallet runs out, and that the batch's counters see both.
func TestBillingPreferenceBatchKeepsTheOrder(t *testing.T) {
	t.Parallel()
	b := newBank(t, 100, 0, Config{})
	b.admitted.Preference = accounts.BillingWalletFirst
	b.subscribe(b.makePlan(300, 3600), uuid.NewV7())
	reqs := []ReserveRequest{
		{Admitted: b.admitted, ID: uuid.NewV7(), Demand: Demand{TextBytes: 80}, Quotables: []Quotable{b.exact}},
		{Admitted: b.admitted, ID: uuid.NewV7(), Demand: Demand{TextBytes: 80}, Quotables: []Quotable{b.exact}},
	}
	var holds []*Hold
	if err := b.app.Tx(context.Background(), b.admitted.Scope, func(tx pg.Tx) error {
		var errs []error
		var err error
		holds, _, errs, err = b.billing.ReserveMany(context.Background(), tx, reqs)
		if err != nil {
			return err
		}
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("batch errors = %v", errs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if holds[0].funding != "wallet" || holds[0].windowSeq != 0 {
		t.Errorf("first hold = %+v, want the wallet", holds[0])
	}
	if holds[1].funding != "plan" || holds[1].windowSeq == 0 {
		t.Errorf("second hold = %+v, want the plan after the wallet ran out", holds[1])
	}
}
