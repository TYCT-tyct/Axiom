package protocol_test

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"strings"
	"testing"
	"testing/fstest"
	"testing/iotest"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/streamcheck"
)

// chatUpstreamCases are the conversations a Chat client has with a Chat endpoint of the kinds the gateway buys from:
// each is a request, the stream a DeepSeek- or Qwen-like endpoint answers it with, assembled in the shapes their
// documentation gives (DeepSeek《Create Chat Completion》, read 2026-10-19; OpenAI《Create chat completion》; Alibaba
// Cloud Model Studio's OpenAI-compatible Chat, read 2026-10-19, whose usage chunk has no choices; its image token
// detail is written as OpenAI's usage has it, the page lists none) before a real upstream of that kind is recorded,
// and what the usage of each is billed as.
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
// the same on both forwarding paths. go test -update rewrites the assembled goldens.
//
// It then replays every scene `just record chat` wrote under testdata/chat: the scene states the billed items its
// stream must produce in usage.json, so recording a real upstream needs the data file and no code change. A scene the
// recorder wrote without one fails the test rather than go unchecked; a scene transcribed from documentation, such as
// chat/text, has no usage to state and is covered by the other sample tests.
func TestChatUpstreamSamples(t *testing.T) {
	for _, c := range chatUpstreamCases {
		t.Run(c.name, func(t *testing.T) {
			up := prepareChatRequest(t, "assembled/"+c.name+".request.json", "assembled/"+c.name+".to-chat.json")
			stream := sample(t, "assembled/"+c.name+".sse")
			relayed := relayChatWholeAndBytewise(t, stream, up.Usage)
			golden(t, "assembled/"+c.name+".client-chat.sse", relayed.out)
			whole := aggregate(t, protocol.Chat, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(0), unsettled)
			if whole.err != nil {
				t.Fatal(whole.err)
			}
			golden(t, "assembled/"+c.name+".aggregate-chat.json", append(whole.body, '\n'))
			checkBilledAs(t, "stream", relayed.r.Usage(), c.want)
			checkBilledAs(t, "aggregate", whole.r.Usage(), c.want)
		})
	}
	for _, dir := range chatScenes(t) {
		t.Run(dir, func(t *testing.T) {
			want, recorded, err := chatSceneUsage(samples, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !recorded {
				return
			}
			up := prepareChatRequest(t, dir+"/request.json", "")
			stream := sample(t, dir+"/upstream.sse")
			relayed := relayChatWholeAndBytewise(t, stream, up.Usage)
			whole := aggregate(t, protocol.Chat, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(0), unsettled)
			if whole.err != nil {
				t.Fatal(whole.err)
			}
			checkBilledTotals(t, "stream", relayed.r.Usage(), want)
			checkBilledTotals(t, "aggregate", whole.r.Usage(), want)
		})
	}
}

// chatScenes is every scene directory under testdata/chat, recorded or transcribed.
func chatScenes(t *testing.T) []string {
	t.Helper()
	dirs, err := fs.Glob(samples, "chat/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no chat scenes: %v", err)
	}
	return dirs
}

// recordedSource is the source meta.json gives a scene the recorder wrote (internal/record).
const recordedSource = "recorded from a live upstream: POST "

