package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/suppliers/internal/adapter"
)

// poolSupplier is a supplier with a pool half: the embedded manager serves the balance and the listing, and Pool answers
// what the test wants the pool's management API to say.
type poolSupplier struct {
	*supplier
	pool func(context.Context) (adapter.PoolReading, error)
}

func (p *poolSupplier) Pool(ctx context.Context) (adapter.PoolReading, error) { return p.pool(ctx) }

// TestPoolCapacityTracksTheReading: the step carries what the pool said, with the capacity derived from it, so the
// projection can write it and compare rows; half the accounts failed is low on its own, a supplier that stops being a
// pool keeps what it had, and a group that leaves the answer disappears.
func TestPoolCapacityTracksTheReading(t *testing.T) {
	t.Parallel()
	head := func(v int) *int { return &v }
	cases := []struct {
		name     string
		caps     []adapter.GroupCapacity
		err      error
		groups   int
		capacity int
		low      bool
	}{
		{"at ten percent", []adapter.GroupCapacity{{GroupRef: "1", Accounts: 10, Usable: 10, Headroom: head(10)}}, nil, 1, 10, true},
		{"the same reading", []adapter.GroupCapacity{{GroupRef: "1", Accounts: 10, Usable: 10, Headroom: head(10)}}, nil, 1, 10, true},
		{"room back", []adapter.GroupCapacity{{GroupRef: "1", Accounts: 10, Usable: 10, Headroom: head(100)}}, nil, 1, 100, false},
		{"half the accounts failed", []adapter.GroupCapacity{{GroupRef: "1", Accounts: 10, Failed: 5, Usable: 5, Headroom: head(100)}}, nil, 1, 50, true},
		{"stopped being a pool", nil, adapter.ErrNotPool, 1, 50, true},
		{"left the answer", nil, nil, 0, 0, false},
	}
	m := &poolSupplier{supplier: healthy(), pool: func(context.Context) (adapter.PoolReading, error) {
		return adapter.PoolReading{}, nil
	}}
	e, st, clk := engine(m)
	for _, c := range cases {
		caps, poolErr := c.caps, c.err
		m.pool = func(context.Context) (adapter.PoolReading, error) {
			return adapter.PoolReading{Groups: caps, Complete: true}, poolErr
		}
		clk.advance(10 * time.Minute)
		round(t, e)
		p := st.state.Pool
		if len(p) != c.groups {
			t.Errorf("%s: pool state %+v, want %d groups", c.name, p, c.groups)
			continue
		}
		if c.groups == 0 {
			continue
		}
		if p[0].Low != c.low || p[0].Capacity == nil || *p[0].Capacity != c.capacity {
			t.Errorf("%s: pool state %+v, want capacity %d low %v", c.name, p[0], c.capacity, c.low)
		}
	}
}

// TestPoolCapacityArithmetic: the effective capacity is the room left times the share of accounts that can serve; a group
// with no window reading has no capacity, and is not guessed to be low.
func TestPoolCapacityArithmetic(t *testing.T) {
	t.Parallel()
	head := func(v int) *int { return &v }
	cases := map[string]struct {
		in       adapter.GroupCapacity
		capacity *int
		low      bool
	}{
		"at the line":            {adapter.GroupCapacity{GroupRef: "1", Accounts: 10, Usable: 10, Headroom: head(20)}, head(20), true},
		"above the line":         {adapter.GroupCapacity{GroupRef: "1", Accounts: 10, Usable: 10, Headroom: head(21)}, head(21), false},
		"half failed":            {adapter.GroupCapacity{GroupRef: "1", Accounts: 10, Failed: 5, Usable: 5, Headroom: head(100)}, head(50), true},
		"one of ten failed":      {adapter.GroupCapacity{GroupRef: "1", Accounts: 10, Failed: 1, Usable: 9, Headroom: head(100)}, head(90), false},
		"no window":              {adapter.GroupCapacity{GroupRef: "1", Accounts: 10, Usable: 10}, nil, false},
		"all failed, no window":  {adapter.GroupCapacity{GroupRef: "1", Accounts: 1, Failed: 1}, nil, true},
		"half failed, no window": {adapter.GroupCapacity{GroupRef: "1", Accounts: 2, Failed: 1, Usable: 1}, nil, true},
		"nothing can serve":      {adapter.GroupCapacity{GroupRef: "1", Accounts: 1, Failed: 1, Headroom: head(100)}, head(0), true},
	}
	for name, c := range cases {
		got := poolCapacity(c.in)
		if (got.Capacity == nil) != (c.capacity == nil) || (got.Capacity != nil && *got.Capacity != *c.capacity) || got.Low != c.low {
			t.Errorf("%s: %+v, want capacity %v low %v", name, got, c.capacity, c.low)
		}
	}
}

// TestATruncatedDirectoryKeepsThePreviousReading: a directory with more pages than one read may take is a prefix, so the
// groups it did not reach must keep their reading and their weight rather than look recovered.
func TestATruncatedDirectoryKeepsThePreviousReading(t *testing.T) {
	t.Parallel()
	head := 10
	m := &poolSupplier{supplier: healthy(), pool: func(context.Context) (adapter.PoolReading, error) {
		return adapter.PoolReading{Complete: false}, nil
	}}
	e, st, _ := engine(m)
	before := []PoolCapacity{{GroupRef: "1", Accounts: 10, Failed: 2, Usable: 8, Headroom: &head, Capacity: &head, Low: true}}
	st.state.Pool = before
	round(t, e)
	if diff := cmp.Diff(before, st.state.Pool); diff != "" {
		t.Errorf("a truncated directory changed the reading (-want +got):\n%s", diff)
	}
}

// TestAFailedOnlyGroupIsLowWithoutAWindow: the adapter reports a group whose only account failed with no headroom; the
// engine still calls it low, and leaves its capacity unknown. That is the shape the projection stores.
func TestAFailedOnlyGroupIsLowWithoutAWindow(t *testing.T) {
	t.Parallel()
	m := &poolSupplier{supplier: healthy(), pool: func(context.Context) (adapter.PoolReading, error) {
		return adapter.PoolReading{Groups: []adapter.GroupCapacity{{GroupRef: "1", Accounts: 1, Failed: 1}}, Complete: true}, nil
	}}
	e, st, _ := engine(m)
	round(t, e)
	p := st.state.Pool
	if len(p) != 1 || p[0].Capacity != nil || p[0].Headroom != nil || !p[0].Low {
		t.Fatalf("pool state %+v, want one low group with no capacity", p)
	}
}

// TestPoolReadFailureIsTheTaskFailure: a pool that cannot be read fails the catalog task, with the round's usual backoff,
// rather than leaving a stale reading looking current.
func TestPoolReadFailureIsTheTaskFailure(t *testing.T) {
	t.Parallel()
	m := &poolSupplier{supplier: healthy(), pool: func(context.Context) (adapter.PoolReading, error) {
		return adapter.PoolReading{}, errors.New("the pool is down")
	}}
	e, st, clk := engine(m)
	round(t, e)
	if st.state.Tasks[TaskCatalog].Failures != 1 {
		t.Fatalf("catalog failures %d, want 1", st.state.Tasks[TaskCatalog].Failures)
	}
	if !st.state.Tasks[TaskCatalog].BackoffUntil.After(t0) {
		t.Error("a failed pool read was retried at once")
	}
	clk.advance(30 * time.Second)
	round(t, e)
	if st.state.Tasks[TaskCatalog].Failures != 1 {
		t.Errorf("catalog failures %d after the backoff, want still 1", st.state.Tasks[TaskCatalog].Failures)
	}
}
