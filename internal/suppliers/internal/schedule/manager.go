package schedule

import (
	"context"
	"errors"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// openOnUse is an adapter.Manager that opens the adapter's the first time a task asks for it, and keeps what came of it
// for the rest of the round. Opening logs in at the supplier, and it can fail like any request of the engine's tasks
// (the account is rejected, the supplier is down): by failing inside a task it is recorded as the task's failure, with
// the backoff or the stop that goes with it, instead of ending the round before the engine has seen it. One login a
// round, not one for each task. A refusal for want of allowance (adapter.ErrBucketsFull) is not kept: it asked nothing
// of the supplier, and later tasks of the round may open it when allowance is available.
type openOnUse struct {
	open    func() (adapter.Manager, error)
	opened  bool
	manager adapter.Manager
	err     error
}

func (o *openOnUse) get() (adapter.Manager, error) {
	if !o.opened {
		o.manager, o.err = o.open()
		o.opened = !errors.Is(o.err, adapter.ErrBucketsFull)
	}
	return o.manager, o.err
}

func (o *openOnUse) Account(ctx context.Context) (adapter.Account, error) {
	m, err := o.get()
	if err != nil {
		return adapter.Account{}, err
	}
	return m.Account(ctx)
}

func (o *openOnUse) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	m, err := o.get()
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	return m.Offer(ctx, from)
}

func (o *openOnUse) Keys(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	m, err := o.get()
	if err != nil {
		return adapter.Page[adapter.KeyState]{}, err
	}
	return m.Keys(ctx, from)
}

func (o *openOnUse) KeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	m, err := o.get()
	if err != nil {
		return secret.Secret{}, err
	}
	return m.KeySecret(ctx, ref)
}

// HasUsageQueue implements adapter.UsageQueueManager. It opens the manager, because only the manager knows whether its
// charges are a queue that is consumed once; the sync engine then skips Charges for it.
func (o *openOnUse) HasUsageQueue(ctx context.Context) (bool, error) {
	m, err := o.get()
	if err != nil {
		return false, err
	}
	_, ok := m.(adapter.UsageQueue)
	return ok, nil
}

func (o *openOnUse) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	m, err := o.get()
	if err != nil {
		return adapter.ChargePage{}, nil, err
	}
	return m.Charges(ctx, scan)
}

// CreateKey forwards key creation to the adapter that has one. A management side without the optional half answers
// ErrNoKeyMaker: the group stays pending rather than failing, and an adapter that declares it can serve a group with a
// key is expected to implement it.
func (o *openOnUse) CreateKey(ctx context.Context, groupRef, name string) (adapter.KeyRef, error) {
	m, err := o.get()
	if err != nil {
		return "", err
	}
	maker, ok := m.(adapter.KeyMaker)
	if !ok {
		return "", adapter.ErrNoKeyMaker
	}
	return maker.CreateKey(ctx, groupRef, name)
}

// Pool forwards the pool read to the adapter that has one. A relay's manager does not, and answers ErrNotPool; that is
// how the engine tells a supplier whose group weights it may lower from one whose capacity is its own.
func (o *openOnUse) Pool(ctx context.Context) (adapter.PoolReading, error) {
	m, err := o.get()
	if err != nil {
		return adapter.PoolReading{}, err
	}
	p, ok := m.(adapter.Pool)
	if !ok {
		return adapter.PoolReading{}, adapter.ErrNotPool
	}
	return p.Pool(ctx)
}
