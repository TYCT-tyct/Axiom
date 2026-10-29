package protocol

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

type breakdownCase struct {
	name, first, next            string
	input, read, write, imageOut int64
	invalid                      bool
}

func TestOpenAIBreakdownsReplaceTogether(t *testing.T) {
	for _, tc := range []breakdownCase{
		{"cache detail arrives later", `{"input_tokens":100}`, `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, 0, 80, 80, 0, false},
		{"image detail arrives later", `{"output_tokens":100}`, `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, 0, 0, 0, 100, false},
		{"cleared input leaves its whole snapshot", `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, `{"input_tokens":0}`, 0, 80, 80, 0, false},
		{"cleared output leaves its whole snapshot", `{"output_tokens":100,"output_tokens_details":{"image_tokens":100}}`, `{"output_tokens":0}`, 0, 0, 0, 100, false},
		{"physical input went down", `{"input_tokens":100}`, `{"input_tokens":99}`, 100, 0, 0, 0, true},
		{"physical output went down", `{"output_tokens":100}`, `{"output_tokens":99}`, 0, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, metered(t, tc.first, tc.next)) })
	}
}

func (tc breakdownCase) check(t *testing.T, got Usage) {
	t.Helper()
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
	for _, tc := range []splitCase{
		{
			"input split kept", cached, `{"prompt_tokens":100,"completion_tokens":50}`,
			map[catalog.Item]int64{catalog.ItemInput: 20, catalog.ItemCacheRead: 80, catalog.ItemOutput: 50},
			100, 50,
		},
		{
			"output split kept", reasoning, `{"completion_tokens":100}`,
			map[catalog.Item]int64{catalog.ItemOutput: 40, catalog.ItemReasoning: 60},
			0, 100,
		},
		{
			"larger input total replaces the split", cached, `{"prompt_tokens":120}`,
			map[catalog.Item]int64{catalog.ItemInput: 120},
			120, 0,
		},
		{
			"larger output total replaces the split", reasoning, `{"completion_tokens":120}`,
			map[catalog.Item]int64{catalog.ItemOutput: 120},
			0, 120,
		},
		{
			"an empty details object replaces the input split", cached, `{"prompt_tokens":100,"prompt_tokens_details":{}}`,
			map[catalog.Item]int64{catalog.ItemInput: 100},
			100, 0,
		},
		{
			"an empty details object replaces the output split", reasoning, `{"completion_tokens":100,"completion_tokens_details":{}}`,
			map[catalog.Item]int64{catalog.ItemOutput: 100},
			0, 100,
		},
		{
			"details of the same total replace the split", cached, `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":10}}`,
			map[catalog.Item]int64{catalog.ItemInput: 90, catalog.ItemCacheRead: 10},
			100, 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, metered(t, tc.first, tc.next)) })
	}
}

type splitCase struct {
	name, first, next string
	counts            map[catalog.Item]int64
	physicalIn        int64
	wantTotalOut      int64
}

func (tc splitCase) check(t *testing.T, got Usage) {
	t.Helper()
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
}

// TestNativeSplitsMergeByPresence: an input frame reports video, a later output frame reports predictions and says
// nothing about video. The later frame must not wipe the earlier native split.
func TestNativeSplitsMergeByPresence(t *testing.T) {
	u := metered(t,
		`{"input_tokens":100,"input_tokens_details":{"video_tokens":10}}`,
		`{"output_tokens":5,"output_tokens_details":{"accepted_prediction_tokens":2,"rejected_prediction_tokens":1}}`)
	got := u.Segments[0].Usage.NativeTokens()
	if got.VideoInput != 10 || got.AcceptedPrediction != 2 || got.RejectedPrediction != 1 {
		t.Errorf("native split = %+v, want video 10, accepted 2, rejected 1", got)
	}
}

// TestNativeSplitsReplaceOnSameSideSnapshot: a new output details object can report accepted=0 after accepted=4; the
// zero is a replacement, not an absent field. Otherwise accepted and rejected would add up past the output total.
func TestNativeSplitsReplaceOnSameSideSnapshot(t *testing.T) {
	u := metered(t,
		`{"output_tokens":5,"output_tokens_details":{"accepted_prediction_tokens":4,"rejected_prediction_tokens":1}}`,
		`{"output_tokens":5,"output_tokens_details":{"accepted_prediction_tokens":0,"rejected_prediction_tokens":5}}`)
	got := u.Segments[0].Usage.NativeTokens()
	if got.AcceptedPrediction != 0 || got.RejectedPrediction != 5 {
		t.Errorf("native split = %+v, want accepted 0, rejected 5", got)
	}
	if got.AcceptedPrediction+got.RejectedPrediction > 5 {
		t.Errorf("native split %+v exceeds the output total", got)
	}
}

// TestNativeVideoClearsOnAnInputSnapshot: video is an input-details field, so a later input snapshot with video=0
// replaces the earlier 10; an output frame from the same call must not clear it.
func TestNativeVideoClearsOnAnInputSnapshot(t *testing.T) {
	u := metered(t,
		`{"input_tokens":100,"input_tokens_details":{"video_tokens":10}}`,
		`{"output_tokens":5,"output_tokens_details":{"accepted_prediction_tokens":2}}`,
		`{"input_tokens":100,"input_tokens_details":{"video_tokens":0}}`)
	if got := u.Segments[0].Usage.NativeTokens(); got.VideoInput != 0 || got.AcceptedPrediction != 2 {
		t.Errorf("native split = %+v, want video 0 and accepted 2 kept from the output frame", got)
	}
}

// TestNativeSplitsKeepWithAZeroTotal: a relay that clears a side's total is ignored by takeSide, so the native splits
// from the kept snapshot must be ignored too, even when the clearing report has an empty details object.
func TestNativeSplitsKeepWithAZeroTotal(t *testing.T) {
	u := metered(t,
		`{"input_tokens":100,"input_tokens_details":{"video_tokens":10},`+
			`"output_tokens":5,"output_tokens_details":{"accepted_prediction_tokens":4,"rejected_prediction_tokens":1}}`,
		`{"input_tokens":0,"input_tokens_details":{}}`,
		`{"output_tokens":0,"output_tokens_details":{}}`)
	got := u.Segments[0].Usage.NativeTokens()
	if got.VideoInput != 10 || got.AcceptedPrediction != 4 || got.RejectedPrediction != 1 {
		t.Errorf("native split = %+v, want the kept input video 10 and output predictions 4/1", got)
	}
}
