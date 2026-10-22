//go:build diff

package main

import "testing"

func TestDiffHarnessMessageStopsCannotDisappear(t *testing.T) {
	stop := func(index int) event {
		return readEvent("content_block_stop", canonical(map[string]any{"type": "content_block_stop", "index": index}))
	}
	for _, tc := range []struct {
		name string
		ours []event
		ok   bool
	}{
		{"moved", []event{stop(1), stop(0)}, true},
		{"lost", []event{stop(1)}, false},
		{"duplicated", []event{stop(1), stop(1)}, false},
		{"wrong block", []event{stop(1), stop(2)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok := true
			for _, d := range compareEvents([]event{stop(0), stop(1)}, tc.ours) {
				if explain(diffRules(), "messages-deepseek-tools-stream", d) == nil {
					ok = false
				}
			}
			if ok != tc.ok {
				t.Fatalf("explained=%v, want %v", ok, tc.ok)
			}
		})
	}
}
