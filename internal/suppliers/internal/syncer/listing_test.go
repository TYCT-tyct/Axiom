package syncer

import (
	"bytes"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

func TestListingProgressSurvivesANewEngine(t *testing.T) {
	t.Parallel()
	for _, task := range []Task{TaskCatalog, TaskKeys} {
		t.Run(task.String(), func(t *testing.T) {
			t.Parallel()
			listingProgress(t, task)
		})
	}
}

func listingProgress(t *testing.T, task Task) {
	t.Helper()
	m := healthy()
	position := adapter.Listing(`{"page":2,"items":["first"]}`)
	until := t0.Add(3 * time.Minute)
	if task == TaskKeys {
		m.keys = func(adapter.Listing) (adapter.Page[adapter.KeyState], error) {
			return adapter.Page[adapter.KeyState]{Next: position, Until: until}, nil
		}
	} else {
		m.offer = func(adapter.Listing) (adapter.Page[adapter.Group], error) {
			return adapter.Page[adapter.Group]{Next: position, Until: until}, nil
		}
	}
	e, st, clk := engine(m)
	for other := range st.state.Tasks {
		if Task(other) != task {
			st.state.Tasks[other].Due = t0.Add(time.Hour)
		}
	}
	oldKey := adapter.KeyState{Ref: "old", Status: adapter.KeyActive}
	st.state.Keys = []adapter.KeyState{oldKey}
	st.state.Groups = []GroupSnap{{Ref: "old"}}
	round(t, e)
	ts := st.state.Tasks[task]
	if ts.Failures != 0 || !ts.LastOK.IsZero() || !ts.Due.Equal(until) {
		t.Fatalf("interrupted task: %+v", ts)
	}
	if st.state.Keys[0] != oldKey || st.state.Groups[0].Ref != "old" {
		t.Fatal("an incomplete listing replaced the previous snapshot")
	}
	if st.count(KeyChanged)+st.count(GroupRemoved) != 0 {
		t.Fatal("an incomplete listing emitted removals")
	}
	resumed := healthy()
	check := func(from adapter.Listing) {
		t.Helper()
		if !bytes.Equal(from, position) {
			t.Fatalf("resumed at %q, want %q", from, position)
		}
	}
	if task == TaskKeys {
		resumed.keys = func(from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
			check(from)
			return adapter.Page[adapter.KeyState]{Done: true, Items: []adapter.KeyState{{Ref: "first"}, {Ref: "second"}}}, nil
		}
	} else {
		resumed.offer = func(from adapter.Listing) (adapter.Page[adapter.Group], error) {
			check(from)
			return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{Ref: "first", Ratio: "1"}, {Ref: "second", Ratio: "1"}}}, nil
		}
	}
	clk.advance(3 * time.Minute)
	round(t, New(st, resumed, Defaults(), clk.now))
	assertCompletedTask(t, st.state, task, until)
}

func assertCompletedTask(t *testing.T, st State, task Task, at time.Time) {
	t.Helper()
	if !st.Tasks[task].LastOK.Equal(at) {
		t.Fatal("the resumed task did not finish")
	}
	if task == TaskKeys {
		if len(st.KeysAt) != 0 || len(st.Keys) != 2 {
			t.Fatalf("keys: %v, position %q", st.Keys, st.KeysAt)
		}
	} else {
		if len(st.OfferAt) != 0 || len(st.Groups) != 2 {
			t.Fatalf("groups: %v, position %q", st.Groups, st.OfferAt)
		}
	}
}
