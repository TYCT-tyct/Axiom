package protocol_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// contents gathers what a stream of format f says, in order: each Messages content block's events and each Responses
// output item's events whole, as the relay passes them on unchanged, and for each Chat choice every member of its
// deltas, tool calls included, every other member with a value, and its last finish_reason, as the relay splits
// chunks. A relay that ends normally must hand the client exactly what the upstream sent.
func contents(t testing.TB, f protocol.Format, stream []byte) map[string]string {
	t.Helper()
	got := map[string]string{}
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if string(data) == sse.Done {
			continue
		}
		var e map[string]any
		if err := jsonx.Unmarshal(data, &e); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		switch typ, _ := e["type"].(string); {
		case f == protocol.Messages && strings.HasPrefix(typ, "content_block_"):
			got[fmt.Sprint(e["index"])] += fmt.Sprint(e)
		case f == protocol.Responses && e["response"] == nil && e["output_index"] != nil:
			got[fmt.Sprint(e["output_index"])] += fmt.Sprint(e)
		case f == protocol.Chat:
			chatContents(e, got)
		}
	}
	return got
}

// chatContents adds a chunk's choices to got. fmt prints maps in key order, so a value prints the same both sides.
func chatContents(e map[string]any, got map[string]string) {
	add := func(key string, v any) {
		switch v := v.(type) {
		case nil:
		case string: // empty text says nothing; our usage chunk has some
			if v != "" {
				got[key] += v
			}
		default:
			got[key] += fmt.Sprint(v)
		}
	}
	choices, _ := e["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		i := fmt.Sprint(choice["index"])
		for name, v := range choice {
			switch name {
			case "index":
			case "finish_reason": // the usage chunk we write repeats choice 0's
				if v != nil {
					got[i+"/finish_reason"] = fmt.Sprint(v)
				}
			case "delta":
				delta, _ := v.(map[string]any)
				deltaContents(i, delta, add)
			default:
				add(i+"/"+name, v)
			}
		}
	}
}

// deltaContents adds choice i's delta, each tool call's members on their own.
func deltaContents(i string, delta map[string]any, add func(string, any)) {
	for name, v := range delta {
		if name != "tool_calls" {
			add(i+"/"+name, v)
			continue
		}
		calls, _ := v.([]any)
		for _, call := range calls {
			call, _ := call.(map[string]any)
			function, _ := call["function"].(map[string]any)
			tool := fmt.Sprintf("%s/tool%v/", i, call["index"])
			for key, v := range map[string]any{
				"id": call["id"], "type": call["type"], "name": function["name"],
				"arguments": function["arguments"],
			} {
				add(tool+key, v)
			}
		}
	}
}

// decodes reports whether every event of a stream decodes to Go values, which contents compares: a number past
// float64's range is valid JSON but does not.
func decodes(stream []byte) bool {
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		var v any
		if string(data) != sse.Done && jsonx.Unmarshal(data, &v) != nil {
			return false
		}
	}
	return true
}

// endsNormally reports whether a stream its format's check accepts ends the way that format ends normally, not with
// an error.
//
//nolint:exhaustive // a format without an end event is not normal
func endsNormally(f protocol.Format, stream []byte) bool {
	r := sse.NewReader(bytes.NewReader(stream), 32<<20)
	var last []byte
	for data, err := r.Next(); err == nil; data, err = r.Next() {
		if f == protocol.Chat && bytes.Contains(data, []byte(`"error"`)) && !bytes.Contains(data, []byte(`"error":null`)) {
			return false
		}
		last = append(last[:0], data...)
	}
	switch f {
	case protocol.Messages:
		return bytes.Contains(last, []byte(`"message_stop"`))
	case protocol.Responses:
		return bytes.Contains(last, []byte(`"response.completed"`)) || bytes.Contains(last, []byte(`"response.incomplete"`))
	case protocol.Chat:
		return string(last) == sse.Done
	}
	return false
}
