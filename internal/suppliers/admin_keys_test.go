package suppliers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/registry"
)

// worldAdmin is the admin side over a world's database, on the world's clock.
func worldAdmin(w *world) *Admin {
	w.t.Helper()
	reg, err := registry.New(fakeKind{})
	if err != nil {
		w.t.Fatal(err)
	}
	a, err := NewAdmin(AdminConfig{Keys: w.keys, Registry: reg, Clock: w.clock.now})
	if err != nil {
		w.t.Fatal(err)
	}
	return a
}

// keyWorld is a world with its admin side, whose changes of the credential reload the service as the handler does.
type keyWorld struct {
	*world
	admin *Admin
	who   audit.Actor
}

// change runs a change of the credential and reloads the service, and answers the status it left and whether the
// endpoint still routes.
func (w keyWorld) change(f func(ctx context.Context, tx pg.Tx) (Credential, error)) []any {
	w.t.Helper()
	var c Credential
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		c, err = f(ctx, tx)
		return err
	})
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	return []any{c.Status, w.routes()}
}

// routes is whether the endpoint admits a call now.
func (w keyWorld) routes() bool {
	a, err := w.svc.Acquire(w.endpoint)
	if err == nil {
		a.Done()
	} else if !errors.Is(err, ErrNoEndpoint) {
		w.t.Fatal(err)
	}
	return err == nil
}

func (w keyWorld) enable(on bool) []any {
	return w.change(func(ctx context.Context, tx pg.Tx) (Credential, error) {
		return w.admin.SetCredentialEnabled(ctx, tx, w.who, w.supplier, w.credential, on, "a switch")
	})
}

func (w keyWorld) remove() []any {
	return w.change(func(ctx context.Context, tx pg.Tx) (Credential, error) {
		return w.admin.RemoveCredential(ctx, tx, w.who, w.supplier, w.credential, "leaked")
	})
}

func (w keyWorld) setStatus(status string) {
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.credentials SET status = $2 WHERE id = $1", w.credential, status)
		return err
	})
}

// TestADisabledOrErasedKeyLeavesRouting: routing goes through the credential, a person switches it off and the next
// reload routes nothing through it, though a call that holds it ends with it; switched on it routes again; a refused key
// switched on stays refused; erased, its key is gone from the table and it never routes again.
func TestADisabledOrErasedKeyLeavesRouting(t *testing.T) {
	t.Parallel()
	w := keyWorld{newWorld(t, 4), nil, audit.User(uuid.NewV7())}
	w.admin = worldAdmin(w.world)
	inFlight, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("the credential does not route: %v", err)
	}
	same(t, "switched off", []any{"disabled", false}, w.enable(false))
	same(t, "the call in flight ends with its credential", w.credential, inFlight.Done().CredentialID())
	same(t, "switched on", []any{"active", true}, w.enable(true))
	w.setStatus("rejected")
	same(t, "a refused key switched on", []any{"rejected", false}, w.enable(true))
	w.setStatus("active")
	same(t, "erased", []any{"erased", false}, w.remove())
	same(t, "erased again", []any{"erased", false}, w.remove())
	var sealed bool
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT ciphertext IS NOT NULL FROM suppliers.credentials WHERE id = $1", w.credential).Scan(&sealed)
	})
	same(t, "the key sealed after erasing", false, sealed)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := w.admin.SetCredentialEnabled(ctx, tx, w.who, w.supplier, w.credential, true, "bring it back")
		refuses(t, "switching an erased key on", err, ErrStatusLocked)
		return nil
	})
}

// TestCredentialLoadIsThisNodesCount: the calls holding a credential and its account's requests in the rolling minute,
// as this node counted them, whether the account is the relay's or the key's own (a direct API's); a credential this
// node never loaded holds nothing.
func TestCredentialLoadIsThisNodesCount(t *testing.T) {
	t.Parallel()
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%t", direct), func(t *testing.T) {
			t.Parallel()
			w := newWorldWith(t, 4, fakeKind{}, messagesAPI)
			if direct {
				w.asDirect(string(messagesAPI.Kind()))
			}
			a, err := w.svc.Acquire(w.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			held := w.svc.CredentialLoad(w.credential, w.supplier)
			a.Done()
			after := w.svc.CredentialLoad(w.credential, w.supplier)
			w.clock.advance(61 * time.Second)
			got := []Load{held, after, w.svc.CredentialLoad(w.credential, w.supplier), w.svc.CredentialLoad(uuid.NewV7(), uuid.NewV7())}
			// holding a call, after it, a minute later, and a credential never loaded
			if want := []Load{{InFlight: 1, LastMinute: 1}, {LastMinute: 1}, {}, {}}; !slices.Equal(got, want) {
				t.Errorf("the loads are %+v, want %+v", got, want)
			}
		})
	}
}

