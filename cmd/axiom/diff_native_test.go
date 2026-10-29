//go:build diff

package main

import (
	"slices"
	"strings"
	"testing"
)

// TestDiffHarnessResponsesLifecycleOrder: a Responses stream's own lifecycle is checked apart from the pairing, so two
// events that differ only by their IDs cannot be swapped, lost or repeated and still compare equal.
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
				if d.path == "sse.order" || d.newAPI == absent || d.axiom == absent {
					ok = false
				}
			}
			if ok != tc.ok {
				t.Errorf("lifecycle accepted=%v, want %v", ok, tc.ok)
			}
		})
	}
}

// TestDiffHarnessEventOrder: the diff checks the order of every event group, not only the Responses lifecycle, so a
// swap within one output item or content block, or a closing event moved before its group, is not hidden by pairing.
func TestDiffHarnessEventOrder(t *testing.T) {
	makeEvent := func(name string, doc map[string]any) event {
		return readEvent(name, canonical(doc))
	}
	part := func(name string) event {
		return makeEvent(name, map[string]any{"type": name, "output_index": 0, "content_index": 0})
	}
	message := func(name string, index int) event {
		return makeEvent(name, map[string]any{"type": name, "index": index})
	}
	unindexed := func(name string) event { return makeEvent(name, map[string]any{"type": name}) }

	added, partAdded, partDone, itemDone := part("response.output_item.added"), part("response.content_part.added"), part("response.content_part.done"), part("response.output_item.done")
	delta, textDone := part("response.output_text.delta"), part("response.output_text.done")
	created, completed := unindexed("response.created"), unindexed("response.completed")
	messageStart, blockStart, blockStop := unindexed("message_start"), message("content_block_start", 0), message("content_block_stop", 0)
	messageDelta, messageStop := unindexed("message_delta"), unindexed("message_stop")

	item := func(index int) event {
		return makeEvent("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": index})
	}
	for _, tc := range []struct {
		name         string
		theirs, ours []event
		order        bool
	}{
		{"same item keeps its lifecycle", []event{added, partAdded, partDone, itemDone}, []event{added, partAdded, partDone, itemDone}, false},
		{"item closes before its part", []event{added, partAdded, partDone, itemDone}, []event{added, partAdded, itemDone, partDone}, true},
		{"text delta after text done", []event{delta, textDone}, []event{textDone, delta}, true},
		{"message stop before message delta", []event{messageStart, blockStart, blockStop, messageDelta, messageStop}, []event{messageStart, blockStart, blockStop, messageStop, messageDelta}, true},
		{"message stop before content block stop", []event{messageStart, blockStart, blockStop, messageDelta, messageStop}, []event{messageStart, blockStart, messageStop, blockStop, messageDelta}, true},
		{"response completed before its item", []event{created, item(0), completed}, []event{created, completed, item(0)}, true},
		{"items close in either order", []event{item(1), item(0)}, []event{item(0), item(1)}, false},
		{"a part the other stream omits", []event{added, itemDone}, []event{added, partAdded, partDone, itemDone}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := slices.ContainsFunc(compareEvents(tc.theirs, tc.ours), func(d difference) bool { return d.path == "sse.order" })
			if got != tc.order {
				t.Errorf("order difference=%v, want %v", got, tc.order)
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
