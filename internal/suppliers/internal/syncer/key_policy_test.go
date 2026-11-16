package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/suppliers/internal/adapter"
)

type policyStore struct {
	*memStore
	keys *[]adapter.KeyState
}

func (s *policyStore) Commit(ctx context.Context, expect int64, c Commit) error {
	s.keys = c.KeyPolicies
	return s.memStore.Commit(ctx, expect, c)
}

func TestKeyPoliciesCommitWithTheCompletedListing(t *testing.T) {
	t.Parallel()
	keys := []adapter.KeyState{{Ref: "7", Status: adapter.KeyActive, Policy: &adapter.KeyPolicy{Models: []string{"upstream"}}}}
	m := healthy()
	m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return adapter.Page[adapter.KeyState]{Items: keys, Done: true}, nil
	}
	_, base, clk := engine(m)
	for task := range base.state.Tasks {
		if Task(task) != TaskKeys {
			base.state.Tasks[task].Due = t0.Add(time.Hour)
		}
	}
	st := &policyStore{memStore: base}
	round(t, New(st, m, Defaults(), clk.now))
	if st.keys == nil || !cmp.Equal(*st.keys, keys) {
		t.Fatalf("complete policies not committed: %v", st.keys)
	}
}
