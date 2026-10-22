package payments

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"testing"

	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

func (w *world) endOrder(o Order, state State) {
	w.t.Helper()
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		locked, err := lockForChange(w.t.Context(), tx, o.ID)
		if err != nil {
			return err
		}
		return move(w.t.Context(), tx, locked, state, event{source: "job", kind: "expire"})
	}); err != nil {
		w.t.Fatal(err)
	}
}

func TestLatePaymentsCreditTerminalOrdersWithOriginalIdentity(t *testing.T) {
	for _, state := range []State{Expired, Cancelled, Failed} {
		for _, closeSupported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/close=%v", state, closeSupported), func(t *testing.T) {
				var o Order
				var caps Caps
				if closeSupported {
					caps = CapClose
				}
				w := newTestWorld(t, fakePlugin{caps: caps, create: goodCheckout, verify: signedNotice,
					close: func(context.Context, OrderID, string) error { return errors.New("fake: close failed") },
					query: func(context.Context, string, string) (Payment, error) { return receipt(o).payment, nil }})
				m := w.waiting()
				o = w.mustPlace(yuan, "late")
				if state == Cancelled {
					if _, err := w.p.Cancel(t.Context(), w.accountA(), o.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					w.endOrder(o, state)
				}
				w.exec("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '1 minute', next_query_at = transaction_timestamp() - interval '1 minute'")
				if w.used(m.ID) != 0 {
					t.Fatal("terminal order reserved usage")
				}
				in := w.input(w.siteA)
				in.ID, in.Rev = o.MerchantID, m.Rev
				in.Secrets = map[string]secret.Secret{"key": secret.New("replacement")}
				if _, err := w.save(siteScope(w.siteA), in); !isField(err, "/secrets") {
					t.Fatalf("identity changed before late payment: %v", err)
				}
				if closeSupported {
					if err := w.p.queryPending(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else if r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o)); r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
				w.assertCredit(o, o.Credits)
				if w.used(m.ID) != o.Total.Minor {
					t.Fatal("late payment did not restore creation-day usage")
				}
			})
		}
	}
}

func TestMerchantIdentityWindowAndNonIdentityChanges(t *testing.T) {
	for _, tc := range []struct {
		state State
		trade bool
		age   string
		hold  bool
	}{
		{Expired, true, "59 seconds", true}, {Cancelled, true, "59 seconds", true},
		{Failed, true, "59 seconds", true}, {Failed, false, "59 seconds", false},
		{Expired, true, "61 seconds", false}, {Cancelled, true, "61 seconds", false}, {Failed, true, "61 seconds", false},
	} {
		t.Run(fmt.Sprintf("%s/%v/%s", tc.state, tc.trade, tc.age), func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{create: goodCheckout})
			m := w.waiting()
			o := w.mustPlace(yuan, "identity-window")
			w.endOrder(o, tc.state)
			w.limits("post_expiry_query_seconds = 60")
			w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '%s'", tc.age))
			if !tc.trade {
				w.exec("UPDATE payments.orders SET trade_no = ''")
			}
			for _, field := range []string{"currency", "gateway", "key", "name"} {
				in := w.input(w.siteA)
				in.ID, in.Rev, in.Secrets = o.MerchantID, m.Rev, nil
				switch field {
				case "currency":
					in.Currency = currency(t, "USD")
				case "gateway":
					in.Settings = []byte(`{"gateway":"https://other.example.com","methods":["alipay"]}`)
				case "key":
					in.Secrets = map[string]secret.Secret{"key": secret.New("new-key")}
				case "name":
					in.Name = "Renamed"
				}
				// Roll back allowed saves so every field is checked against the same original revision.
				rollback := errors.New("test: rollback allowed save")
				err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
					_, err := w.p.SaveMerchant(t.Context(), tx, admin, in)
					if err != nil {
						return err
					}
					return rollback
				})
				wantHold := tc.hold && field != "name"
				if wantHold && errors.Is(err, rollback) || !wantHold && !errors.Is(err, rollback) {
					t.Fatalf("%s hold=%v: %v", field, wantHold, err)
				}
			}
		})
	}
}

