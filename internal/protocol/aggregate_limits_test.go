package protocol_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// aggregateDeltas is deltas with the events a reply that is aggregated needs to place them: the Messages block stays
// open, and the Responses item and its part are added before their text.
func aggregateDeltas(f protocol.Format, n, size int) []byte {
	text := strings.Repeat("x", size)
	switch f {
	case protocol.Responses:
		delta := `data: {"type":"response.output_text.delta","sequence_number":3,"output_index":0,"content_index":0,` +
			`"item_id":"msg_1","delta":"` + text + "\"}\n\n"
		return []byte(responsesEvent("response.created", 0, responsesObject) +
			responsesEvent("response.output_item.added", 1, `,"output_index":0,"item":{"id":"msg_1","type":"message","content":[]}`) +
			responsesEvent("response.content_part.added", 2, `,"output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":""}`) +
			strings.Repeat(delta, n) +
			responsesEvent("response.completed", 4, strings.Replace(responsesObject, `"usage":null`, `"usage":{"input_tokens":3}`, 1)))
	case protocol.Messages:
		delta := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + "\"}}\n\n"
		return []byte(messageStart(`{"input_tokens":3}`) + blockStart + strings.Repeat(delta, n) + blockStop +
			messageDelta(`{"output_tokens":2}`) + messageStop)
	case protocol.Chat:
	}
	return deltas(f, n, size)
}

// bigStream is a stream of format f whose reply is n deltas of 512 KiB.
func bigStream(f protocol.Format, n int) string {
	return string(aggregateDeltas(f, n, 512<<10))
}

// TestAggregateBudget: a reply holds at most 16 MiB of content, and what it holds beyond the stream's base allowance
// is drawn from the node's budget, both in the pieces and in what is charged twice for the body made of them. The
// stream that passes either fails with ErrMemory, a local failure, never the upstream's, and gives back all it drew,
// the reply that failed by the cap included.
func TestAggregateBudget(t *testing.T) {
	const node = 128 << 20
	// A Responses ending whose response alone passes the cap.
	huge := strings.Replace(responsesObject, `"output":[]`, `"output":[],"x":"`+strings.Repeat("x", 17<<20)+`"`, 1)
	hugeEnding := responsesEvent("response.created", 0, responsesObject) +
		responsesEvent("response.completed", 1, strings.Replace(huge, `"usage":null`, `"usage":{"input_tokens":3}`, 1))
	for _, c := range []struct {
		name             string
		client, upstream protocol.Format
		stream           string
		budget           int64
		wantMemory       bool
	}{
		{"messages within the cap", protocol.Messages, protocol.Messages, bigStream(protocol.Messages, 31), node, false},
		{"messages past the cap", protocol.Messages, protocol.Messages, bigStream(protocol.Messages, 34), node, true},
		{"messages past the node's budget", protocol.Messages, protocol.Messages, bigStream(protocol.Messages, 24), 4 << 20, true},
		{"chat within the cap", protocol.Chat, protocol.Chat, bigStream(protocol.Chat, 8), node, false},
		{"chat past the cap", protocol.Chat, protocol.Chat, bigStream(protocol.Chat, 34), node, true},
		{"chat past the node's budget", protocol.Chat, protocol.Chat, bigStream(protocol.Chat, 24), 20 << 20, true},
		{"responses within the cap", protocol.Responses, protocol.Responses, bigStream(protocol.Responses, 8), node, false},
		{"responses ending past the cap", protocol.Responses, protocol.Responses, hugeEnding, node, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := protocol.NewMemory(c.budget)
			got := aggregate(t, c.client, c.upstream, strings.NewReader(c.stream), mem, unsettled)
			if errors.Is(got.err, protocol.ErrMemory) != c.wantMemory || errors.Is(got.err, protocol.ErrUpstream) || (got.err != nil) != c.wantMemory {
				t.Errorf("error %v, want a memory error: %v", got.err, c.wantMemory)
			}
			if mem.Left() != c.budget {
				t.Errorf("the node's budget is %d after the reply, was %d", mem.Left(), c.budget)
			}
		})
	}
}

