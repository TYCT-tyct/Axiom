package adapter_test

import (
	"slices"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// TestNoPassAsksForMoreThanADay: a site may refuse a log search longer than a day (new-api's log_setting.max_range_days
// at its least), so no pass spans more, also when the scan ran late after days away; and the passes, forward and late,
// still leave no second unread between the connection and where they stand.
func TestNoPassAsksForMoreThanADay(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_800_000_000, 0)
	s := adapter.NewWindowScan(start)
	var forward, late [][2]int64
	now := start
	step := func(d time.Duration) {
		now = now.Add(d)
		p := s.Next(now)
		if p == nil {
			return
		}
		if p.End-p.Start > int64(24*time.Hour/time.Second) {
			t.Fatalf("at %s a pass asks for %s", now.Sub(start), time.Duration(p.End-p.Start)*time.Second)
		}
		if p.Late {
			late = append(late, [2]int64{p.Start, p.End})
		} else {
			forward = append(forward, [2]int64{p.Start, p.End})
		}
		s.Pass = p
		p.Pages = 1
		s = s.Finish(now)
	}
	for range 3 * 24 * 30 { // three days of rounds two minutes apart
		step(2 * time.Minute)
	}
	step(3 * 24 * time.Hour) // a node away for three days
	for range 2 * 24 * 30 {
		step(2 * time.Minute)
	}
	if len(late) < 4 {
		t.Fatalf("%d late passes in eight days", len(late))
	}
	checkCovered(t, "forward", forward, start.Unix(), s.Watermark)
	checkCovered(t, "late", late, start.Unix(), late[len(late)-1][1])
}

// checkCovered fails unless the passes, together, cover every second from from to to.
func checkCovered(t *testing.T, name string, passes [][2]int64, from, to int64) {
	t.Helper()
	slices.SortFunc(passes, func(a, b [2]int64) int { return int(a[0] - b[0]) })
	reached := from
	for _, p := range passes {
		if p[0] > reached {
			t.Fatalf("%s passes leave %d to %d unread", name, reached, p[0])
		}
		reached = max(reached, p[1])
	}
	if reached < to {
		t.Fatalf("%s passes reach %d, short of %d", name, reached, to)
	}
}
