package protocol

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestPrepareUnrecoverableMemoryRefusalDoesNotQueue: native conversion's shape reservation can exceed the entire
// budget even though the body fits. Such a refusal must return while another preparation still holds its read gate.
func TestPrepareUnrecoverableMemoryRefusalDoesNotQueue(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"` + strings.Repeat("{", 4096) + `"}]}`)
	r, err := Inspect(Messages, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		held int64
	}{{"no competitor", 0}, {"competitor cannot cover it", 1 << 18}} {
		t.Run(c.name, func(t *testing.T) {
			m := NewMemory(1 << 20)
			competitor := &budget{m: m}
			if err := competitor.draw(c.held); err != nil {
				t.Fatal(err)
			}
			checkRefusalDoesNotQueue(t, m, &r, body)
			competitor.give(c.held)
			if m.left.Load() != 1<<20 {
				t.Fatalf("after refusal: left %d", m.left.Load())
			}
		})
	}
}

// TestPrepareSentBodiesCannotFundRetries: a prepared body stays charged until Send returns, so waiting for
// preparations cannot recover its bytes to cover another request's failed reservation.
func TestPrepareSentBodiesCannotFundRetries(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	sent, err := Inspect(Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(Messages, body)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Format: Chat, Model: "up"}
	_, capacity, err := PrepareBudgeted(nil, &sent, body, target, Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	size := nativeRequestCost(body) + capacity - 1
	m := NewMemory(size)
	_, held, err := PrepareBudgeted(m, &sent, body, target, Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	checkRefusalDoesNotQueue(t, m, &r, body)
	m.Refund(held)
	if m.left.Load() != size || m.preparationHeld != 0 {
		t.Fatalf("after sending: left %d, preparing %d", m.left.Load(), m.preparationHeld)
	}
}

func checkRefusalDoesNotQueue(t *testing.T, m *Memory, r *Request, body []byte) {
	t.Helper()
	left := m.left.Load()
	preparing := m.preparationHeld
	var err error
	m.preparing.RLock()
	done := make(chan error, 1)
	go func() {
		_, _, err := PrepareBudgeted(m, r, body, Target{Format: Chat, Model: "up"}, Rewrites{})
		done <- err
	}()
	queued := false
	func() {
		defer m.preparing.RUnlock()
		for {
			select {
			case err = <-done:
				return
			default:
			}
			if !m.preparing.TryRLock() {
				queued = true
				return
			}
			m.preparing.RUnlock()
			runtime.Gosched()
		}
	}()
	if queued {
		err = <-done
		t.Error("an unrecoverable refusal queued an exclusive retry")
	}
	if !errors.Is(err, ErrMemory) || m.left.Load() != left || m.preparationHeld != preparing {
		t.Fatalf("refusal: left %d, preparing %d, error %v", m.left.Load(), m.preparationHeld, err)
	}
}
