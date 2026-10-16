package gateway

import (
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// count reads one number as the platform, which sees every tenant's rows.
func (r *rig) count(query string, args ...any) (n int64) {
	r.t.Helper()
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(r.t.Context(), query, args...).Scan(&n)
	}); err != nil {
		r.t.Fatal(err)
	}
	return n
}

const (
	pendingCount = "SELECT count(*) FROM billing.reservations WHERE state = 'pending'"
	pendingSum   = "SELECT coalesce(sum(amount), 0) FROM billing.reservations WHERE state = 'pending'"
	reservedSum  = "SELECT coalesce(sum(reserved), 0) FROM billing.balances"
	allRows      = "SELECT count(*) FROM billing.reservations"
)

// consistent checks that what the balance rows hold reserved is what the pending reservations add up to, and that no
// balance went below its floor, as billing's own tests do.
func (r *rig) consistent() {
	r.t.Helper()
	if held, pending := r.count(reservedSum), r.count(pendingSum); held != pending {
		r.t.Errorf("the balances hold %d reserved, the pending reservations add up to %d", held, pending)
	}
	if n := r.count("SELECT count(*) FROM billing.balances WHERE balance - reserved < floor"); n != 0 {
		r.t.Errorf("%d balances passed their floor", n)
	}
}

// quote is what a request reserves for an endpoint that charges out billionths a token of output: the input at its
// bound, which is the body's length in bytes, and the longest output in full.
func quote(body string, out int64) int64 { return int64(len(body))*inputUnit + maxOutput*out }

// idle checks that nothing is held: no slot of an account or of the node, no memory for a body.
func (r *rig) idle(budget int64) {
	r.t.Helper()
	node, accounts := r.limits.InFlight.node.size(), r.limits.InFlight.accounts.size()
	whole := r.memory.Charge(budget) // the whole budget can be drawn only if nothing is out
	if whole {
		r.memory.Refund(budget)
	}
	if node != 0 || accounts != 0 || !whole {
		r.t.Errorf("slots held: node %d, accounts %d; the whole memory budget is free: %v", node, accounts, whole)
	}
}

func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}

// asked waits until n calls have been admitted, or fails.
func (r *rig) asked(n int) []*Call {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if calls := r.calls(); len(calls) >= n {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("%d calls admitted, want %d", len(r.calls()), n)
	return nil
}

// answer is one response of a request sent from a goroutine.
type answer struct {
	resp reply
	body string
}

// together sends the requests at once and calls during once they are on their way; the answers come back in order.
func together(during func(), requests ...func() (reply, string)) []answer {
	answers := make([]answer, len(requests))
	var wg sync.WaitGroup
	for i, send := range requests {
		wg.Go(func() { answers[i].resp, answers[i].body = send() })
	}
	during()
	wg.Wait()
	return answers
}

// errorOf reads the unified error_type and the shell's own type out of an error body of either shell.
func errorOf(t *testing.T, body string) (code, typ string) {
	t.Helper()
	var shell struct {
		Error struct {
			Type      string `json:"type"`
			ErrorType string `json:"error_type"`
			Metadata  struct {
				ErrorType string `json:"error_type"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if err := jsonx.Unmarshal([]byte(body), &shell); err != nil {
		t.Fatalf("not an error body: %q: %v", body, err)
	}
	return shell.Error.ErrorType + shell.Error.Metadata.ErrorType, shell.Error.Type
}
