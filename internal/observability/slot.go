package observability

import (
	"errors"
	"fmt"
	"sync"
)

// Slot is one call's reserved room in the call log. Admit returns it; Commit puts the call's record in the spill, and
// Release gives the room back if the call ends without one. It is safe to call Release after Commit: the writer then
// owns the reservation until the record reaches the database. A pending record (CallRecord.BillingPending) does not
// close the slot: the room stays reserved for the settled record that completes it, and Release gives that room back
// when the settlement is left to the cleanup.
type Slot struct {
	r     *Recorder
	mu    sync.Mutex
	state slotState
}

type slotState uint8

const (
	slotOpen      slotState = iota
	slotPending             // a pending record is in the spill; the slot's own room waits for the settled one
	slotCommitted           // the call's settled record is in the spill
	slotReleased
)

// Commit puts the call's record in the spill and makes it durable before returning: it survives the process and the
// host from here on, and the writer replays it on the next start. The response may be served after this returns.
//
// A pending record leaves the slot open for one settled record of the same call. The pending record is counted as one
// more outstanding record beyond the slot's own room, so a node can hold at most twice Capacity records when every
// call it admitted was left pending.
func (s *Slot) Commit(rec CallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != slotOpen && (s.state != slotPending || rec.BillingPending) {
		return ErrClosed
	}
	if rec.BillingPending {
		s.r.outstanding.Add(1) // before the append: the writer may take the record off the count as soon as it is there
	}
	err := s.append(rec)
	switch {
	case err != nil:
		if rec.BillingPending {
			s.r.outstanding.Add(-1)
		}
		s.state = slotReleased
		s.r.outstanding.Add(-1)
		s.r.finishSlot()
		return fmt.Errorf("call record %s: %w", rec.ID, err)
	case rec.BillingPending:
		s.state = slotPending
	default:
		s.state = slotCommitted
		s.r.finishSlot()
	}
	return nil
}

// append validates, encodes and appends rec, making it durable; a record that cannot be kept stops the recorder.
func (s *Slot) append(rec CallRecord) error {
	call, attempts, err := newRows(rec)
	var body []byte
	if err == nil {
		body, err = encodeRecord(call, attempts)
	}
	if err != nil {
		s.r.reject(rec, "invalid", err)
		return err
	}
	if err := s.r.spill.appendSynced(body); err != nil {
		reason := "spill"
		if errors.Is(err, ErrClosed) {
			reason = "shutdown"
		}
		s.r.reject(rec, reason, err)
		return err
	}
	return nil
}

// Release gives back a reservation whose call produced no record, or the room a pending record held for its settled
// one. It is a no-op after the settled record's Commit.
func (s *Slot) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == slotOpen || s.state == slotPending {
		s.state = slotReleased
		s.r.outstanding.Add(-1)
		s.r.finishSlot()
	}
}
