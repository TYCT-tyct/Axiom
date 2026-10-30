package gateway

import (
	"container/list"
	"context"
	"sync"
	"time"

	"axiom/internal/platform/pg"
)

// The bounds of one account's batch. An admission batch is as large as the completion pipeline can write in one
// admission budget; a completion batch is smaller because one settlement's retry has only settleAttempt.
const (
	admissionBatchMax  = 32
	completionBatchMax = 16
	// admissionCollect is how long a batch that already has peers waits for more before it starts. A batch of one
	// never waits: a cold account pays no added latency, and the next requests of a burst form the batch behind it.
	admissionCollect = time.Millisecond
)

// gateItem is one request's place in the account's queue and its result. The run function of its class reads payload,
// which is the caller's own item; a batch holds one class only.
type gateItem struct {
	class    Class
	ctx      context.Context
	run      batchRun
	payload  any
	done     chan struct{}
	err      error
	panicked any
	closed   bool
	taken    bool
	elem     *list.Element
	queue    *accountQueue
}

// batchRun writes one batch. Items whose work failed carry their errors; an error returned for the batch itself is
// left on every item that has none. A batch uses the run of the item that led it, so one class's callers pass the
// same function and it captures no per-request state.
type batchRun func(context.Context, []*gateItem) error

// accountQueue is one account's waiters, completion class first, and whether a leader is already serving it.
type accountQueue struct {
	completion, admission list.List
	leader                bool
	since                 time.Time // when the current batch was taken, for its bounded collection
}

func (q *accountQueue) of(class Class) *list.List {
	if class == Completion {
		return &q.completion
	}
	return &q.admission
}

func (q *accountQueue) empty() bool { return q.completion.Len() == 0 && q.admission.Len() == 0 }

// batchGate lets one batch of one account's money transactions run at a time. The balance row lock does that in the
// database too, but a transaction waiting for it holds a connection; waiting here holds none, so a hot account
// queues in memory and cannot fill the pool for other tenants.
//
// Completions go before admissions: a settlement that waited behind admissions would keep the reservation, and the
// in-flight slot it pins, for longer. Within a class the order is first come.
type batchGate struct {
	mu            sync.Mutex
	admissionWait time.Duration
	accounts      map[string]*accountQueue
}

func newBatchGate(admissionWait time.Duration) *batchGate {
	return &batchGate{admissionWait: admissionWait, accounts: map[string]*accountQueue{}}
}

// Funds starts the transactions that move money: admission, extension, settlement and SettleOrphan. The order is
// the account gate first and the connection second, so a transaction waiting for its account holds no connection.
type Funds struct {
	gate   *batchGate
	pool   *Pool
	budget time.Duration // an admission batch's connection and transaction, counted from the moment it holds the gate
}

// Admit is for admission (class Admission). item is the caller's request; run writes the batch. The returned context
// of the batch carries the budget: the wait for the gate is the gate's own limit and is not part of it, because that
// wait holds no database resource; the budget bounds only what does.
func (f *Funds) Admit(ctx context.Context, account string, item *gateItem, run batchRun) error {
	item.class = Admission
	return f.submit(ctx, account, item, run)
}

// Complete is for extension, settlement and SettleOrphan (class Completion): ahead of admissions at the gate, and
// able to use the connections admissions may not.
func (f *Funds) Complete(ctx context.Context, account string, item *gateItem, run batchRun) error {
	item.class = Completion
	return f.submit(ctx, account, item, run)
}

// submit queues item and returns its result. The first request of an idle account starts a leader goroutine that
// serves the queue until no waiter is left; every request, the leader's included, waits only for the batch that takes
// it, so a busy account cannot hold its first request past the end of its own batch.
func (f *Funds) submit(ctx context.Context, account string, item *gateItem, run batchRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g := f.gate
	g.mu.Lock()
	q := g.accounts[account]
	if q == nil {
		q = &accountQueue{}
		g.accounts[account] = q
	}
	item.ctx, item.queue, item.run = ctx, q, run
	item.elem = q.of(item.class).PushBack(item)
	leader := !q.leader
	q.leader = true
	g.mu.Unlock()

	if leader {
		go f.lead(account, q)
	}
	return f.await(item)
}

// await waits for the batch that takes item. An admission waits at most admissionWait to be taken and then gets
// errAccountGate, so a client that hammers one account is refused rather than left holding a request open; once a
// batch takes it, the budget applies instead. A completion waits for as long as ctx lets it, since it carries money
// that is already committed.
func (f *Funds) await(item *gateItem) error {
	var expiredAt <-chan time.Time
	if item.class == Admission {
		t := time.NewTimer(f.gate.admissionWait)
		defer t.Stop()
		expiredAt = t.C
	}
	select {
	case <-item.done:
	case <-expiredAt:
		if f.gate.withdraw(item) {
			return expired(item.ctx, errAccountGate)
		}
		<-item.done
	case <-item.ctx.Done():
		if f.gate.withdraw(item) {
			return item.ctx.Err()
		}
		<-item.done
	}
	if item.panicked != nil {
		panic(item.panicked)
	}
	return item.err
}

// withdraw removes an item that no batch has taken, and reports whether it did. An item a batch took stays until
// the batch gives it a result.
func (g *batchGate) withdraw(item *gateItem) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if item.taken {
		return false
	}
	if item.elem != nil {
		item.queue.of(item.class).Remove(item.elem)
		item.elem = nil
	}
	return true
}

