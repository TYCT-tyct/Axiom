package billing

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// TestExtendAcrossAttempts: each attempt's meter numbers its segments from its own start, so a later attempt may ask
// for a segment number an earlier one already used. Extend succeeds only once the reservation covers the target, so
// the later attempt's larger target is written, not taken for a retry.
func TestExtendAcrossAttempts(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, Segments: Segments{Output: 100, Overshoot: time.Second}})
	segmented := b.exact
	segmented.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"}}`)
	segmented.MaxOutput, segmented.Segmentable = 1000, true
	var h *Hold
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 10}, []Quotable{segmented})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The extension point is drawn per request between 400 and 600 per mille of the 100-token segment; the latest one
	// is the boundary, and a read must pass it (60 tokens would not, strictly) for every draw to ask.
	h.trigger = 600
	for _, read := range []int64{61, 250} { // attempt A, then attempt B after a fallback, each with a fresh meter
		step := h.Bind(0).Observe(Progress{ContentBytes: read})
		if !step.Extend || step.Request.Seq != 2 {
			t.Fatalf("attempt reading %d: %+v, want a request for segment 2", read, step)
		}
		if err := b.billing.Extend(t.Context(), h, step.Request); err != nil {
			t.Fatal(err)
		}
		if got, want := b.count("SELECT amount FROM billing.reservations"), step.Request.Target.CeilAmount(); got < want {
			t.Errorf("attempt reading %d: %d reserved, below its target %d", read, got, want)
		}
	}
	b.consistent()
}

// TestReserveLimitResets: a limit counts every day its current window covers, whatever window it had when the
// money was spent or reserved, so changing how often it resets never forgets what is already held.
func TestReserveLimitResets(t *testing.T) {
	t.Parallel()

	b := newBank(t, 1e6, 0, Config{})
	a := b.admitted
	a.KeyLimit = &Limit{Amount: 1000, Reset: ResetDaily}
	if _, err := b.reserve(a, 600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Reset{ResetNever, ResetWeekly, ResetMonthly, ResetDaily} {
		a.KeyLimit.Reset = r
		if _, err := b.reserve(a, 600); !errors.As(err, new(*Insufficient)) {
			t.Errorf("%q: Reserve = %v, want the 600 already held counted", r, err)
		}
	}
	a.KeyLimit = nil
	if _, err := b.reserve(a, 600); err != nil {
		t.Errorf("no limit: %v", err)
	}
	b.consistent()
}

// TestReserveBalanceExtremes: the balance's headroom is computed exactly, so stored values at the ends of int64
// neither refuse a reservation they allow nor allow one a negative balance cannot cover.
func TestReserveBalanceExtremes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                     string
		balance, reserved, floor int64
		allowed                  bool
	}{
		{"the largest balance above a floor of -1", math.MaxInt64, 0, -1, true},
		{"a balance near the least with reservations", math.MinInt64 + 50, 100, 0, false},
		{"the least floor", 0, 0, math.MinInt64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			b.sql(fmt.Sprintf("UPDATE billing.balances SET balance = %d, reserved = %d, floor = %d", tc.balance, tc.reserved, tc.floor))
			if _, err := b.reserve(b.admitted, 100); (err == nil) != tc.allowed {
				t.Errorf("Reserve = %v, want allowed %v", err, tc.allowed)
			}
		})
	}
}

// TestReserveNoBalanceRow: an account without a balance row reserves nothing, even at a price of zero, and nothing is
// written.
func TestReserveNoBalanceRow(t *testing.T) {
	t.Parallel()

	b := newBank(t, 0, 0, Config{})
	a := b.admitted
	a.Account = uuid.NewV7()
	a.Scope = pg.NewScope(false, nil, []uuid.UUID{a.Account})
	free := b.exact
	free.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`)
	err := b.app.Tx(t.Context(), a.Scope, func(tx pg.Tx) error {
		_, _, err := b.billing.Reserve(t.Context(), tx, a, uuid.NewV7(), Demand{TextBytes: 10}, []Quotable{free})
		return err
	})
	if s, ok := errors.AsType[*Insufficient](err); !ok || s.Source != SourceBalance {
		t.Errorf("Reserve = %v, want the balance short", err)
	}
	if n := b.count("SELECT (SELECT count(*) FROM billing.reservations) + (SELECT count(*) FROM billing.spend)"); n != 0 {
		t.Errorf("%d rows written", n)
	}
}

// TestExtendAcrossWindows: an extension checks the limit in the window of its reservation's day, the one it books
// to, so a reservation admitted before a UTC day, week or month boundary cannot outgrow its limit by extending after
// it, when the new window is still empty; nor does it count what the new window holds.
func TestExtendAcrossWindows(t *testing.T) {
	t.Parallel()

	for _, r := range []Reset{ResetDaily, ResetWeekly, ResetMonthly} {
		t.Run(string(r), func(t *testing.T) {
			b := newBank(t, 1e6, 0, Config{})
			a := b.admitted
			a.KeyLimit = &Limit{Amount: 1000, Reset: r}
			h, err := b.reserve(a, 600)
			if err != nil {
				t.Fatal(err)
			}
			// As if admitted on the last day of the previous window.
			before := fmt.Sprintf("billing.window_from('%s', clock_timestamp()) - interval '1 day'", r)
			b.sql("UPDATE billing.reservations SET workspace_window = "+before+", key_window = "+before,
				"UPDATE billing.spend SET window_start = "+before)
			if _, err := b.reserve(a, 600); err != nil { // the current window's own 600, which the old one must not count
				t.Fatal(err)
			}
			b.extendTo(h, 900, true)
			b.extendTo(h, 1200, false)
			if n := b.count("SELECT sum(reserved) FROM billing.spend WHERE window_start < " + "billing.window_from('" + string(r) + "', clock_timestamp())"); n != 1800 {
				t.Errorf("the previous window holds %d, want 900 for the key and 900 for the workspace", n)
			}
		})
	}
}

// extendTo extends h to target billionths and checks whether it was allowed.
func (b *bank) extendTo(h *Hold, target int64, allowed bool) {
	b.t.Helper()
	if err := b.billing.Extend(b.t.Context(), h, ExtendRequest{Seq: 2, Target: platform.AmountExact(target)}); (err == nil) != allowed {
		b.t.Errorf("extending to %d: %v, want allowed %v", target, err, allowed)
	}
}
