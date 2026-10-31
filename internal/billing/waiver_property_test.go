package billing

import (
	"testing"

	"axiom/internal/catalog"
)

// FuzzZeroOutputIncludesAudio: the waiver's zero-output condition must see every output item, audio included. A reply
// with any reported audio is delivered output and is never waived; one with all four at zero keeps the original rule.
func FuzzZeroOutputIncludesAudio(f *testing.F) {
	f.Add(int64(0), int64(0), int64(0), int64(0))
	f.Add(int64(0), int64(0), int64(1), int64(0))
	f.Add(int64(0), int64(0), int64(0), int64(1))
	f.Add(int64(1), int64(0), int64(0), int64(0))
	f.Add(int64(-3), int64(0), int64(0), int64(0))
	f.Fuzz(func(t *testing.T, output, reasoning, audio, image int64) {
		positive := func(n int64) int64 {
			n %= 4
			if n < 0 {
				n = -n
			}
			return n
		}
		counts := map[catalog.Item]int64{
			catalog.ItemOutput:    positive(output),
			catalog.ItemReasoning: positive(reasoning),
			catalog.ItemAudioOut:  positive(audio),
			catalog.ItemImageOut:  positive(image),
		}
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		at := AttemptResult{Result: Committed, Finish: FinishError, Segments: []Segment{{
			Usage: u,
			Reported: 1<<catalog.ItemOutput | 1<<catalog.ItemReasoning | 1<<catalog.ItemAudioOut |
				1<<catalog.ItemImageOut,
		}}}
		sum := counts[catalog.ItemOutput] + counts[catalog.ItemReasoning] + counts[catalog.ItemAudioOut] +
			counts[catalog.ItemImageOut]
		if got := zeroOutput(at); got != (sum == 0) {
			t.Fatalf("counts %v: zeroOutput = %v, want %v", counts, got, sum == 0)
		}
	})
}
