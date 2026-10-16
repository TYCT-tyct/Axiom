package persist

import (
	"testing"
	"time"

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
