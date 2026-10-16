package suppliers

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// JournalEntry says what an attempt has done so far: that it began (a receipt without a request id), that the supplier
// answered with its request id (the receipt has it), or that content was handed to the client (Committed). Site and
// Account are the request's tenant, nil for the platform's own requests.
type JournalEntry struct {
	Request       platform.RequestID
	Seq           platform.AttemptSeq
	Site, Account *uuid.UUID
	Receipt       AttemptReceipt
	Committed     bool
}

// journal is the node's buffer of attempts not yet written. Entries of one attempt merge into one row (a request id
// that arrived stays, a commit is not undone), so the buffer holds at most one row per attempt of the requests that
// are still running: the node's slots times three. Nothing is dropped: a row leaves the buffer when it is written, or
// when its request is settled and the row would be of no use.
type journal struct {
	mu       sync.Mutex
	rows     map[journalKey]*journalRow
	last     map[platform.RequestID]uint64 // the number of the latest entry of each request with rows in the buffer
	appended uint64                        // entries appended so far
	flushed  uint64                        // every entry numbered up to here has been written
	waiters  []waiter
}

type journalKey struct {
	request platform.RequestID
	seq     platform.AttemptSeq
}

type journalRow struct {
	row store.JournalRow
	n   uint64 // the number of the latest entry merged into the row
}

type waiter struct {
	request platform.RequestID
	upTo    uint64
	done    chan struct{}
}

func newJournal() *journal {
	return &journal{rows: map[journalKey]*journalRow{}, last: map[platform.RequestID]uint64{}}
}

// Journal appends an entry. It does not block and does not touch the database: the rows are written once a second by
// Run, in one statement.
func (s *Service) Journal(e JournalEntry) { s.journal.add(e) }

// JournalFlushed returns a channel that closes once every entry appended for the request before the call is written.
// It is complete only if the request will append no more: gateway calls it after the executor has stopped.
func (s *Service) JournalFlushed(id platform.RequestID) <-chan struct{} {
	return s.journal.flushedChan(id)
}

// Settled tells the journal that the request's settlement is committed, which carries the reconciliation keys the
// journal exists to keep, so what is not written yet is dropped, and whoever waits for the request is released.
func (s *Service) Settled(id platform.RequestID) { s.journal.settle(id) }

// JournalBuffered is how many attempts are waiting to be written.
func (s *Service) JournalBuffered() int {
	s.journal.mu.Lock()
	defer s.journal.mu.Unlock()
	return len(s.journal.rows)
}

func (j *journal) add(e JournalEntry) {
	r := e.Receipt
	row := store.JournalRow{
		RequestID: uuid.UUID(e.Request), AttemptSeq: int16(e.Seq), SiteID: e.Site, AccountID: e.Account,
		SupplierID: r.supplier, CredentialID: r.credential, EndpointID: r.endpoint, ScheduleID: r.schedule,
		PricedAt: r.pricedAt, Committed: e.Committed,
	}
	if r.ref != "" {
		row.ChargeRef = &r.ref
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appended++
	k := journalKey{e.Request, e.Seq}
	if cur, ok := j.rows[k]; ok {
		if cur.row.ChargeRef != nil && row.ChargeRef == nil {
			row.ChargeRef = cur.row.ChargeRef
		}
		row.Committed = row.Committed || cur.row.Committed
	}
	j.rows[k] = &journalRow{row: row, n: j.appended}
	j.last[e.Request] = j.appended
}

func (j *journal) flushedChan(id platform.RequestID) <-chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	done := make(chan struct{})
	upTo, pending := j.last[id]
	if !pending || upTo <= j.flushed {
		close(done)
		return done
	}
	j.waiters = append(j.waiters, waiter{request: id, upTo: upTo, done: done})
	return done
}

func (j *journal) settle(id platform.RequestID) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for seq := platform.AttemptSeq(1); seq <= platform.MaxAttempts; seq++ {
		delete(j.rows, journalKey{id, seq})
	}
	delete(j.last, id)
	j.waiters = slices.DeleteFunc(j.waiters, func(w waiter) bool {
		if w.request == id {
			close(w.done)
			return true
		}
		return false
	})
}

