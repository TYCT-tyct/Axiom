package observability

import (
	"errors"
	"fmt"
	"sync"
)

// Slot is one call's reserved room in the call log. Admit returns it; Commit puts the call's record in the spill, and
// Release gives the room back if the call ends without one. It is safe to call Release after Commit: the writer then
// owns the reservation until the record reaches the database. A pending record (CallRecord.BillingPending) is parked
// instead and keeps the slot open: the call's settled record replaces it, or Release moves it into the spill when the
// settlement is left to the cleanup. Either way one record of the call takes the slot's room.
type Slot struct {
	r      *Recorder
	mu     sync.Mutex
	state  slotState
	id     string // the parked record's request ID, while parked
	parked []byte // the parked record's encoding, while parked
}

type slotState uint8

const (
	slotOpen   slotState = iota
	slotParked           // a pending record is parked; the slot's room waits for the call's settled record
	slotCommitted
	slotReleased
)

// Commit puts the call's record in the spill and makes it durable before returning: it survives the process and the
// host from here on, and the writer replays it on the next start. The response may be served after this returns. A
// pending record is parked rather than put in the spill, equally durable, and the slot then takes one settled record
// of the same call.
func (s *Slot) Commit(rec CallRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != slotOpen && (s.state != slotParked || rec.BillingPending) {
		return ErrClosed
	}
	body, err := s.r.encode(rec)
	if err == nil && rec.BillingPending {
		err = s.r.park(rec, body)
	} else if err == nil {
		err = s.r.append(rec, body)
	}
	switch {
	case err != nil:
		// A parked record stays parked: it is the call's record, which the next start moves into the spill.
		s.state = slotReleased
		s.r.outstanding.Add(-1)
		s.r.finishSlot()
		return fmt.Errorf("call record %s: %w", rec.ID, err)
	case rec.BillingPending:
		s.state, s.id, s.parked = slotParked, rec.ID.String(), body
		s.r.parked.Add(1)
	default:
		if s.state == slotParked {
			s.r.parking.unpark(s.id)
			s.r.parked.Add(-1)
		}
		s.state, s.parked = slotCommitted, nil
		s.r.finishSlot()
	}
	return nil
}

// Release gives back a reservation whose call produced no record. A parked record whose settlement went to the
// cleanup is the call's record: Release moves it into the spill, in the slot's room. When the spill no longer takes
// records (a shutdown, or a disk that failed), the record stays parked for the next start. Release is a no-op after
// the settled record's Commit.
func (s *Slot) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case slotOpen:
		s.r.outstanding.Add(-1)
	case slotParked:
		if s.r.spill.appendSynced(s.parked, true) == nil {
			s.r.parking.unpark(s.id)
			s.r.parked.Add(-1)
		} else {
			s.r.outstanding.Add(-1)
		}
		s.parked = nil
	case slotCommitted, slotReleased:
		return
	}
	s.state = slotReleased
	s.r.finishSlot()
}

// encode validates and encodes rec; a record that cannot be encoded stops the recorder.
func (r *Recorder) encode(rec CallRecord) ([]byte, error) {
	call, attempts, err := newRows(rec)
	var body []byte
	if err == nil {
		body, err = encodeRecord(call, attempts)
	}
	if err != nil {
		r.reject(rec, "invalid", err)
	}
	return body, err
}

// append puts an encoded record in the spill; a record the spill does not keep stops the recorder.
func (r *Recorder) append(rec CallRecord, body []byte) error {
	err := r.spill.appendSynced(body, false)
	if err != nil {
		r.rejectUnkept(rec, err)
	}
	return err
}

// park parks an encoded pending record. Once Close has begun nothing is parked: the directory is about to pass to the
// next process.
func (r *Recorder) park(rec CallRecord, body []byte) error {
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	err := ErrClosed
	if !closing {
		err = r.parking.park(rec.ID.String(), body)
	}
	if err != nil {
		r.rejectUnkept(rec, err)
	}
	return err
}

// rejectUnkept rejects a record the disk did not keep, naming a shutdown as such.
func (r *Recorder) rejectUnkept(rec CallRecord, err error) {
	reason := "spill"
	if errors.Is(err, ErrClosed) {
		reason = "shutdown"
	}
	r.reject(rec, reason, err)
}
