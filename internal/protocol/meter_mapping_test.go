package protocol

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

func TestOpenAIBreakdownsReplaceTogether(t *testing.T) {
	for _, tc := range []struct {
		name, first, next            string
		input, read, write, imageOut int64
		invalid                      bool
	}{
		{"cache detail arrives later", `{"input_tokens":100}`, `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, 0, 80, 80, 0, false},
		{"image detail arrives later", `{"output_tokens":100}`, `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, 0, 0, 0, 100, false},
		{"cleared input leaves its whole snapshot", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, `{"input_tokens":0}`, 0, 80, 80, 0, false},
		{"cleared output leaves its whole snapshot", `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, `{"output_tokens":0}`, 0, 0, 0, 100, false},
		{"physical input went down", `{"input_tokens":100}`, `{"input_tokens":99}`, 100, 0, 0, 0, true},
		{"physical output went down", `{"output_tokens":100}`, `{"output_tokens":99}`, 0, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := metered(t, tc.first, tc.next)
			if (got.Invalid != nil) != tc.invalid {
				t.Fatalf("invalid = %v", got.Invalid)
			}
			if tc.invalid {
				return
			}
			u := got.Segments[0].Usage
			for it, n := range map[catalog.Item]int64{catalog.ItemInput: tc.input, catalog.ItemCacheRead: tc.read, catalog.ItemCacheWrite: tc.write, catalog.ItemImageOut: tc.imageOut} {
				if u.Count(it) != n {
					t.Errorf("%s = %d, want %d", it, u.Count(it), n)
				}
			}
			if tc.imageOut > 0 && u.OutputTotal() != 100 {
				t.Errorf("double counted output: %d", u.OutputTotal())
			}
			if tc.read > 0 && u.InputTotal() != 100 {
				t.Errorf("physical input changed: %d", u.InputTotal())
			}
		})
	}
}

// metered is what the meter makes of OpenAI usage objects arriving one after another.
func metered(t *testing.T, bodies ...string) Usage {
	t.Helper()
	var m meter
	for _, body := range bodies {
		var u openAIUsage
		if err := jsonx.Unmarshal([]byte(body), &u); err != nil {
			t.Fatal(err)
		}
		r, err := u.items(nil)
		if err != nil {
			t.Fatal(err)
		}
		m.report(r, nil)
	}
	return m.result()
}

// A later report of the same total without the side's details object leaves the split the earlier one gave: 100
// prompt tokens of which 80 are cached are 20 input at 1 and 80 reads at 0.1, 28 in all, not 100 input. A larger total,
// or the details object itself, is a new snapshot.
func TestOpenAIKeepsTheSplitOfAnEarlierReport(t *testing.T) {
	const (
		cached    = `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":80}}`
		reasoning = `{"completion_tokens":100,"completion_tokens_details":{"reasoning_tokens":60}}`
	)
	for _, tc := range []struct {
		name         string
		first, next  string
		counts       map[catalog.Item]int64
		physicalIn   int64
		wantTotalOut int64
	}{
		{"input split kept", cached, `{"prompt_tokens":100,"completion_tokens":50}`,
			map[catalog.Item]int64{catalog.ItemInput: 20, catalog.ItemCacheRead: 80, catalog.ItemOutput: 50}, 100, 50},
		{"output split kept", reasoning, `{"completion_tokens":100}`,
			map[catalog.Item]int64{catalog.ItemOutput: 40, catalog.ItemReasoning: 60}, 0, 100},
		{"larger input total replaces the split", cached, `{"prompt_tokens":120}`,
			map[catalog.Item]int64{catalog.ItemInput: 120}, 120, 0},
		{"larger output total replaces the split", reasoning, `{"completion_tokens":120}`,
			map[catalog.Item]int64{catalog.ItemOutput: 120}, 0, 120},
		{"an empty details object replaces the input split", cached, `{"prompt_tokens":100,"prompt_tokens_details":{}}`,
			map[catalog.Item]int64{catalog.ItemInput: 100}, 100, 0},
		{"an empty details object replaces the output split", reasoning, `{"completion_tokens":100,"completion_tokens_details":{}}`,
			map[catalog.Item]int64{catalog.ItemOutput: 100}, 0, 100},
		{"details of the same total replace the split", cached, `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":10}}`,
			map[catalog.Item]int64{catalog.ItemInput: 90, catalog.ItemCacheRead: 10}, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := metered(t, tc.first, tc.next)
			if got.Invalid != nil {
				t.Fatalf("invalid: %v", got.Invalid)
			}
			u := got.Segments[0].Usage
			for it := range catalog.NumItems {
				if u.Count(it) != tc.counts[it] {
					t.Errorf("%s = %d, want %d", it, u.Count(it), tc.counts[it])
				}
			}
			if n, ok := u.PhysicalInput(); ok != (tc.physicalIn > 0) || n != tc.physicalIn {
				t.Errorf("physical input = %d/%t, want %d", n, ok, tc.physicalIn)
			}
			if u.OutputTotal() != tc.wantTotalOut {
				t.Errorf("output total = %d, want %d", u.OutputTotal(), tc.wantTotalOut)
			}
		})
	}
}