// lead serves q until it is empty: take a batch, give it a bounded time to collect peers, run it, repeat. It runs in
// its own goroutine, so a request returns when its batch ends even if the account has more waiters; the leader never
// abandons the queue because its own request was cancelled, since the others are still waiting.
func (f *Funds) lead(account string, q *accountQueue) {
	for {
		batch, class := f.take(q)
		if len(batch) == 0 {
			if f.close(account, q) {
				return
			}
			continue
		}
		if class == Admission && len(batch) > 1 && len(batch) < admissionBatchMax {
			batch = f.collect(q, batch, class)
		}
		if batch = dropCancelled(batch); len(batch) == 0 {
			continue
		}
		f.runBatch(class, batch)
	}
}

// dropCancelled answers admissions whose client left while they waited, without writing for them. A completion is
// never dropped: the money it carries is already committed, so it is settled even when its caller is gone.
func dropCancelled(batch []*gateItem) []*gateItem {
	live := batch[:0]
	for _, item := range batch {
		if item.class != Admission {
			live = append(live, item)
			continue
		}
		if err := item.ctx.Err(); err != nil {
			item.err = err
			close(item.done)
			continue
		}
		live = append(live, item)
	}
	return live
}

// take removes one batch, completions first, and marks its items as taken.
func (f *Funds) take(q *accountQueue) ([]*gateItem, Class) {
	f.gate.mu.Lock()
	defer f.gate.mu.Unlock()
	class, limit := Completion, completionBatchMax
	if q.completion.Len() == 0 && q.admission.Len() > 0 {
		class, limit = Admission, admissionBatchMax
	}
	queue := q.of(class)
	batch := make([]*gateItem, 0, min(limit, queue.Len()))
	for len(batch) < limit {
		e := queue.Front()
		if e == nil {
			break
		}
		item := queue.Remove(e).(*gateItem)
		item.elem, item.taken = nil, true
		batch = append(batch, item)
	}
	if len(batch) > 0 {
		q.since = time.Now()
	}
	return batch, class
}

// collect waits out the rest of the batch's bounded collection time and takes whatever arrived, up to the class's
// limit. It runs without the lock, so submissions are not held up while it waits.
func (f *Funds) collect(q *accountQueue, batch []*gateItem, class Class) []*gateItem {
	for len(batch) < admissionBatchMax {
		f.gate.mu.Lock()
		remain := time.Until(q.since.Add(admissionCollect))
		f.gate.mu.Unlock()
		if remain <= 0 {
			break
		}
		timer := time.NewTimer(remain)
		<-timer.C
		f.gate.mu.Lock()
		queue := q.of(class)
		for len(batch) < admissionBatchMax {
			e := queue.Front()
			if e == nil {
				break
			}
			item := queue.Remove(e).(*gateItem)
			item.elem, item.taken = nil, true
			batch = append(batch, item)
		}
		f.gate.mu.Unlock()
	}
	return batch
}

// close ends the leadership when no waiter is left. It holds the lock across the check and the delete, so a request
// that arrives just after sees an idle account and leads its own batch.
func (f *Funds) close(account string, q *accountQueue) bool {
	f.gate.mu.Lock()
	defer f.gate.mu.Unlock()
	if !q.empty() {
		return false
	}
	delete(f.gate.accounts, account)
	q.leader = false
	return true
}

// runBatch adds the class's context, takes one connection, runs the batch, and gives every item its result. The
// connection is per batch, not per request: a batch of thirty admissions holds one slot, not thirty.
func (f *Funds) runBatch(class Class, batch []*gateItem) {
	ctx, cancel := batchContext(batch, class, f.budget)
	defer cancel()
	release, err := f.pool.Acquire(ctx, class)
	if err != nil {
		f.finish(batch, err)
		return
	}
	var panicked any
	func() {
		defer release()
		defer func() { panicked = recover() }()
		err = batch[0].run(ctx, batch)
	}()
	if panicked != nil {
		// A panic is the leader's caller's to observe, as it was when the caller served the queue itself; every
		// waiter in the batch sees it in its own goroutine. The connection is released above either way.
		for _, item := range batch {
			item.panicked = panicked
		}
		f.finish(batch, nil)
		return
	}
	f.finish(batch, err)
}

// batchContext is one context for the batch: its first item's values, the admission budget or the earliest
// completion deadline, and no cancellation from the client, because the rest of the batch still has to finish.
func batchContext(batch []*gateItem, class Class, budget time.Duration) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(batch[0].ctx)
	if class == Admission {
		return context.WithTimeout(ctx, budget)
	}
	ctx = pg.CompletionContext(ctx)
	var earliest time.Time
	for _, item := range batch {
		if d, ok := item.ctx.Deadline(); ok && (earliest.IsZero() || d.Before(earliest)) {
			earliest = d
		}
	}
	if earliest.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, earliest)
}

// finish gives every item its result and wakes its waiter. An item whose run wrote its own error keeps it; the
// batch's error covers the rest, so a failure before a request's work never leaves that request waiting.
func (f *Funds) finish(batch []*gateItem, err error) {
	for _, item := range batch {
		if item.closed {
			continue
		}
		if item.err == nil {
			item.err = err
		}
		item.closed = true
		close(item.done)
	}
}
