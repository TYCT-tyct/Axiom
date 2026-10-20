package gateway

import (
	"context"
	"sync"
)

// Class says what a database connection is wanted for.
type Class int

// The classes, in the order Pool grants them.
const (
	// Completion is extension, settlement and SettleOrphan: money already committed to, so it has priority.
	Completion Class = iota
	// Admission is admission, cold-key lookup and the read-only key endpoints.
	Admission
	classCount
)

// Pool schedules gateway work before it reaches pg. The physical reserve is enforced by pg, including for callers
// outside the gateway: a permit alone cannot protect connections from background work.
type Pool struct {
	mu      sync.Mutex
	used    int
	classes [classCount]poolClass
}

// poolClass grants while the pool's total in use is below ceiling.
type poolClass struct {
	ceiling int
	waiting waitList
}

func newPool(size, reserve int) *Pool {
	return &Pool{classes: [classCount]poolClass{
		Completion: {ceiling: size},
		Admission:  {ceiling: size - reserve},
	}}
}

// Acquire waits, first in first out within the class, until a connection may be used, and returns the function that
// gives it back. The wait is bounded by ctx alone: the admission transaction's 1s budget is the caller's deadline.
// A dead ctx never keeps a connection.
func (p *Pool) Acquire(ctx context.Context, class Class) (release func(), err error) {
	p.mu.Lock()
	wt := p.classes[class].waiting.push("")
	p.dispatch()
	p.mu.Unlock()

	if !wt.park(ctx, &p.mu) {
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		p.release()
		return nil, err
	}
	return sync.OnceFunc(p.release), nil
}

func (p *Pool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used--
	p.dispatch()
}

// dispatch grants every waiter that may run now. Grants only raise the total, so one pass is enough.
func (p *Pool) dispatch() {
	for i := range p.classes {
		c := &p.classes[i]
		for p.used < c.ceiling && c.waiting.grant() {
			p.used++
		}
	}
}
