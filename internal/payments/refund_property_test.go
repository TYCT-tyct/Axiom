package payments

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func TestRefundFXSequenceAndSplitProperty(t *testing.T) {
	for _, withFee := range []bool{false, true} {
		t.Run(fmt.Sprint(withFee), func(t *testing.T) {
			testRefundFXSequence(t, withFee)
		})
	}
}

func testRefundFXSequence(t *testing.T, withFee bool) {
	t.Helper()
	sent := make(map[RefundID]Money)

	w := newTestWorld(t, fakePlugin{
		create: goodCheckout, caps: CapRefund | CapRefundKey,
		refund: func(_ context.Context, r RefundCall) (RefundState, error) {
			sent[r.Refund] = r.Amount
			return RefundState{State: RefundSucceeded, ProviderRef: r.Refund.TypeID()}, nil
		},
	})
	w.open(w.siteA, RoundRobin)
	in := w.input(w.siteA)
	in.Currency = currency(t, "USD")
	in.RefundEnabled, in.UserRefund = true, true
	merchant := w.mustSave(siteScope(w.siteA), in)
	w.limits(fmt.Sprintf("refund_with_fee=%t,ip_order_limit=1000", withFee))
	rng := rand.New(rand.NewPCG(309, 391)) //nolint:gosec // deterministic monetary property inputs
	version := int64(0)
	changeRate := func() {
		t.Helper()
		kind := "correction"
		if version == 0 {
			kind = "initial"
		}
		err := w.tx(w.p.platform, func(tx pg.Tx) error {
			_, err := billing.SetExchangeRate(t.Context(), tx, admin, billing.ExchangeInput{Base: "CNY", Quote: "USD", Numerator: fmt.Sprint(1 + rng.IntN(500)), Denominator: "100", ExpectedVersion: version, Kind: kind, Reason: "FX sequence property"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		version++
	}
	for i := range 24 {
		testFXRefundOrder(w, rng, withFee, i, changeRate, sent)
	}

	if n := w.count(w.p.platform, "SELECT count(*) FROM (SELECT transaction_id,currency FROM billing.entries GROUP BY transaction_id,currency HAVING sum(amount)<>0) x"); n != 0 {
		t.Fatal("unbalanced ledger", n)
	}
	var differences []RecordDiscrepancyParams
	err := w.tx(w.p.platform, func(tx pg.Tx) (err error) {
		now, err := queries.RefundClock(t.Context(), tx)
		if err != nil {
			return err
		}
		differences, err = checkMerchant(t.Context(), tx, merchant.ID, now.UTC().Truncate(24*time.Hour))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(differences) != 0 {
		t.Fatalf("refunded cash does not reconcile: %+v", differences)
	}
}

func testFXRefundOrder(w *world, rng *rand.Rand, withFee bool, i int, changeRate func(), sent map[RefundID]Money) {
	w.t.Helper()
	t := w.t
	changeRate()
	o := w.mustPlace(10*yuan+int64(rng.IntN(10))*yuan, fmt.Sprintf("fx-%d", i))
	// Rotate through locked, grace-period, and arrival-rate crediting.
	if i%3 != 0 {
		w.endOrder(o, Expired)
		age := "1 minute"
		if i%3 == 2 {
			age = "6 minutes"
		}
		w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at=clock_timestamp()-interval '%s' WHERE id='%s'", age, uuid.UUID(o.ID)))
	}
	changeRate()
	w.receive(receipt(o))
	credited := int64(w.count(w.p.platform, "SELECT credited FROM payments.orders WHERE id=$1", uuid.UUID(o.ID)))
	net := o.Total.Minor
	if !withFee {
		net = int64(w.count(w.p.platform, "SELECT paid_total-paid_fee FROM payments.orders WHERE id=$1", uuid.UUID(o.ID)))
	}
	splitRefundProperty(w, o, rng, withFee, credited, net, changeRate, sent)

	state, _, _ := w.stored(o.ID)
	if state != "refunded" {
		t.Fatal(state)
	}
	w.limits(fmt.Sprintf("refund_with_fee=%t", withFee))
}

func splitRefundProperty(w *world, o Order, rng *rand.Rand, withFee bool, credited, net int64, changeRate func(), sent map[RefundID]Money) {
	w.t.Helper()
	t := w.t
	remaining := credited
	var cash int64
	for part, left := 0, 2+rng.IntN(6); left > 0 && remaining > 0; part, left = part+1, left-1 {
		changeRate()
		amount := 1 + rng.Int64N(remaining)
		if left == 1 {
			amount = remaining
		}
		r, err := w.p.Refund(t.Context(), w.accountA(), refundRequest(w, o, amount, fmt.Sprintf("split-%d", part)))
		if errors.Is(err, ErrRefundAmount) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		r = w.approve(r)
		if r.State != "succeeded" {
			t.Fatal(r)
		}
		actual := sent[r.ID]
		assertSentRefund(w, r, actual)
		cash += actual.Minor
		remaining -= r.Credits
		if cash > o.Total.Minor || cash > net {
			t.Fatalf("refunded %d > net %d paid %d", cash, net, o.Total.Minor)
		}
		// A later policy change must not change the first successful refund's policy.
		w.limits(fmt.Sprintf("refund_with_fee=%t", !withFee))
	}
	if remaining != 0 || cash != net {
		t.Fatalf("full split leaves credits=%d cash=%d want=%d", remaining, cash, net)
	}
}

func assertSentRefund(w *world, r Refund, actual Money) {
	w.t.Helper()
	if actual != r.Amount {
		w.t.Fatalf("channel cash %+v differs from quoted %+v", actual, r.Amount)
	}
}
