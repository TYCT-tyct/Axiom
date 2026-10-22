package kv

import (
	"context"
	"sync"
	"time"
	"uuid"
)

const (
	laterCapacity     = 4096
	stickyOutstanding = 32
	accountCapacity   = 65536
	writeInterval     = time.Second / 10
	writeBurst        = 100
)

// writeAccounts bounds both the writes one account can create and the memory used to remember accounts. A full table
// refuses new accounts until an idle bucket expires; evicting a busy bucket would give it another burst.
type writeAccounts struct {
	mu     sync.Mutex
	fullAt map[uuid.UUID]time.Time
}

func (a *writeAccounts) allow(account uuid.UUID, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, exists := a.fullAt[account]
	if !exists && len(a.fullAt) >= accountCapacity {
		return false
	}
	if at.Before(now) {
		at = now
	}
	if at.Sub(now) > (writeBurst-1)*writeInterval {
		return false
	}
	a.fullAt[account] = at.Add(writeInterval)
	return true
}

func (a *writeAccounts) prune(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for account, at := range a.fullAt {
		if !at.After(now) {
			delete(a.fullAt, account)
		}
	}
}

// Later offers a sticky write without waiting for the server or queue space. Only bounded SET commands can enter:
// scripts may reference caller-owned slices, whereas these commands own immutable strings and always have a TTL.
func (c *Client) Later(account uuid.UUID, cmd Cmd) {
	if cmd.kind != setCmd {
		panic("kv: Later only accepts expiring bindings")
	}
	if c.later == nil {
		return
	}
	c.laterMu.RLock()
	defer c.laterMu.RUnlock()
	if c.closed {
		c.m.drop(context.Background(), dropShutdown, 1)
		return
	}
	switch {
	case c.memoryStop.Load():
		c.m.drop(context.Background(), dropMemory, 1)
	case !c.accounts.allow(account, c.now()):
		c.m.drop(context.Background(), dropAccount, 1)
	default:
		select {
		case c.later <- cmd:
			return
		default:
			c.m.drop(context.Background(), dropQueue, 1)
		}
	}
}

// writeLater gives up immediately on busy slots; it must never queue ahead of foreground accesses.
// An unavailable write is discarded, never retried: a stale retry could replace a newer binding from another node.
func (c *Client) writeLater(ctx context.Context) {
	defer func() {
		var pending int64
		for {
			select {
			case <-c.later:
				pending++
			default:
				if pending > 0 {
					c.m.drop(context.WithoutCancel(ctx), dropShutdown, pending)
				}
				return
			}
		}
	}()
	var batch [stickyOutstanding]Cmd
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case batch[0] = <-c.later:
		}
		n := 1
	drain:
		for n < len(batch) {
			select {
			case batch[n] = <-c.later:
				n++
			default:
				break drain
			}
		}
		if ctx.Err() != nil {
			c.m.drop(context.WithoutCancel(ctx), dropShutdown, int64(n))
		} else if c.memoryStop.Load() {
			c.m.drop(ctx, dropMemory, int64(n))
		} else {
			_, _ = c.Do(ctx, StickyWrite, batch[:n]...) //nolint:errcheck // Do records failures; this disposable write has no caller to fail.
		}
		clear(batch[:n])
	}
}

func (c *Client) checkMemory(mem memory) {
	// Division first keeps even a malformed server's largest int64 from overflowing.
	ratio := float64(mem.used) / float64(mem.max)
	if mem.max <= 0 || ratio > .7 {
		c.memoryStop.Store(true)
	} else if ratio < .6 {
		c.memoryStop.Store(false)
	}
}
