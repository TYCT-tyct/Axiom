package gateway

import "sync"

// Limiter counts what is held per key and refuses at a limit; it never queues. It serves the per-IP connection and
// in-flight limits, the per-account in-flight limit and, under one fixed key, the node's.
type Limiter struct {
	mu   sync.Mutex
	full error
	held map[string]int
}

// NewLimiter returns a Limiter whose refusals are full.
func NewLimiter(full *Refusal) *Limiter { return &Limiter{full: full, held: map[string]int{}} }

// Acquire takes one of the limit slots of key, or returns the Limiter's refusal. The returned release is safe to
// call more than once: a request has several exit paths (the recover path among them) and a second call must not
// free a slot that belongs to another request.
func (l *Limiter) Acquire(key string, limit int) (release func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] >= limit {
		return nil, l.full
	}
	l.held[key]++
	return sync.OnceFunc(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.held[key]--; l.held[key] == 0 {
			delete(l.held, key) // a flood of cold keys must not leave a map entry each
		}
	}), nil
}

// InFlight is the count of requests being served: per account, then for the node. A slot is held from the
// admission of the request to the end of its settlement, background retries included, and is released only when
// Settle has committed (then suppliers.Settled), or when the request was handed to the cleanup and its executor has
// stopped the upstream, will not retry the settlement and has flushed every journal row (suppliers.JournalFlushed).
// That keeps the unsettled requests, and the journal buffer behind them, within the node limit.
type InFlight struct {
	node, accounts *Limiter
	nodeMax        int
}

// Acquire takes the account's slot, then the node's. accountMax 0 means the default, a tenth of the node's slots; a
// larger or smaller number is the account's own setting. A full account is 429 and a full node is 503, and the node
// does not queue.
func (f *InFlight) Acquire(account string, accountMax int) (release func(), err error) {
	if accountMax == 0 {
		accountMax = max(1, f.nodeMax/10)
	}
	releaseAccount, err := f.accounts.Acquire(account, accountMax)
	if err != nil {
		return nil, err
	}
	releaseNode, err := f.node.Acquire("", f.nodeMax)
	if err != nil {
		releaseAccount()
		return nil, err
	}
	return func() {
		releaseNode()
		releaseAccount()
	}, nil
}