// TestCheckedStatus: a pass brings a refused key back, a refusal as not valid makes a serving key refused, and nothing
// else moves a status; a person's switch and an erasure always stand.
func TestCheckedStatus(t *testing.T) {
	t.Parallel()
	passed := KeyCheck{Outcome: CheckPassed}
	refused := KeyCheck{Outcome: CheckFailed, Cause: CauseRejected}
	quota := KeyCheck{Outcome: CheckFailed, Cause: CauseQuota}
	unclear := KeyCheck{Outcome: CheckInconclusive, Cause: CauseTimeout}
	for _, tc := range []struct {
		from  string
		check KeyCheck
		want  string
	}{
		{"rejected", passed, "active"},
		{"active", passed, "active"},
		{"upstream_disabled", passed, "upstream_disabled"},
		{"active", refused, "rejected"},
		{"quota_exhausted", refused, "rejected"},
		{"active", quota, "active"},
		{"rejected", unclear, "rejected"},
		{"disabled", refused, "disabled"},
		{"disabled", passed, "disabled"},
		{"erased", passed, "erased"},
	} {
		if got := checkedStatus(tc.from, tc.check); got != tc.want {
			t.Errorf("%s after %s/%s: %s, want %s", tc.from, tc.check.Outcome, tc.check.Cause, got, tc.want)
		}
	}
}

// TestJudgeCheck: 2xx passes; 401 is the key refused; 403 is the key's own quota only when the adapter says so and is
// otherwise inconclusive; the rest say nothing of the key.
func TestJudgeCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status  int
		quirks  fakeQuirks
		outcome CheckOutcome
		cause   CheckCause
	}{
		{http.StatusOK, fakeQuirks{}, CheckPassed, ""},
		{http.StatusUnauthorized, fakeQuirks{}, CheckFailed, CauseRejected},
		{http.StatusForbidden, fakeQuirks{}, CheckFailed, CauseQuota},
		{http.StatusTooManyRequests, fakeQuirks{}, CheckInconclusive, CauseRateLimited},
		{http.StatusBadGateway, fakeQuirks{}, CheckInconclusive, CauseUpstreamError},
		{http.StatusNotFound, fakeQuirks{}, CheckInconclusive, CauseUnknown},
	} {
		outcome, cause := judgeCheck(tc.quirks, tc.status, http.Header{}, nil)
		if outcome != tc.outcome || cause != tc.cause {
			t.Errorf("%d: %s/%s, want %s/%s", tc.status, outcome, cause, tc.outcome, tc.cause)
		}
	}
}

// TestDifferenceBP: (charged − estimated) / estimated in basis points, exact, rounded half away from zero; nothing
// estimated has no difference.
func TestDifferenceBP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		estimated, charged string
		want               *int64
	}{
		{"100", "100", new(int64(0))},
		{"100", "101", new(int64(100))},
		{"100", "99", new(int64(-100))},
		{"20000", "20001", new(int64(1))},  // exactly 0.5 bp: away from zero
		{"20000", "19999", new(int64(-1))}, // exactly −0.5 bp
		{"30000", "30001", new(int64(0))},  // 0.33 bp
		{"0.000000000000001", "0.000000000000002", new(int64(10000))},
		{"0", "5", nil},
	} {
		bp, ok, err := differenceBP(tc.estimated, tc.charged)
		if err != nil {
			t.Fatalf("%s against %s: %v", tc.charged, tc.estimated, err)
		}
		var got *int64
		if ok {
			got = &bp
		}
		if deref64(got) != deref64(tc.want) {
			t.Errorf("%s against %s: %v, want %v", tc.charged, tc.estimated, deref64(got), deref64(tc.want))
		}
	}
	if _, _, err := differenceBP("x", "1"); err == nil {
		t.Error("an estimate that is no number was read")
	}
}

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestACallsRefusalIsInTheQueueAtOnce: a key the supplier refused in a call is marked rejected and is in the supplier's
// queue in the same write, whatever its mode: no sync reads a direct API's or a cloud supplier's keys again.
func TestACallsRefusalIsInTheQueueAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorldWith(t, 4, fakeKind{}, messagesAPI)
	w.asDirect("messagesapi")
	var status string
	var queued int
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		if err := persist.RejectCredential(ctx, tx, w.supplier, w.credential, w.clock.now()); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT (SELECT status FROM suppliers.credentials WHERE id = $1),
			(SELECT count(*)::int FROM suppliers.inbox_items WHERE kind = 'key_invalid' AND subject = $1::text)`, w.credential).Scan(&status, &queued)
	})
	same(t, "the refused key", []any{"rejected", 1}, []any{status, queued})
}
