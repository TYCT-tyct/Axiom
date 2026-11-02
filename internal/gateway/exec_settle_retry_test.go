package gateway

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/platform"
)

// TestASettlementIsRetriedUntilItIsWritten: the same outcome each time, so a try that committed with its
// acknowledgement lost costs nothing more; the ending carries the cost of the one written, and the call gives its slots
// back once it is written.
func TestASettlementIsRetriedUntilItIsWritten(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{
			final: billing.Final{Charged: platform.AmountExact(5000)},
			settle: func(try int) error {
				if try < 3 {
					return io.ErrUnexpectedEOF
				}
				return nil
			},
		})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		if !strings.Contains(body, `"cost":0.000005,"cost_currency":"USD"`) || strings.Contains(body, "billing_pending") {
			t.Errorf("the ending:\n%s", body)
		}
		h.mu.Lock()
		tries := len(h.settlements)
		h.mu.Unlock()
		if tries != 3 || !h.isReleased() {
			t.Errorf("%d tries, released %v", tries, h.isReleased())
		}
	})
}

// TestASettlementNotWrittenInTimeGoesOnInTheBackground: after 10s the ending goes out without a cost and says the
// billing is pending; the settlement goes on with the same outcome, and the call keeps its slots until it is written.
func TestASettlementNotWrittenInTimeGoesOnInTheBackground(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		start := time.Now()
		h := newHarness(t, ctx, options{settle: func(int) error {
			if time.Since(start) < time.Minute {
				return io.ErrUnexpectedEOF
			}
			return nil
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		<-h.start()
		if h.elapsed() != h.cfg.SettleRetry || !strings.Contains(h.w.body(), `"billing_pending":true`) || strings.Contains(h.w.body(), `"cost"`) {
			t.Fatalf("after %v:\n%s", h.elapsed(), h.w.body())
		}
		if h.isReleased() {
			t.Fatal("the call gave its slots back before its settlement was written")
		}
		<-h.released
		if h.elapsed() < time.Minute {
			t.Errorf("released after %v, before the settlement could be written", h.elapsed())
		}
		h.check()
	})
}

// TestDrainDoesNotAddGracePastTheDeadline: the drain's grace is part of the node's deadline, not a new wait appended
// after it. A settlement still blocked when the deadline arrives must make Drain return there, not five seconds later;
// the controlled clock proves the whole path, not just the arithmetic.
func TestDrainDoesNotAddGracePastTheDeadline(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		release := make(chan struct{})
		h := newHarness(t, ctx, options{settle: func(int) error {
			<-release
			return nil
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		done := h.start()
		time.Sleep(time.Second) // the attempt is over; its settlement is blocked
		stop, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		h.exec.Outstanding.Drain(stop, 5*time.Second)
		if h.elapsed() != 11*time.Second {
			t.Errorf("drain returned after %v; want the deadline at 11s, not a grace after it", h.elapsed())
		}
		close(release)
		<-done
		h.check()
	})
}
