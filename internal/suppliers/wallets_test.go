package suppliers

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// TestEachKeyPaysFromItsOwnWallet: a key of a type that reads balances pays from its own (docs/domains/
// suppliers-direct.md「模型重读与余额」). Its balance is read when it is connected; the supplier's funds are the richest
// key's; and AcquireWithin picks only a key whose balance pays the request's bound at the type's price, the bound ÷ the
// line's ratio, however busy it is, and is ErrNoSlot when none can. A key the provider refuses for want of funds rests
// until a read of its balance shows some; a balance told in another currency is stored at the platform's rate.
func TestEachKeyPaysFromItsOwnWallet(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	w.provider.set("sk-good-wallet-rich", providerKey{models: []string{"stone"}, balance: "10", currency: "USD"})
	w.provider.set("sk-good-wallet-poor", providerKey{models: []string{"stone"}, balance: "0.001", currency: "USD"})
	line := w.connect("wallet", "0.5", "sk-good-wallet-rich", "sk-good-wallet-poor")
	endpoint := w.endpoint(line, "stone")
	rich, poor := w.keyOf(line, "rich"), w.keyOf(line, "poor")
	if got := w.balanceOf(rich); got != "10" {
		t.Errorf("the rich key's balance read at connect is %q, want 10", got)
	}
	w.expectFunds("5")
	for _, tc := range []struct {
		name, bound string
		want        uuid.UUID
		err         error
	}{
		{"a bound the rich key's wallet pays", "1", rich, nil},
		{"the most it pays: 10 at the type's price", "5", rich, nil},
		{"more than either pays", "5.000000000000001", uuid.UUID{}, ErrNoSlot},
	} {
		w.expectPicks(tc.name, endpoint, tc.bound, tc.want, tc.err)
	}

	w.provider.set("sk-good-wallet-rich", providerKey{models: []string{"stone"}, balance: "0", currency: "USD", broke: true})
	w.refused(endpoint)
	w.expectPicks("the refused key before a read", endpoint, "1", uuid.UUID{}, ErrNoSlot)
	w.provider.set("sk-good-wallet-rich", providerKey{models: []string{"stone"}, balance: "12", currency: "USD"})
	w.provider.set("sk-good-wallet-poor", providerKey{models: []string{"stone"}, balance: "7", currency: "CNY"})
	w.clock.advance(time.Minute)
	yuan := platform.ExchangeRate{ID: uuid.NewV7(), Version: 1, Base: "CNY", Quote: "USD", Numerator: 1, Denominator: 7}
	jobs := &lines{
		db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default(),
		rates: func(context.Context, pg.Tx) ([]platform.ExchangeRate, error) {
			return []platform.ExchangeRate{yuan}, nil
		},
	}
	if err := jobs.relist(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.expectPicks("the refused key after a read that shows funds", endpoint, "1", rich, nil)
	if got, want := w.balanceOf(rich)+" "+w.balanceOf(poor), "12 1"; got != want {
		t.Errorf("the balances read again are %s, want %s: 7 yuan at 7 a dollar", got, want)
	}
}

// expectFunds checks that the one endpoint's supplier can pay up to most and no more.
func (w *pastedWorld) expectFunds(most string) {
	w.t.Helper()
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	routable, err := w.svc.Routable()
	if err != nil || len(routable) != 1 {
		w.t.Fatalf("%d routable endpoints (%v)", len(routable), err)
	}
	above, err := exact(w.t, most).Add(exact(w.t, "0.000000000000001"))
	if err != nil {
		w.t.Fatal(err)
	}
	if f := routable[0].Funds; f.Unbounded || !f.CanPay(exact(w.t, most)) || f.CanPay(above) {
		w.t.Errorf("the funds %+v do not pay exactly up to %s", f, most)
	}
}

// expectPicks reloads and takes three attempts within the bound, each held until the last, and checks that every one
// went to want, or that the first was refused with wantErr: after the first, want is the busier key.
func (w *pastedWorld) expectPicks(what string, endpoint uuid.UUID, bound string, want uuid.UUID, wantErr error) {
	w.t.Helper()
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	var held []*Attempt
	defer func() {
		for _, a := range held {
			a.Done()
		}
	}()
	for range 3 {
		a, err := w.svc.AcquireWithin(endpoint, exact(w.t, bound))
		if !errors.Is(err, wantErr) {
			w.t.Fatalf("%s: %v, want %v", what, err, wantErr)
		}
		if err != nil {
			return
		}
		held = append(held, a)
		if got := a.receipt().CredentialID(); got != want {
			w.t.Errorf("%s: picked %s, want %s", what, got, want)
		}
	}
}

// refused makes a call that the provider refuses for want of funds.
func (w *pastedWorld) refused(endpoint uuid.UUID) {
	w.t.Helper()
	a, err := w.svc.AcquireWithin(endpoint, exact(w.t, "1"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer a.Done()
	ctx, cancel := context.WithTimeout(w.t.Context(), 10*time.Second) // a call has a deadline
	defer cancel()
	resp, f := a.Send(ctx, Call{Format: protocol.Chat, Body: []byte(`{"model":"stone"}`)})
	if resp != nil {
		w.t.Fatal(resp.Body.Close(), "a call the provider refused for want of funds succeeded")
	}
	if f.Cause != (CredentialQuotaExhausted{}) {
		w.t.Fatalf("a call the provider refused for want of funds failed with %+v", f)
	}
}

// keyOf is the supplier's key whose last four characters are last4.
func (w *pastedWorld) keyOf(supplier uuid.UUID, last4 string) uuid.UUID {
	w.t.Helper()
	return uuid.MustParse(w.text("SELECT id::text FROM suppliers.credentials WHERE supplier_id = $1 AND hint = $2", supplier, last4))
}

// balanceOf is the key's balance as stored, empty for none.
func (w *pastedWorld) balanceOf(credential uuid.UUID) string {
	w.t.Helper()
	return w.text("SELECT coalesce(trim_scale(balance_native)::text, '') FROM suppliers.credentials WHERE id = $1", credential)
}

// exact is the amount the text says.
func exact(t *testing.T, s string) platform.Exact {
	t.Helper()
	e, err := platform.ParseExact(s)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
