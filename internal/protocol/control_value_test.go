package protocol

import (
	"testing"
)

func TestReasoningDetailIndexUsesDecodedValue(t *testing.T) {
	t.Parallel()
	c, err := decodeCanon([]byte(`{"model":"m","messages":[{"role":"assistant","reasoning_details":[{"type":"reasoning.text","index":0,"text":"a"},{"type":"reasoning.text","index":-0,"text":"b"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	details := c.Messages[0].ReasoningDetails
	if len(details) != 1 || string(details[0].Text) != `"ab"` {
		t.Fatalf("equivalent indices split one detail: %+v", details)
	}
}

func TestReasoningDetailIndices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		first, second string
		count         int
	}{
		{"0", "-0", 1},
		{"-0", "0", 1},
		{"1", "2", 2},
		{"null", "null", 2},
		{"9223372036854775807", "9223372036854775807", 1},
		{"9007199254740992", "9007199254740993", 2},
	} {
		c, err := decodeCanon([]byte(`{"messages":[{"role":"assistant","reasoning_details":[{"type":"reasoning.text","index":` + tc.first + `,"text":"a"},{"type":"reasoning.text","index":` + tc.second + `,"text":"b"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		details := c.Messages[0].ReasoningDetails
		if len(details) != tc.count || string(details[0].Index) != tc.first {
			t.Fatalf("%s/%s: %+v", tc.first, tc.second, details)
		}
	}
	for _, index := range []string{`"0"`, `true`, `-1`, `1.5`, `1e0`, `9223372036854775808`} {
		_, err := decodeCanon([]byte(`{"messages":[{"role":"assistant","reasoning_details":[{"type":"reasoning.text","index":` + index + `}]}]}`))
		if err == nil {
			t.Fatalf("invalid index accepted: %s", index)
		}
	}
}
