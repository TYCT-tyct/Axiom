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
// of the supplier, and the tasks of the round after it must be able to open again then.
//
// It does not forward adapter.Rescanner, which a removal asks the Manager for; a removal opens the adapter before it
// begins.
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

func (o *openOnUse) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	m, err := o.get()
	if err != nil {
		return adapter.ChargePage{}, nil, err
	}
	return m.Charges(ctx, scan)
}
