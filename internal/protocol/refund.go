package protocol

// refund gives back n bytes a piece no longer holds, keeping charged equal to what held has above the free allowance.
func (r *Relay) refund(n int) {
	if n <= 0 {
		return
	}
	r.held = max(0, r.held-int64(n))
	want := max(0, r.held-r.free)
	if want < r.charged {
		r.o.Memory.Refund(r.charged - want)
		r.charged = want
	}
}
