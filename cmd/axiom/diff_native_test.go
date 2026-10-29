//go:build diff

package main

import (
	"strings"
	"testing"
)

// TestDiffHarnessResponsesLifecycleOrder: a Responses stream's own lifecycle is checked apart from the pairing, so two
// events that differ only by their IDs cannot be swapped and still compare equal.
func TestDiffHarnessResponsesLifecycleOrder(t *testing.T) {
	life := func(name string, sequence int) event {
		return readEvent(name, canonical(map[string]any{"type": name, "sequence_number": sequence}))
	}
	created, progress, completed := life("response.created", 0), life("response.in_progress", 1), life("response.completed", 2)
	theirs := []event{created, progress, completed}
	for _, tc := range []struct {
		name string
		ours []event
		ok   bool
	}{
		{"in order", theirs, true},
		{"created and in progress swapped", []event{progress, created, completed}, false},
		{"in progress lost", []event{created, completed}, false},
		{"completed twice", []event{created, progress, completed, completed}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok := true
			for _, d := range compareEvents(theirs, tc.ours) {
				if d.path == "sse[response].lifecycle" {
					ok = false
				}
			}
			if ok != tc.ok {
				t.Errorf("lifecycle accepted=%v, want %v", ok, tc.ok)
			}
		})
	}
}

func TestDiffHarnessResponsesItemOrder(t *testing.T) {
	frame := func(index, seq int, text string) event {
		return readEvent("response.output_item.done", canonical(map[string]any{
			"output_index": index, "sequence_number": seq, "item": map[string]any{"text": text},
		}))
	}
	theirs := []event{frame(1, 0, "answer"), frame(0, 1, "reasoning")}
	for _, tc := range []struct {
		text string
		bad  bool
	}{{"answer", false}, {"lost answer", true}} {
		ours := []event{frame(0, 0, "reasoning"), frame(1, 1, tc.text)}
		bad := false
		for _, d := range compareEvents(theirs, ours) {
			if !strings.HasSuffix(d.path, ".sequence_number") || !eventSequence(d) {
				bad = true
			}
		}
		if bad != tc.bad {
			t.Errorf("text %q: content difference=%v, want %v", tc.text, bad, tc.bad)
		}
	}
}

func TestDiffHarnessNativeEventsRefuseDegradation(t *testing.T) {
	for _, tc := range []struct {
		theirs, ours string
		ok           bool
	}{{"4", "6", true}, {"4", "5", false}, {"3", "6", false}} {
		d := difference{at: "sse[response.completed]#4/6.sequence_number", newAPI: tc.theirs, axiom: tc.ours}
		if eventSequence(d) != tc.ok {
			t.Errorf("event sequence %s/%s accepted=%v", tc.theirs, tc.ours, !tc.ok)
		}
	}
	const part = `{"content_index":0,"item_id":"12345678-1234-1234-1234-123456789abc_0","output_index":0,"part":{"annotations":[],"text":"Hello","type":"output_text"},"sequence_number":5,"type":"response.content_part.done"}`
	for _, tc := range []struct {
		from, to string
		ok       bool
	}{{"", "", true}, {"Hello", "Lost", false}, {`"sequence_number":5`, `"sequence_number":4`, false}, {`"output_index":0`, `"output_index":1`, false}, {`"annotations":[]`, `"annotations":[1]`, false}} {
		body := part
		if tc.from != "" {
			body = strings.Replace(body, tc.from, tc.to, 1)
		}
		d := difference{at: "sse[response.content_part.done]#-/5", newAPI: absent, axiom: body}
		if responsePartIs("Hello", 0)(d) != tc.ok {
			t.Errorf("part mutation %q accepted=%v", tc.from, !tc.ok)
		}
	}
}
