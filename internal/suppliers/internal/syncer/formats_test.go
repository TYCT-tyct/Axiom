package syncer

import (
	"slices"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// TestStateKeepsFormatsByTheGate: the persisted state is what every node loads and projects, a pre-P18 one included,
// for any task. With the gate closed it holds no format such a node cannot name, whether the listing was read or its
// models were kept from an earlier state written with the gate open; a model left with no format is not listed. Open,
// it keeps every format the adapter reported.
func TestStateKeepsFormatsByTheGate(t *testing.T) {
	t.Parallel()
	listed := func(unread bool) func(adapter.Listing) (adapter.Page[adapter.Group], error) {
		return func(adapter.Listing) (adapter.Page[adapter.Group], error) {
			return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{
				Ref: "1", Name: "g", Ratio: "1", Facts: adapter.GroupFacts(`{}`), ModelsUnread: unread,
				Models: []adapter.Model{
					{UpstreamName: "embed", Formats: []protocol.Format{protocol.Embeddings}},
					{UpstreamName: "mixed", Formats: []protocol.Format{protocol.Chat, protocol.Embeddings, protocol.Rerank}},
				},
			}}}, nil
		}
	}
	formats := func(g GroupSnap) map[string][]int {
		out := map[string][]int{}
		for _, m := range g.Models {
			out[m.Name] = m.Formats
		}
		return out
	}
	chat, embeddings, rerank := int(protocol.Chat), int(protocol.Embeddings), int(protocol.Rerank)
	m := healthy()
	m.offer = listed(false)
	st, clk := newStore(), &clock{t: t0}
	open := Defaults()
	open.DirectFormats = true
	round(t, New(st, m, open, clk.now))
	if got, want := formats(st.state.Groups[0]), map[string][]int{"embed": {embeddings}, "mixed": {chat, embeddings, rerank}}; !equalFormats(got, want) {
		t.Fatalf("open gate keeps %v, want %v", got, want)
	}
	for _, unread := range []bool{true, false} {
		m.offer = listed(unread)
		clk.advance(10 * time.Minute)
		round(t, New(st, m, Defaults(), clk.now))
		if got, want := formats(st.state.Groups[0]), map[string][]int{"mixed": {chat}}; !equalFormats(got, want) {
			t.Errorf("closed gate, models unread %t: state keeps %v, want %v", unread, got, want)
		}
	}
}

func equalFormats(a, b map[string][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