// take is the rows to write now and the number of the latest entry among them.
func (j *journal) take() (rows []store.JournalRow, through uint64, taken map[journalKey]uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	taken = make(map[journalKey]uint64, len(j.rows))
	for k, r := range j.rows {
		rows = append(rows, r.row)
		taken[k] = r.n
	}
	slices.SortFunc(rows, func(a, b store.JournalRow) int {
		if c := bytes.Compare(a.RequestID[:], b.RequestID[:]); c != 0 {
			return c
		}
		return int(a.AttemptSeq) - int(b.AttemptSeq)
	})
	return rows, j.appended, taken
}

// written drops the rows that were written and have not changed since, and releases those waiting for entries up to
// the number through. A row that changed after take stays for the next write, which only adds to what is stored.
func (j *journal) written(through uint64, taken map[journalKey]uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for k, n := range taken {
		if cur, ok := j.rows[k]; ok && cur.n == n {
			delete(j.rows, k)
		}
	}
	j.flushed = through
	for id, n := range j.last {
		if n <= through {
			delete(j.last, id)
		}
	}
	j.waiters = slices.DeleteFunc(j.waiters, func(w waiter) bool {
		if w.upTo <= through {
			close(w.done)
			return true
		}
		return false
	})
}

// Run writes the journal every FlushEvery until ctx ends, then once more. A write that fails leaves the rows where
// they are and is tried again, waiting longer each time up to half a minute: the journal is never dropped for want of
// the database, and its size is bounded by the requests that are running, not by how long the database is gone.
func (s *Service) Run(ctx context.Context) error {
	wait := s.cfg.FlushEvery
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return s.Flush(flushCtx)
		case <-time.After(wait):
		}
		if err := s.Flush(ctx); err != nil {
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = s.cfg.FlushEvery
	}
}

// Flush writes what is buffered in one statement.
func (s *Service) Flush(ctx context.Context) error {
	rows, through, taken := s.journal.take()
	if len(rows) > 0 {
		if err := s.writeJournal(ctx, rows); err != nil {
			return fmt.Errorf("write the journal: %w", err)
		}
	}
	s.journal.written(through, taken)
	return nil
}

func (s *Service) writeJournal(ctx context.Context, rows []store.JournalRow) error {
	if s.writeRows != nil {
		return s.writeRows(ctx, rows)
	}
	return s.cfg.DB.Tx(ctx, s.cfg.Scope, func(tx pg.Tx) error { return store.UpsertJournal(ctx, tx, rows) })
}

// JournalOf reads the journal of a request, for cleanup to learn what a node that died had done: which attempts reached
// the supplier, with which key, and whether any content was handed to the client.
func JournalOf(ctx context.Context, tx pg.Tx, id platform.RequestID) ([]JournalEntry, error) {
	rows, err := store.New().ListJournal(ctx, tx, uuid.UUID(id))
	if err != nil {
		return nil, fmt.Errorf("read the journal of %s: %w", id, err)
	}
	entries := make([]JournalEntry, len(rows))
	for i, r := range rows {
		receipt := AttemptReceipt{
			supplier: r.SupplierID, credential: r.CredentialID, endpoint: r.EndpointID,
			schedule: r.CostScheduleID, pricedAt: r.PricedAt,
		}
		if r.ChargeRef != nil {
			receipt.ref = *r.ChargeRef
		}
		entries[i] = JournalEntry{
			Request: id, Seq: seqOf(r.AttemptSeq), Site: r.SiteID, Account: r.AccountID,
			Receipt: receipt, Committed: r.Committed,
		}
	}
	return entries, nil
}

// seqOf reads an attempt number from its column, which holds 1 to 3.
func seqOf(n int16) platform.AttemptSeq { return platform.AttemptSeq(n) } //nolint:gosec // the column holds 1 to 3

// PurgeJournal removes the rows written before the time given, which a settled request no longer needs.
func PurgeJournal(ctx context.Context, tx pg.Tx, before time.Time) (int64, error) {
	return store.New().PurgeJournal(ctx, tx, before)
}
