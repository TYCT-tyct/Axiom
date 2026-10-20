package protocol_test

import (
	"bytes"
	"testing"
	"testing/iotest"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/streamcheck"
)

// chatUpstreamCases are the conversations a Chat client has with a Chat endpoint of the kinds the gateway buys from:
// each is a request, the stream a DeepSeek- or Qwen-like endpoint answers it with, assembled in the shapes their
// documentation gives (DeepSeek《Create Chat Completion》, read 2026-10-19; OpenAI《Create chat completion》; Alibaba
// Cloud Model Studio's OpenAI-compatible Chat, read 2026-10-19, whose usage chunk has no choices; its image token
// detail is written as OpenAI's usage has it, the page lists none) because no recorded sample of those
// endpoints is checked in (README.md「录制」), and what the usage of each is billed as.
var chatUpstreamCases = []struct {
	name string
	// The items the usage maps to, with every other item 0.
	want map[catalog.Item]int64
}{
	// Tool calls in fragments, the filler DashScope and DeepSeek write on the later ones, and usage in the chunk that
	// finishes the choice with DeepSeek's names for the cache read beside the standard ones.
	{"chat-tool-calls", map[catalog.Item]int64{catalog.ItemInput: 56, catalog.ItemCacheRead: 64, catalog.ItemOutput: 40}},
	// Reasoning in reasoning_content, a null beside the answer's content, a multi-turn request that sends it back.
	{"chat-reasoning-cache", map[catalog.Item]int64{
		catalog.ItemInput: 72, catalog.ItemCacheRead: 128, catalog.ItemOutput: 20, catalog.ItemReasoning: 30,
	}},
	// An image in the request and in the usage, and the usage chunk with no choices.
	{"chat-image", map[catalog.Item]int64{catalog.ItemInput: 24, catalog.ItemImageIn: 66, catalog.ItemOutput: 6}},
}

// TestChatUpstreamSamples replays each conversation as the gateway does: the request is rewritten for the endpoint
// (golden <name>.to-chat.json), the stream is relayed whole and byte by byte to the client (<name>.client-chat.sse) and
// aggregated for a client that asked for none (<name>.aggregate-chat.json), and the usage is billed as want says,
// the same on all three paths. go test -update rewrites the goldens.
func TestChatUpstreamSamples(t *testing.T) {
	for _, c := range chatUpstreamCases {
		t.Run(c.name, func(t *testing.T) {
			up := prepareForChatEndpoint(t, c.name)
			stream := sample(t, "assembled/"+c.name+".sse")
			relayed := relayChatWholeAndBytewise(t, c.name, stream, up.Usage)
			whole := aggregate(t, protocol.Chat, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(0), unsettled)
			if whole.err != nil {
				t.Fatal(whole.err)
			}
			golden(t, "assembled/"+c.name+".aggregate-chat.json", append(whole.body, '\n'))
			checkBilledAs(t, "stream", relayed.r.Usage(), c.want)
			checkBilledAs(t, "aggregate", whole.r.Usage(), c.want)
		})
	}
}

// prepareForChatEndpoint rewrites the conversation's request for a streaming Chat endpoint, against its golden copy.
func prepareForChatEndpoint(t *testing.T, name string) protocol.Upstream {
	t.Helper()
	request := sample(t, "assembled/"+name+".request.json")
	r := inspected(t, protocol.Chat, request)
	up, err := protocol.Prepare(&r, request, protocol.Target{Format: protocol.Chat, Model: "up", Streams: true, MaxOutput: 4096},
		protocol.Rewrites{Identity: derive})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "assembled/"+name+".to-chat.json", append(up.Body, '\n'))
	return up
}

// relayChatWholeAndBytewise relays the stream to a Chat client in one read and byte by byte, which must agree, against
// the golden client stream, and checks that the client's stream is a valid Chat stream.
func relayChatWholeAndBytewise(t *testing.T, name string, stream []byte, ctx protocol.UsageContext) relayed {
	t.Helper()
	got := relay(t, protocol.Chat, bytes.NewReader(stream), ctx, protocol.NewMemory(0), settled)
	bytewise := relay(t, protocol.Chat, iotest.OneByteReader(bytes.NewReader(stream)), ctx, protocol.NewMemory(0), settled)
	if got.err != nil || !bytes.Equal(bytewise.out, got.out) {
		t.Fatalf("error %v; byte by byte:\n%s\nwhole:\n%s", got.err, bytewise.out, got.out)
	}
	if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
	golden(t, "assembled/"+name+".client-chat.sse", got.out)
	return got
}

// checkBilledAs holds a usage to being exactly the items in want, reported, with nothing unknown.
func checkBilledAs(t *testing.T, path string, u protocol.Usage, want map[catalog.Item]int64) {
	t.Helper()
	if u.Invalid != nil || len(u.Segments) != 1 {
		t.Fatalf("%s: usage %+v", path, u)
	}
	seg := u.Segments[0]
	for it := range catalog.NumItems {
		if seg.Usage.Count(it) != want[it] {
			t.Errorf("%s: %s = %d, want %d", path, it, seg.Usage.Count(it), want[it])
		}
	}
	if len(seg.Usage.Unknown()) != 0 {
		t.Errorf("%s: unknown usage %v", path, seg.Usage.Unknown())
	}
	if !seg.Reported.Has(catalog.ItemInput) || !seg.Reported.Has(catalog.ItemOutput) {
		t.Errorf("%s: input and output are not reported: %v", path, seg.Reported)
	}
}
