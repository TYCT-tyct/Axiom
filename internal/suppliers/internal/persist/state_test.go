package persist

import (
	"maps"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/syncer"
)

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
	} {
		if got := nextDue(c.st); !got.Equal(c.want) {
			t.Errorf("%s: due %v, want %v", name, got, c.want)
		}
	}
}

// TestAStateOfTheFirstFormatIsRead: format 1 kept the plans as an object keyed by the joined group and model, and no
// confirmed bases; it is read into this format, the plans by group and model from the groups' own models (which the
// object repeated), each of them published, since nothing else made a plan then. This format reads back what it
// writes, and a later one is refused.
func TestAStateOfTheFirstFormatIsRead(t *testing.T) {
	t.Parallel()
	v1 := `{"Groups":[{"Ref":"a","Name":"a","Ratio":"1","Facts":"{}","Models":[{"Name":"b/c","Formats":[2],"Without":0,"Plan":"f2"},` +
		`{"Name":"d","Formats":[2],"Without":0,"Plan":""}]},` +
		`{"Ref":"a/b","Name":"a/b","Ratio":"1","Facts":"{}","Models":[{"Name":"c","Formats":[2],"Without":0,"Plan":"f1"}]}],` +
		`"Plans":{"a/b/c":"f1"},"Confirmed":null}`
	st, err := decode(3, 1, []byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	want := syncer.Plans{{GroupRef: "a", Model: "b/c"}: "f2", {GroupRef: "a/b", Model: "c"}: "f1"}
	if !maps.Equal(st.Plans, want) || st.Version != 3 || len(st.Confirmed) != 0 {
		t.Errorf("format 1 read as plans %v, version %d, bases %v; want %v, 3 and none", st.Plans, st.Version, st.Confirmed, want)
	}
	if m := st.Groups[0].Models; !m[0].Published || m[1].Published || !st.Groups[1].Models[0].Published {
		t.Errorf("the models of format 1 read as %+v %+v; a model with a plan is published, one without is not", m, st.Groups[1].Models)
	}

	raw, err := jsonx.MarshalCanonical(st)
	if err != nil {
		t.Fatal(err)
	}
	again, err := decode(3, format, raw)
	if err != nil || !maps.Equal(again.Plans, want) {
		t.Errorf("this format read back plans %v, %v; want %v", again.Plans, err, want)
	}
	if _, err := decode(3, format+1, raw); err == nil {
		t.Error("a later format was read")
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
		"a low balance":                              {"active", syncer.State{LowNotified: true}, "low_balance", ""},
		"the balance recovers":                       {"low_balance", syncer.State{}, "active", ""},
		"a person's pause stays":                     {"paused", syncer.State{}, "paused", ""},
		"reconciliation's pause stays":               {"needs_attention", syncer.State{}, "needs_attention", ""},
		"the engine needs a person":                  {"active", syncer.State{Status: syncer.NeedsAttention, Reason: "rejected"}, "needs_attention", "rejected"},
		"a removal is not turned back to attention":  {"removing", syncer.State{Status: syncer.NeedsAttention, Reason: "x"}, "removing", ""},
		"attention without a reason makes no reason": {"active", syncer.State{Status: syncer.NeedsAttention}, "needs_attention", ""},
	} {
		got, reason := projectStatus(c.current, c.st)
		if got != c.want || (reason == nil) != (c.reason == "") || (reason != nil && *reason != c.reason) {
			t.Errorf("%s: status %q reason %v, want %q %q", name, got, reason, c.want, c.reason)
		}
	}
}
