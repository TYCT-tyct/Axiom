package gateway

import (
	"context"
	"sync"
)

// Class says what a database connection is wanted for.
type Class int

// The classes share an account gate but use separate connection pools.
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
	classes [classCount]poolClass
}

// Each class counts only its own permits: completions must not consume admission capacity in the other physical pool.
type poolClass struct {
	used    int
	ceiling int
	waiting waitList
}

func newPool(size, reserve int) *Pool {
	return &Pool{classes: [classCount]poolClass{
		Completion: {ceiling: reserve},
		Admission:  {ceiling: size - reserve},
	}}
}

// Acquire waits, first in first out within the class, until a connection may be used, and returns the function that
// gives it back. The wait is bounded by ctx alone: the admission transaction's 1s budget is the caller's deadline.
// A dead ctx never keeps a connection.
func (p *Pool) Acquire(ctx context.Context, class Class) (release func(), err error) {
	p.mu.Lock()
	wt := p.classes[class].waiting.push("")
	p.classes[class].dispatch()
	p.mu.Unlock()

	if !wt.park(ctx, &p.mu) {
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		p.release(class)
		return nil, err
	}
	return sync.OnceFunc(func() { p.release(class) }), nil
}

func (p *Pool) release(class Class) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := &p.classes[class]
	c.used--
	c.dispatch()
}

// dispatch runs with Pool.mu held.
func (c *poolClass) dispatch() {
	for c.used < c.ceiling && c.waiting.grant() {
		c.used++
	}
}
