package gateway

import (
	"context"
	"sync"
	"time"
)

// accountGate lets one funds transaction per account run at a time. The row lock on the balance does that in the
// database too, but a transaction waiting for it holds a connection; waiting here holds none, so a hot account
// queues in memory and cannot fill the pool for the other tenants.
//
// An account is in the map exactly while its gate is held. Releasing hands the gate straight to the oldest waiter,
// completion class before admission class: a settlement that is waiting behind admissions would keep the
// reservation, and the in-flight slot it pins, for longer.
type accountGate struct {
	mu            sync.Mutex
	admissionWait time.Duration
	accounts      map[string]*gateWaiters
}

type gateWaiters struct{ completion, admission waitList }

func newAccountGate(admissionWait time.Duration) *accountGate {
	return &accountGate{admissionWait: admissionWait, accounts: map[string]*gateWaiters{}}
}

// enter takes the gate of account. An admission waits at most admissionWait and then gets errAccountGate, so a
// client that hammers one account is refused rather than left holding a request open; a completion waits for as
// long as ctx lets it, since it carries money that is already committed.
func (g *accountGate) enter(parent context.Context, account string, class Class) (func(), error) {
	completion := class == Completion
	g.mu.Lock()
	waiters, held := g.accounts[account]
	if !held {
		g.accounts[account] = &gateWaiters{}
		g.mu.Unlock()
		return g.releaser(account), nil
	}
	queue := &waiters.admission
	ctx := parent
	if completion {
		queue = &waiters.completion
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, g.admissionWait)
		defer cancel()
	}
	wt := queue.push("")
	g.mu.Unlock()

	if !wt.park(ctx, &g.mu) {
		return nil, expired(parent, errAccountGate)
	}
	if ctx.Err() != nil {
		g.release(account)
		return nil, expired(parent, errAccountGate)
	}
	return g.releaser(account), nil
}

func (g *accountGate) releaser(account string) func() {
	return sync.OnceFunc(func() { g.release(account) })
}

func (g *accountGate) release(account string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	waiters := g.accounts[account]
	if waiters.completion.grant() || waiters.admission.grant() {
		return
	}
	delete(g.accounts, account)
}

// Funds starts the transactions that move money: admission, extension, settlement and SettleOrphan. The order is
// the account gate first and the connection second, so a transaction waiting for its account holds no connection.
type Funds struct {
	gate *accountGate
	pool *Pool
}

// Admit is for the admission transaction (class Admission).
func (f *Funds) Admit(ctx context.Context, account string) (release func(), err error) {
	return f.begin(ctx, account, Admission)
}

// Complete is for extension, settlement and SettleOrphan (class Completion): ahead of admissions at the gate, and
// able to use the connections admissions may not.
func (f *Funds) Complete(ctx context.Context, account string) (release func(), err error) {
	return f.begin(ctx, account, Completion)
}

func (f *Funds) begin(ctx context.Context, account string, class Class) (func(), error) {
	releaseGate, err := f.gate.enter(ctx, account, class)
	if err != nil {
		return nil, err
	}
	releaseConn, err := f.pool.Acquire(ctx, class)
	if err != nil {
		releaseGate()
		return nil, err
	}
	return func() {
		releaseConn()
		releaseGate()
	}, nil
}
