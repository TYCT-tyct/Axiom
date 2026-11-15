package observability

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
)

func TestOwnAttemptDetails(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	for _, tc := range []struct {
		name, route string
		attempts    int
		wantRoute   *OwnRoute
	}{
		{"fallback", `{"preset":{"id":"p","version":3},"excluded":[{"endpoint":"private","reason":"capacity"}],"excluded_more":19,"excluded_reasons":{"capacity":20},"route":["selected"],"fastest_unmeasured":true,"preset_unavailable":true,"credentials":"secret"}`, 3, &OwnRoute{Preset: &OwnRoutePreset{ID: "p", Version: 3}, ExcludedReasons: map[string]int{"capacity": 20}, ExcludedMore: 19, SelectedEndpoint: "selected", FastestUnmeasured: true, PresetUnavailable: true}},
		{"legacy", `{}`, 0, nil},
		{"legacy preset", `{"preset":"p1","excluded":[{"reason":"capacity"}]}`, 2, &OwnRoute{ExcludedReasons: map[string]int{"capacity": 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := record(account, now)
			rec.Route = []byte(tc.route)
			if tc.attempts == 0 {
				rec.Attempts = nil
			} else {
				rec = withAttempts(rec, tc.attempts)
			}
			if tc.attempts == 3 {
				rec.Attempts[1].Outcome, rec.Attempts[1].ErrorKind = AttemptFailed, "timeout"
			}
			keep(t, db, rec)
			checkOwnAttemptDetail(t, db, rec, tc.wantRoute)
		})
	}
}

func checkOwnAttemptDetail(t *testing.T, db *pg.DB, rec CallRecord, wantRoute *OwnRoute) {
	t.Helper()
	var got OwnCall
	err := db.ReadTx(t.Context(), accountScope(rec.Account), func(tx pg.Tx) (err error) { got, err = GetOwn(t.Context(), tx, rec.ID); return err })
	if err != nil {
		t.Fatal(err)
	}
	wantAttempts := make([]OwnAttempt, len(rec.Attempts))
	for i, a := range rec.Attempts {
		wantAttempts[i] = OwnAttempt{
			Sequence: i + 1, Endpoint: a.Endpoint.String(), SourceClass: a.SourceClass,
			StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, FirstTokenMs: int(a.FirstToken.Milliseconds()), Result: string(a.Outcome), ErrorKind: a.ErrorKind,
		}
	}
	if diff := cmp.Diff(wantAttempts, got.Attempts); diff != "" {
		t.Fatalf("attempts (-want +got): %s", diff)
	}
	if got.AttemptCount != len(rec.Attempts) {
		t.Fatalf("count: %d", got.AttemptCount)
	}
	if diff := cmp.Diff(wantRoute, got.Route); diff != "" {
		t.Fatalf("route (-want +got): %s", diff)
	}
	err = db.ReadTx(t.Context(), accountScope(uuid.NewV7()), func(tx pg.Tx) error { _, err := GetOwn(t.Context(), tx, rec.ID); return err })
	if !errors.Is(err, pg.ErrNoRows) {
		t.Fatalf("foreign detail: %v", err)
	}
}

func TestOwnFallbackPaginationAndPlan(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	var records []CallRecord
	var want []uuid.UUID
	for i := range 110 {
		rec := record(account, now.Add(-time.Duration(i)*time.Second))
		if i%2 == 0 {
			rec.Attempts = rec.Attempts[:1]
		} else {
			want = append(want, rec.ID)
		}
		records = append(records, rec)
	}
	keep(t, db, records...)
	q := day(account)
	q.FellBack, q.PageSize = true, 50
	calls := allPages(t, db, q)
	var got []uuid.UUID
	for _, c := range calls {
		got = append(got, c.ID)
		if c.AttemptCount != 2 || c.Attempts != nil || c.Route != nil {
			t.Fatalf("list detail: %+v", c)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged IDs: %v, want %v", got, want)
	}
	plan := explain(t, db, q, accountScope(account))
	if strings.Contains(plan, "call_attempt") || strings.Contains(plan, "SubPlan") {
		t.Fatalf("per-row attempt query: %s", plan)
	}
	q.FellBack = false
	if got := allPages(t, db, q); len(got) != 110 {
		t.Fatalf("unfiltered: %d", len(got))
	}
}

func TestOwnRouteRejectsMalformedRecord(t *testing.T) {
	if _, err := ownRoute(`{"preset":{"version":"bad"}}`); err == nil {
		t.Fatal("malformed preset accepted")
	}
	if _, err := ownRoute(`{"excluded_more":"bad"}`); err == nil {
		t.Fatal("malformed count accepted")
	}
}
