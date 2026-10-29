package observability

import (
	"bytes"
	"slices"
	"testing"
	"time"
	"uuid"
)

func TestListOwnPagesAcrossPartitions(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	since := time.Date(2026, 10, 31, 23, 59, 59, 999999000, time.UTC)
	until := time.Date(2090, 2, 1, 0, 0, 0, 0, time.UTC)
	var records []CallRecord
	for _, at := range []time.Time{since.Add(-time.Microsecond), since, since.Add(time.Microsecond), until.Add(-time.Hour), until} {
		for range 2 {
			records = append(records, record(account, at))
		}
	}
	keep(t, db, records...)
	slices.SortFunc(records, func(a, b CallRecord) int {
		if n := b.At.Compare(a.At); n != 0 {
			return n
		}
		return bytes.Compare(b.ID[:], a.ID[:])
	})
	var want []uuid.UUID
	for _, r := range records {
		if !r.At.Before(since) && r.At.Before(until) {
			want = append(want, r.ID)
		}
	}
	for _, filter := range []struct{ model, app string }{{}, {"claude-opus", ""}, {"", "claude-code"}, {"claude-opus", "claude-code"}} {
		q := OwnQuery{Account: account, Since: since, Until: until, Model: filter.model, ClientApp: filter.app, PageSize: 1}
		var got []uuid.UUID
		for _, call := range allPages(t, db, q) {
			got = append(got, call.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("model=%q app=%q: pages read %v, want %v", filter.model, filter.app, got, want)
		}
	}
}