// chatSceneUsage reads a recorded scene's expected billed items from its usage.json: each item's total count over the
// call's samplings, an item left out being zero. recorded is false for a scene transcribed from documentation, which
// states no usage; one the recorder wrote without usage.json is an error, so a recorded scene never goes unchecked.
func chatSceneUsage(fsys fs.FS, dir string) (want map[catalog.Item]int64, recorded bool, err error) {
	raw, err := fs.ReadFile(fsys, dir+"/usage.json")
	if errors.Is(err, fs.ErrNotExist) {
		source, err := chatSceneSource(fsys, dir)
		if err != nil {
			return nil, false, err
		}
		if strings.HasPrefix(source, recordedSource) {
			return nil, false, errors.New(dir + " was recorded from a live upstream but has no usage.json: " +
				"add the billed items its stream must produce, as {\"input\": n, \"cache_read\": n, ...}")
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	counts := map[string]int64{}
	if err := jsonx.Unmarshal(raw, &counts); err != nil {
		return nil, false, errors.New(dir + "/usage.json: " + err.Error())
	}
	want = map[catalog.Item]int64{}
	for name, n := range counts {
		it, err := catalog.ParseItem(name)
		if err != nil {
			return nil, false, errors.New(dir + "/usage.json: " + err.Error())
		}
		want[it] = n
	}
	return want, true, nil
}

// chatSceneSource is the source meta.json records for a scene.
func chatSceneSource(fsys fs.FS, dir string) (string, error) {
	var m struct {
		Source string `json:"source"`
	}
	raw, err := fs.ReadFile(fsys, dir+"/meta.json")
	if err != nil {
		return "", err
	}
	if err := jsonx.Unmarshal(raw, &m); err != nil {
		return "", errors.New(dir + "/meta.json: " + err.Error())
	}
	return m.Source, nil
}

// chatUsageCase is one scene's files and what chatSceneUsage should make of them.
type chatUsageCase struct {
	name     string
	files    fstest.MapFS
	want     map[catalog.Item]int64
	recorded bool
	err      string
}

// TestChatSceneUsage: a recorded scene must state its billed items, a transcription need not, and a name that is not
// an item is refused so that a typo cannot silently expect zero.
func TestChatSceneUsage(t *testing.T) {
	const meta = `{"source":"` + recordedSource + `/v1/chat/completions","date":"2026-10-19"}`
	const page = `{"source":"https://example.com/chat","date":"2026-10-19"}`
	for _, c := range []chatUsageCase{
		{
			"a recorded scene",
			fstest.MapFS{
				"chat/x/meta.json":  {Data: []byte(meta)},
				"chat/x/usage.json": {Data: []byte(`{"input":25,"cache_read":40,"output":15}`)},
			},
			map[catalog.Item]int64{catalog.ItemInput: 25, catalog.ItemCacheRead: 40, catalog.ItemOutput: 15},
			true, "",
		},
		{
			"a recorded scene without usage",
			fstest.MapFS{"chat/x/meta.json": {Data: []byte(meta)}},
			nil, false, "usage.json",
		},
		{
			"a scene transcribed from documentation",
			fstest.MapFS{"chat/x/meta.json": {Data: []byte(page)}},
			nil, false, "",
		},
		{
			"an item name that does not exist",
			fstest.MapFS{
				"chat/x/meta.json":  {Data: []byte(meta)},
				"chat/x/usage.json": {Data: []byte(`{"cache-read":1}`)},
			},
			nil, false, "cache-read",
		},
	} {
		t.Run(c.name, func(t *testing.T) { checkChatSceneUsage(t, c) })
	}
}

// checkChatSceneUsage holds chatSceneUsage to one case.
func checkChatSceneUsage(t *testing.T, c chatUsageCase) {
	t.Helper()
	want, recorded, err := chatSceneUsage(c.files, "chat/x")
	if c.err != "" {
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Fatalf("error = %v, want one naming %q", err, c.err)
		}
		return
	}
	if err != nil || recorded != c.recorded {
		t.Fatalf("recorded = %t, error %v; want %t", recorded, err, c.recorded)
	}
	if !maps.Equal(want, c.want) {
		t.Errorf("usage = %v, want %v", want, c.want)
	}
}

// prepareChatRequest rewrites the request at requestPath for a streaming Chat endpoint; a non-empty goldenPath keeps
// the result there.
func prepareChatRequest(t *testing.T, requestPath, goldenPath string) protocol.Upstream {
	t.Helper()
	request := sample(t, requestPath)
	r := inspected(t, protocol.Chat, request)
	up, err := protocol.Prepare(&r, request, protocol.Target{Format: protocol.Chat, Model: "up", Streams: true, MaxOutput: 4096},
		protocol.Rewrites{Identity: derive})
	if err != nil {
		t.Fatal(err)
	}
	if goldenPath != "" {
		golden(t, goldenPath, append(up.Body, '\n'))
	}
	return up
}

// relayChatWholeAndBytewise relays the stream to a Chat client in one read and byte by byte, which must agree, and
// checks that the client's stream is a valid Chat stream.
func relayChatWholeAndBytewise(t *testing.T, stream []byte, ctx protocol.UsageContext) relayed {
	t.Helper()
	got := relay(t, protocol.Chat, bytes.NewReader(stream), ctx, protocol.NewMemory(0), settled)
	bytewise := relay(t, protocol.Chat, iotest.OneByteReader(bytes.NewReader(stream)), ctx, protocol.NewMemory(0), settled)
	if got.err != nil || !bytes.Equal(bytewise.out, got.out) {
		t.Fatalf("error %v; byte by byte:\n%s\nwhole:\n%s", got.err, bytewise.out, got.out)
	}
	if err := streamcheck.Chat(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
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

// checkBilledTotals is checkBilledAs for a recorded call: the items are summed over the usage's samplings, so what the
// call used in all is what the scene states, and every sampling must report input and output and hold nothing unknown.
func checkBilledTotals(t *testing.T, path string, u protocol.Usage, want map[catalog.Item]int64) {
	t.Helper()
	if u.Invalid != nil || len(u.Segments) == 0 {
		t.Fatalf("%s: usage %+v", path, u)
	}
	got := map[catalog.Item]int64{}
	for _, seg := range u.Segments {
		for it := range catalog.NumItems {
			got[it] += seg.Usage.Count(it)
		}
		if len(seg.Usage.Unknown()) != 0 {
			t.Errorf("%s: unknown usage %v", path, seg.Usage.Unknown())
		}
		if !seg.Reported.Has(catalog.ItemInput) || !seg.Reported.Has(catalog.ItemOutput) {
			t.Errorf("%s: a sampling does not report input and output: %v", path, seg.Reported)
		}
	}
	for it := range catalog.NumItems {
		if got[it] != want[it] {
			t.Errorf("%s: %s = %d, want %d", path, it, got[it], want[it])
		}
	}
}
