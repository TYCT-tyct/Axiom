package billing

import "fmt"

// refund applies the cumulative revision at this point in history, never today's freeze to an older charge.
func (a *allocation) refund(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || b.source != "paid" || b.opened.compare(a.at) > 0 {
		return fmt.Errorf("billing: %s transaction %s names no open paid batch %s", m.kind, m.key.id, m.request)
	}
	total := b.refundTotal(m.kind)
	previous, limit, debit := *total, b.frozen(), int64(0)
	if m.kind == "freeze" {
		limit = b.available()
	}
	if m.kind == "refund" {
		debit = m.revision - previous
	}
	if m.revision <= previous || m.revision-previous > limit || m.debit != debit {
		return fmt.Errorf("billing: %s transaction %s at %d does not fit batch %s", m.kind, m.key.id, m.revision, b.id)
	}
	*total = m.revision
	if m.kind == "unfreeze" {
		a.payDebt(b, m.revision-previous)
	}
	b.touched = true
	a.dropSpent()
	return nil
}

// refundTotal selects the cumulative ledger revision shared by posting and replay.
func (b *batchState) refundTotal(kind string) *int64 {
	switch kind {
	case "freeze":
		return &b.freezeTotal
	case "unfreeze":
		return &b.unfreezeTotal
	default:
		return &b.refunded
	}
}
