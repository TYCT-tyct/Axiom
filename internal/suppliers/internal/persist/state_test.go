package persist

import (
	"reflect"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/syncer"
)

// stateFormat1 names every member the state has in format 1: the shape the version before this one reads. A member this
// program adds to the stored state without raising the format is one that version refuses, and the two run side by side
// during a rollout (CONTRIBUTING「发布与缓存」).
type stateFormat1 struct {
	Version     jsonx.Raw
	ConfigGen   jsonx.Raw
	Status      jsonx.Raw
	Reason      jsonx.Raw
	Tasks       jsonx.Raw
	Balance     jsonx.Raw
	BalanceAt   jsonx.Raw
	Funding     jsonx.Raw
	LowNotified jsonx.Raw
	Groups      jsonx.Raw
	Keys        jsonx.Raw
	Plans       jsonx.Raw
	Confirmed   jsonx.Raw
	Scan        jsonx.Raw
	ScanOpen    jsonx.Raw
	Removal     jsonx.Raw
	GraceUntil  jsonx.Raw
	RescanFrom  jsonx.Raw
	RemovalAt   jsonx.Raw
	Through     jsonx.Raw
}

// TestTheStoredStateIsWhatAnOlderNodeReads: the state this version writes has exactly the members format 1 has, so the
// version before this one loads it, and the state that version wrote loads here. The members of a removal are among them
// although the engine no longer takes one: a stored state has them.
func TestTheStoredStateIsWhatAnOlderNodeReads(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st := syncer.State{
		Version: 7, ConfigGen: 3, Status: syncer.Removing, Reason: "x",
		Tasks:       [4]syncer.TaskState{{Due: at, BackoffUntil: at, Failures: 1, LastOK: at, Halted: true}},
		Balance:     "12.5",
		BalanceAt:   at,
		Funding:     adapter.FundingWallet,
		LowNotified: true,
		Groups: []syncer.GroupSnap{{Ref: "1", Name: "g", Ratio: "1.5", Facts: "{}", Models: []syncer.ModelSnap{{
			Name: "m", Formats: []int{1}, Without: protocol.Images, Plan: "f",
		}}}},
		Keys:      []adapter.KeyState{{Ref: "7", GroupRef: "1", Status: adapter.KeyActive, ResetAt: at}},
		Plans:     map[string]string{"1/m": "f"},
		Confirmed: map[string]string{"m": "{}"},
		Scan:      adapter.ScanState(`{"v":1}`),
		ScanOpen:  true,
		Removal:   syncer.RemovalStep(2), GraceUntil: at, RescanFrom: at, RemovalAt: at, Through: at,
	}
	b, err := jsonx.MarshalCanonical(st)
	if err != nil {
		t.Fatal(err)
	}
	var older stateFormat1
	if err := jsonx.UnmarshalKnown(b, &older); err != nil {
		t.Fatalf("the version before this one refuses a state this one writes: %v", err)
	}
	v := reflect.ValueOf(older)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("format 1 has the member %s, which this version no longer writes", v.Type().Field(i).Name)
		}
	}
	var back syncer.State
	if err := jsonx.UnmarshalKnown(b, &back); err != nil {
		t.Fatalf("this version refuses a state format 1 writes: %v", err)
	}
	if back.Removal != st.Removal || !back.RemovalAt.Equal(st.RemovalAt) || !back.Through.Equal(st.Through) {
		t.Errorf("the removal read back as step %v at %v through %v", back.Removal, back.RemovalAt, back.Through)
	}
}

func TestNextDue(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tasks := func(ts ...syncer.TaskState) (out [4]syncer.TaskState) {
		for i := range out {
			out[i] = syncer.TaskState{Due: at.Add(time.Hour)}
		}
		copy(out[:], ts)
		return out
	}
	for name, c := range map[string]struct {
		st   syncer.State
		want time.Time
	}{
		"never synced: now or before":     {syncer.State{}, time.Time{}},
		"the earliest task":               {syncer.State{Tasks: tasks(syncer.TaskState{Due: at.Add(2 * time.Minute)}, syncer.TaskState{Due: at.Add(time.Minute)})}, at.Add(time.Minute)},
		"a backoff delays a task":         {syncer.State{Tasks: tasks(syncer.TaskState{Due: at, BackoffUntil: at.Add(5 * time.Minute)}, syncer.TaskState{Due: at.Add(3 * time.Minute)})}, at.Add(3 * time.Minute)},
		"a halted task is not waited for": {syncer.State{Tasks: tasks(syncer.TaskState{Due: at, Halted: true})}, at.Add(time.Hour)},
		"every task halted":               {syncer.State{Tasks: [4]syncer.TaskState{{Halted: true}, {Halted: true}, {Halted: true}, {Halted: true}}}, never},
		"needs attention":                 {syncer.State{Status: syncer.NeedsAttention}, never},
		"being removed":                   {syncer.State{Status: syncer.Removing}, never},
	} {
		if got := nextDue(c.st); !got.Equal(c.want) {
			t.Errorf("%s: due %v, want %v", name, got, c.want)
		}
	}
}

func TestProjectStatus(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		current string
		st      syncer.State
		want    string
		reason  string
	}{
		"first sync brings a connecting supplier up": {"connecting", syncer.State{}, "active", ""},
		"a low balance":                             {"active", syncer.State{LowNotified: true}, "low_balance", ""},
		"the balance recovers":                      {"low_balance", syncer.State{}, "active", ""},
		"a person's pause stays":                    {"paused", syncer.State{}, "paused", ""},
		"reconciliation's pause stays":              {"needs_attention", syncer.State{}, "needs_attention", ""},
		"the engine needs a person":                 {"active", syncer.State{Status: syncer.NeedsAttention, Reason: "rejected"}, "needs_attention", "rejected"},
		"a removal is not turned back to attention": {"removing", syncer.State{Status: syncer.NeedsAttention, Reason: "x"}, "removing", ""},
		"removing": {"needs_attention", syncer.State{Status: syncer.Removing}, "removing", ""},
		"removed":  {"removing", syncer.State{Status: syncer.Removed}, "removed", ""},
		"a removed supplier is not removing again":   {"removed", syncer.State{Status: syncer.Removing}, "removed", ""},
		"attention without a reason makes no reason": {"active", syncer.State{Status: syncer.NeedsAttention}, "needs_attention", ""},
	} {
		got, reason := projectStatus(c.current, c.st)
		if got != c.want || (reason == nil) != (c.reason == "") || (reason != nil && *reason != c.reason) {
			t.Errorf("%s: status %q reason %v, want %q %q", name, got, reason, c.want, c.reason)
		}
	}
}