// TestAggregateHoldsItsBudgetUntilReleased: the body Finish or Fail returns is the relay's buffers, so the budget they
// were charged against stays drawn after the ending, and Release, which the gateway calls once the body is written,
// gives it back whole, once.
func TestAggregateHoldsItsBudgetUntilReleased(t *testing.T) {
	const node, size = 128 << 20, 2 << 20
	for _, p := range aggregatePaths {
		stream := string(aggregateDeltas(p.upstream, 4, size/4))
		for name, stream := range map[string]string{"finished": stream, "failed": stream[:len(stream)-20]} {
			t.Run(p.name+" "+name, func(t *testing.T) {
				mem := protocol.NewMemory(node)
				got := aggregate(t, p.client, p.upstream, strings.NewReader(stream), mem, unsettled)
				if (got.err != nil) != (name == "failed") {
					t.Fatalf("error %v", got.err)
				}
				// A Messages client's failure body has no content, so the relay holds nothing for it.
				if held := node - got.left; held < int64(len(got.body))-64<<10 {
					t.Errorf("%d bytes of the budget were held with a body of %d", held, len(got.body))
				}
				if mem.Left() != node {
					t.Errorf("the budget is %d after Release, was %d", mem.Left(), node)
				}
			})
		}
	}
}

// drainAggregated is drain for a reply that is aggregated.
func drainAggregated(tb testing.TB, f protocol.Format, stream []byte) {
	o := protocol.RelayOptions{Memory: protocol.NewMemory(1 << 30), Aggregate: true}
	r := protocol.NewRelay(f, f, bytes.NewReader(stream), protocol.UsageContext{}, o)
	for {
		step, err := r.Next()
		if err != nil {
			tb.Fatal(err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			r.Finish(settled(r.Usage()))
			return
		}
	}
}

// TestAggregateAllocs: aggregating allocates nothing per event beyond what reading it does, but the growth of the
// buffers, which doubles, so the allocations a longer reply adds are logarithmic in its length and do not depend on
// the size of its deltas (docs/performance.md「调用路径上的规则」). The measurement runs in a child process, so this
// binary's other goroutines and their collections are not counted as the aggregator's.
func TestAggregateAllocs(t *testing.T) {
	read := func(stream []byte) func() {
		return func() {
			r := sse.NewReader(bytes.NewReader(stream), 1<<20)
			for _, err := r.Next(); err == nil; _, err = r.Next() {
			}
		}
	}
	for _, format := range formats {
		for _, size := range []int{1 << 10, 16 << 10} {
			t.Run(fmt.Sprintf("%s-%d", format.dir, size), func(t *testing.T) {
				ten, twoHundred := aggregateDeltas(format.f, 10, size), aggregateDeltas(format.f, 200, size)
				got := alloctest.Measure(t,
					alloctest.Function{Name: "read/10", F: read(ten)},
					alloctest.Function{Name: "aggregate/10", F: func() { drainAggregated(t, format.f, ten) }},
					alloctest.Function{Name: "read/200", F: read(twoHundred)},
					alloctest.Function{Name: "aggregate/200", F: func() { drainAggregated(t, format.f, twoHundred) }},
				)
				few := got["aggregate/10"].Count - got["read/10"].Count
				more := got["aggregate/200"].Count - got["read/200"].Count
				// 190 more events would add at least 190 allocations if each allocated. The pieces and the decoder's
				// pooled buffers, made again after a collection, add a few.
				if more-few > 30 {
					t.Errorf("%s, %d-byte deltas: aggregating allocates %v more than reading 10 events, %v more for 200",
						format.dir, size, few, more)
				}
			})
		}
	}
}

func BenchmarkAggregate(b *testing.B) {
	for _, format := range formats {
		for _, n := range []int{16, 128} {
			for _, size := range []int{1 << 10, 64 << 10} {
				stream := aggregateDeltas(format.f, n, size)
				b.Run(fmt.Sprintf("%s/deltas=%d/size=%d", format.dir, n, size), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(stream)))
					for b.Loop() {
						drainAggregated(b, format.f, stream)
					}
				})
			}
		}
	}
}
