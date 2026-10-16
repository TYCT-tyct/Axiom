package accounts

import (
	"container/list"
	"sync"
	"time"
)

// lru is a bounded map that drops its least recently used entry when full; an entry may also expire. Safe for
// concurrent use.
type lru[K comparable, V any] struct {
	mu      sync.Mutex
	limit   int
	order   *list.List // front is the most recently used
	entries map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key     K
	value   V
	expires time.Time // zero for never
}

func newLRU[K comparable, V any](limit int) *lru[K, V] {
	return &lru[K, V]{limit: limit, order: list.New(), entries: make(map[K]*list.Element)}
}

// get returns the value for key unless it is missing or expired at now.
func (c *lru[K, V]) get(key K, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	entry := element.Value.(*lruEntry[K, V])
	if !entry.expires.IsZero() && !now.Before(entry.expires) {
		c.order.Remove(element)
		delete(c.entries, key)
		var zero V
		return zero, false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

// put stores value for key until expires, or for good when it is zero.
func (c *lru[K, V]) put(key K, value V, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value = &lruEntry[K, V]{key, value, expires}
		c.order.MoveToFront(element)
		return
	}
	c.entries[key] = c.order.PushFront(&lruEntry[K, V]{key, value, expires})
	if c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*lruEntry[K, V]).key)
	}
}