func TestIdentityChangesRacingCancellationOrExpiry(t *testing.T) {
	for _, end := range []State{Cancelled, Expired} {
		t.Run(string(end), func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{create: goodCheckout})
			m := w.waiting()
			o := w.mustPlace(yuan, "identity-race")
			in := w.input(w.siteA)
			in.ID, in.Rev = o.MerchantID, m.Rev
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() {
				<-start
				_, err := w.save(siteScope(w.siteA), in)
				if !isField(err, "/secrets") {
					done <- fmt.Errorf("identity race allowed: %w", err)
					return
				}
				done <- nil
			}()
			go func() {
				<-start
				done <- w.tx(w.p.platform, func(tx pg.Tx) error {
					locked, err := lockForChange(t.Context(), tx, o.ID)
					if err != nil {
						return err
					}
					return move(t.Context(), tx, locked, end, event{source: "job", kind: "expire"})
				})
			}()
			close(start)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLateCrossCurrencyUsesArrivalRateWithoutChangingQuote(t *testing.T) {
	for _, age := range []string{"1 minute", "6 minutes", "8 days"} {
		t.Run(age, func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{create: goodCheckout})
			w.open(w.siteA, RoundRobin)
			w.setRate("CNY", "USD", "1", "7")
			in := w.input(w.siteA)
			in.Currency = currency(t, "USD")
			w.mustSave(siteScope(w.siteA), in)
			o := w.mustPlace(10*yuan, "arrival")
			w.endOrder(o, Expired)
			w.exec(fmt.Sprintf("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '%s'", age))
			if err := w.tx(w.p.platform, func(tx pg.Tx) error {
				_, err := billing.SetExchangeRate(t.Context(), tx, admin, billing.ExchangeInput{Base: "CNY", Quote: "USD",
					Numerator: "1", Denominator: "6", ExpectedVersion: 1, Kind: "correction", Reason: "arrival rate"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			w.receive(receipt(o))
			want := o.Credits
			if age == "6 minutes" {
				var err error
				want, _, err = creditAt(o.Total, Fee{PPM: 30_000}, big.NewRat(17, 100)) // (1/6) × 1.02
				if err != nil {
					t.Fatal(err)
				}
			}
			if age == "8 days" {
				want = 0
			}
			w.assertCredit(o, want)
			var locked, arrival string
			if err := w.tx(w.p.platform, func(tx pg.Tx) error {
				return tx.QueryRow(t.Context(), "SELECT applied_num::text || '/' || applied_den::text, coalesce(arrival_num::text || '/' || arrival_den::text, '') FROM payments.orders WHERE id = $1", uuid.UUID(o.ID)).Scan(&locked, &arrival)
			}); err != nil {
				t.Fatal(err)
			}
			if locked != "51/350" || (arrival != "") != (age == "6 minutes") {
				t.Fatalf("locked=%s arrival=%s", locked, arrival)
			}
		})
	}
}

func TestVerifyThrottleScopeAndQueryWindow(t *testing.T) {
	var calls atomic.Int64
	w := newTestWorld(t, fakePlugin{create: goodCheckout, query: func(context.Context, string, string) (Payment, error) {
		calls.Add(1)
		return Payment{State: PayPending}, nil
	}})
	w.waiting()
	o := w.mustPlace(yuan, "verify")
	if _, err := w.p.Verify(t.Context(), w.accountB(), o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("verified another account", err)
	}
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); !errors.Is(err, ErrVerifyLimited) {
		t.Fatal("verify was not limited", err)
	}
	w.endOrder(o, Expired)
	w.exec("UPDATE payments.orders SET expires_at = transaction_timestamp() - interval '8 days', next_verify_at = transaction_timestamp() - interval '1 minute', next_query_at = transaction_timestamp() - interval '1 minute'")
	if _, err := w.p.Verify(t.Context(), w.accountA(), o.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.p.queryPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("queried an order after its identity window")
	}
}

func TestPaymentReplayProperty(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	w.waiting()
	rng := rand.New(rand.NewPCG(309, 390))
	for i := range 12 {
		o := w.mustPlace(yuan+int64(rng.IntN(1000)), fmt.Sprintf("replay-%d", i))
		if i%2 == 0 {
			w.endOrder(o, Expired)
		}
		for range 8 {
			c := receipt(o)
			if rng.IntN(3) == 0 {
				c.payment.State = PayClosed
			}
			w.receive(c)
		}
		w.receive(receipt(o))
		w.assertCredit(o, o.Credits)
	}
	if w.count(w.p.platform, "SELECT count(*) FROM payments.order_payments") != 12 {
		t.Fatal("replays created payments")
	}
}

func TestCreditAtNeverOvercredits(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 3))
	for range 1000 {
		paid := Money{currency(t, "USD"), rng.Int64N(100000) + 1}
		fee := Fee{PPM: int32(rng.IntN(500001)), Fixed: rng.Int64N(paid.Minor)}
		rate := big.NewRat(rng.Int64N(1000)+1, rng.Int64N(1000)+1)
		credits, chargedFee, err := creditAt(paid, fee, rate)
		if err != nil {
			if !errors.Is(err, ErrOutOfRange) {
				t.Fatal(err)
			}
			continue
		}
		q, err := quote(credits, rate, paid.Currency, fee)
		if err != nil || q.Total.Minor > paid.Minor || chargedFee < 0 || chargedFee >= paid.Minor {
			t.Fatalf("inverse overcredits: %d fee %d total %d paid %d: %v", credits, chargedFee, q.Total.Minor, paid.Minor, err)
		}
	}
}
