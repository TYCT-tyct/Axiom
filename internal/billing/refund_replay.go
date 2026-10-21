package billing

import "fmt"

// refund applies the cumulative revision at this point in history, never today's freeze to an older charge.
func (a *allocation) refund(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || b.source != "paid" || b.opened.compare(a.at) > 0 {
		return fmt.Errorf("billing: %s transaction %s names no open paid batch %s", m.kind, m.key.id, m.request)
	}
	var previous, limit, debit int64
	switch m.kind {
	case "freeze":
		previous, limit = b.freezeTotal, b.available()
	case "unfreeze":
		previous, limit = b.unfreezeTotal, b.frozen()
	case "refund":
		previous, limit = b.refunded, b.frozen()
		debit = m.revision - previous
	}
	if m.revision <= previous || m.revision-previous > limit || m.debit != debit {
		return fmt.Errorf("billing: %s transaction %s at %d does not fit batch %s", m.kind, m.key.id, m.revision, b.id)
	}
	switch m.kind {
	case "freeze":
		b.freezeTotal = m.revision
	case "unfreeze":
		b.unfreezeTotal = m.revision
		a.payDebt(b, m.revision-previous)
	case "refund":
		b.refunded = m.revision
	}
	b.touched = true
	a.dropSpent()
	return nil
}
