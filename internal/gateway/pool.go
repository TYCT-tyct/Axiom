package gateway

import (
	"context"
	"sync"
)

// Class says what a database connection is wanted for.
type Class int

// The classes, in the order Pool grants them.
const (
	// Completion is extension, settlement and SettleOrphan: money already committed to, so it may use every
	// connection.
	Completion Class = iota
	// CallRecords is the batch writer of call records.
	CallRecords
	// Journal is the batch writer of the reconciliation journal.
	Journal
	// Admission is admission, cold-key lookup and the read-only key endpoints.
	Admission
	classCount
)

// Pool divides the N connections of the database pool among the classes. Only Completion may take the last
// Reserve connections, so settlement and extension always find one however many admissions, cold-key lookups and
// batch writes are waiting. A waiter holds nothing, so waiting costs no connection.
type Pool struct {
	mu      sync.Mutex
	used    int
	classes [classCount]poolClass
}

// poolClass grants while the pool's total in use is below ceiling and the class's own count is below max.
type poolClass struct {
	ceiling, max, used int
	waiting            waitList
}

func newPool(size, reserve, batch int) *Pool {
	return &Pool{classes: [classCount]poolClass{
		Completion:  {ceiling: size, max: size},
		CallRecords: {ceiling: size - reserve, max: batch},
		Journal:     {ceiling: size - reserve, max: batch},
		Admission:   {ceiling: size - reserve, max: size},
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
		p.release(class)
		return nil, err
	}
	return sync.OnceFunc(func() { p.release(class) }), nil
}

func (p *Pool) release(class Class) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used--
	p.classes[class].used--
	p.dispatch()
}

// dispatch grants every waiter that may run now. Grants only raise the total, so one pass is enough.
func (p *Pool) dispatch() {
	for i := range p.classes {
		c := &p.classes[i]
		for p.used < c.ceiling && c.used < c.max && c.waiting.grant() {
			p.used++
			c.used++
		}
	}
}
