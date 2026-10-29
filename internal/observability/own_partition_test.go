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
	workspace, key := uuid.NewV7(), uuid.NewV7()
	var records []CallRecord
	for _, at := range []time.Time{since.Add(-time.Microsecond), since, since.Add(time.Microsecond), until.Add(-time.Hour), until} {
		for range 2 {
			r := record(account, at)
			r.Workspace, r.Key = workspace, key
			records = append(records, r)
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
	for _, filter := range []OwnQuery{
		{},
		{Model: "claude-opus"},
		{ClientApp: "claude-code"},
		{Model: "claude-opus", ClientApp: "claude-code"},
		{Key: &key},
		{Workspace: &workspace},
		{Status: "error"},
		{Key: &key, Workspace: &workspace, Status: "error"},
	} {
		q := filter
		q.Account, q.Since, q.Until, q.PageSize = account, since, until, 1
		var got []uuid.UUID
		for _, call := range allPages(t, db, q) {
			got = append(got, call.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("filter %+v: pages read %v, want %v", filter, got, want)
		}
	}
}
