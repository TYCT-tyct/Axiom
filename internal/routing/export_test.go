package routing

// StickyLen is how many sessions the router remembers.
func (r *Router) StickyLen() (n int) {
	for i := range r.sticky.shards {
		sh := &r.sticky.shards[i]
		sh.mu.Lock()
		n += len(sh.m)
		sh.mu.Unlock()
	}
	return n
}
