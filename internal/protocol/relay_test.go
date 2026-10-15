package protocol_test

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
	"axiom/internal/protocol/internal/streamcheck"
)

var update = flag.Bool("update", false, "rewrite the golden client streams in testdata")

var (
	usd     = must(platform.ParseCurrency("USD"))
	price   = must(platform.ParseUnitPrice("0.000003"))
	options = protocol.RelayOptions{ID: "gen-01", Model: "claude-client-name"}
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// settled is the settlement the tests' endings report.
func settled(u protocol.Usage) protocol.Final {
	return protocol.Final{Usage: u, Cost: price.Mul(1234567), Currency: usd}
}

func sample(tb testing.TB, name string) []byte {
	b, err := fs.ReadFile(samples, name)
	if err != nil && !*update {
		tb.Fatal(err)
	}
	return b
}

// relayed is what a gateway-like caller writes for one upstream stream.
type relayed struct {
	out   []byte
	kinds []protocol.StepKind
	err   error // what ended the reading, nil at the upstream's normal end
	r     *protocol.Relay
}

// relay runs a Relay over src as the gateway does: it writes what each step gives, settles with settle at the
// upstream's normal end, and writes Fail's ending after a failed step. It checks that a step holding events writes
// nothing and that the ending is written once.
func relay(t testing.TB, src io.Reader, ctx protocol.UsageContext, mem *protocol.Memory,
	settle func(protocol.Usage) protocol.Final,
) relayed {
	t.Helper()
	o := options
	o.Memory = mem
	res := relayed{r: protocol.NewRelay(protocol.Messages, protocol.Messages, src, ctx, o)}
	for {
		step, err := res.r.Next()
		if err != nil {
			res.err = err
			res.out = append(res.out, res.r.Fail(protocol.Error{Type: protocol.UpstreamFailed, Message: "upstream failed"}, nil)...)
			break
		}
		res.kinds = append(res.kinds, step.Kind)
		if (step.Kind == protocol.StepBuffered || step.Kind == protocol.StepProgress) && len(step.Out) > 0 {
			t.Fatalf("step %d writes %q", step.Kind, step.Out)
		}
		res.out = append(res.out, step.Out...)
		if step.Kind == protocol.StepUpstreamDone {
			res.out = append(res.out, res.r.Finish(settle(res.r.Usage()))...)
			break
		}
	}
	_, err := res.r.Next()
	if again := append(res.r.Finish(settled(protocol.Usage{})), res.r.Fail(protocol.Error{}, nil)...); len(again) > 0 ||
		!errors.Is(err, protocol.ErrTerminated) {
		t.Fatalf("after the ending: Next %v, another ending %q", err, again)
	}
	return res
}

// TestRelaySamples relays every Messages sample, whole and byte by byte, and compares the client's stream with its
// golden copy, client-messages.sse, which go test -update rewrites. Every client stream is a valid Messages stream.
func TestRelaySamples(t *testing.T) {
	dirs, err := fs.Glob(samples, "messages/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no Messages samples: %v", err)
	}
	for _, dir := range dirs {
		t.Run(dir, func(t *testing.T) {
			stream := sample(t, dir+"/upstream.sse")
			// The base allowance holds an ordinary stream: none of it is drawn from the node's budget.
			got := relay(t, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if got.err != nil {
				t.Fatal(got.err)
			}
			if bytewise := relay(t, iotest.OneByteReader(bytes.NewReader(stream)), protocol.UsageContext{},
				protocol.NewMemory(0), settled); !bytes.Equal(bytewise.out, got.out) {
				t.Errorf("byte by byte:\n%s\nwhole:\n%s", bytewise.out, got.out)
			}
			if err := streamcheck.Messages(bytes.NewReader(got.out)); err != nil {
				t.Error(err)
			}
			golden(t, dir+"/client-messages.sse", got.out)
		})
	}
}

// golden compares got with the golden file at name in testdata, which go test -update rewrites.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path.Join("testdata", name), got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if diff := cmp.Diff(string(sample(t, name)), string(got)); diff != "" {
		t.Errorf("client stream (-golden +got):\n%s", diff)
	}
}

// Events to build streams from.
const (
	blockStart  = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	blockStop   = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	hiDelta     = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n"
	textBlock   = blockStart + hiDelta + blockStop
	messageStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	ping        = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
)

func messageStart(usage string) string {
	return `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],` +
		`"model":"claude-upstream-name","stop_reason":null,"usage":` + usage + "}}\n\n"
}

func messageDelta(usage string) string {
	return `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":` + usage + "}\n\n"
}

// events decodes the data of each event of a client stream.
func events(t *testing.T, stream []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for block := range strings.SplitSeq(strings.TrimSuffix(string(stream), "\n\n"), "\n\n") {
		_, data, _ := strings.Cut(block, "data: ")
		var e map[string]any
		if err := jsonx.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("event %q: %v", block, err)
		}
		out = append(out, e)
	}
	return out
}

// TestRelaySteps: nothing is written before the first content, which writes everything held with it; a ping is
// written only after it; the ending is held until the upstream's normal end.
func TestRelaySteps(t *testing.T) {
	stream := ping + messageStart(`{"input_tokens":3,"output_tokens":1}`) + ping + textBlock + ping +
		messageDelta(`{"output_tokens":2}`) + messageStop
	got := relay(t, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	var kinds []protocol.StepKind
	for _, k := range got.kinds {
		if k != protocol.StepProgress {
			kinds = append(kinds, k)
		}
	}
	want := []protocol.StepKind{
		protocol.StepHeartbeat, protocol.StepBuffered, protocol.StepHeartbeat, protocol.StepBuffered,
		protocol.StepCommit, protocol.StepData, protocol.StepHeartbeat, protocol.StepBuffered, protocol.StepUpstreamDone,
	}
	if diff := cmp.Diff(want, kinds); diff != "" || got.err != nil {
		t.Fatalf("steps (-want +got):\n%s\nerror: %v", diff, got.err)
	}
	if n := strings.Count(string(got.out), "event: ping"); n != 1 {
		t.Errorf("%d pings written, want only the one after the commit point", n)
	}
	if err := streamcheck.Messages(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
}

// TestRelayEndings: each way a stream ends gives exactly one ending, and only the upstream's normal end a successful
// one; a failure before any content writes nothing the upstream sent.
func TestRelayEndings(t *testing.T) {
	start := messageStart(`{"input_tokens":3,"output_tokens":1}`)
	for _, c := range []struct {
		name, stream string
		err          error // nil: a normal end, which ends with message_stop; otherwise the ending is an error event
		committed    bool
	}{
		{"normal end", start + textBlock + messageDelta(`{"output_tokens":2}`) + messageStop, nil, true},
		{
			"error before content", start + `data: {"type":"error","error":{"type":"overloaded_error","message":"x"}}` + "\n\n",
			protocol.ErrUpstream, false,
		},
		{
			"error after content", start + textBlock + `data: {"type":"error","error":{"type":"api_error","message":"x"}}` + "\n\n",
			protocol.ErrUpstream, true,
		},
		{"stream ends after the usage event", start + textBlock + messageDelta(`{"output_tokens":2}`), protocol.ErrUpstream, true},
		{"stream ends inside an event", start + textBlock + "data: {", protocol.ErrUpstream, true},
		{"end without a usage event", start + textBlock + messageStop, protocol.ErrUpstream, true},
		{"event after the usage event", start + messageDelta(`{"output_tokens":2}`) + textBlock, protocol.ErrUpstream, false},
		{"not an object", start + "data: [1]\n\n", protocol.ErrUpstream, false},
		{"no content at all", start + messageDelta(`{"output_tokens":2}`) + messageStop, nil, false},
		{
			"two usage events", start + textBlock + messageDelta(`{"output_tokens":2}`) + messageDelta(`{"output_tokens":3}`) + messageStop,
			nil, true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if !errors.Is(got.err, c.err) || (c.err == nil) != (got.err == nil) {
				t.Fatalf("error %v, want %v", got.err, c.err)
			}
			es := events(t, got.out)
			last, want, deltas := es[len(es)-1]["type"], "message_stop", strings.Count(c.stream, `"message_delta"`)
			if c.err != nil {
				want, deltas = "error", 0
			}
			wrote := strings.Contains(string(got.out), "content_block_start")
			if last != want || wrote != c.committed || strings.Count(string(got.out), `"message_delta"`) != deltas {
				t.Errorf("last event %v, want %s; content written %v, want %v; message_delta events %d, want %d", last, want,
					wrote, c.committed, strings.Count(string(got.out), `"message_delta"`), deltas)
			}
			if err := streamcheck.Messages(bytes.NewReader(got.out)); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestRelayRewrites: the client sees the call ID and its own model name, usage without anything but Messages'
// usage members, and the cost of our settlement in the ending.
func TestRelayRewrites(t *testing.T) {
	stream := messageStart(`{"input_tokens":3,"output_tokens":1,"cost":0.9,"inference_geo":"us"}`) + textBlock +
		strings.Replace(messageDelta(`{"output_tokens":2,"cost_details":{"upstream_inference_cost":0.8}}`), "{", `{"axiom":{"billing_pending":true},`, 1) +
		messageStop
	for _, c := range []struct {
		name  string
		final protocol.Final
		usage map[string]any // the ending's usage
		axiom any
	}{
		{"settled", settled(protocol.Usage{}), map[string]any{
			"output_tokens": 2.0, "cost": 3.703701, "cost_currency": "USD",
		}, nil},
		{"estimated", protocol.Final{Cost: price.Mul(1e6), Currency: usd, Estimated: true}, map[string]any{
			"output_tokens": 2.0, "cost": 3.0, "cost_currency": "USD", "cost_estimated": true,
		}, nil},
		{
			"pending",
			protocol.Final{Pending: true, Cost: price.Mul(1)},
			map[string]any{"output_tokens": 2.0},
			map[string]any{"billing_pending": true},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0),
				func(protocol.Usage) protocol.Final { return c.final })
			es := events(t, got.out)
			message := es[0]["message"].(map[string]any)
			wantStart := map[string]any{"input_tokens": 3.0, "output_tokens": 1.0, "inference_geo": "us"}
			if message["id"] != options.ID || message["model"] != options.Model || !cmp.Equal(message["usage"], wantStart) {
				t.Errorf("message_start's message: %v", message)
			}
			end := es[len(es)-2]
			if diff := cmp.Diff(c.usage, end["usage"]); diff != "" || !cmp.Equal(end["axiom"], c.axiom) {
				t.Errorf("ending usage (-want +got):\n%s\naxiom: %v", diff, end["axiom"])
			}
		})
	}
	// An ending without usage gets one, and an empty one takes the cost as its only members.
	for _, usage := range []string{"", `,"usage":null`, `,"usage":{"cost":1}`} {
		delta := `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}` + usage + "}\n\n"
		got := relay(t, strings.NewReader(messageStart(`{}`)+textBlock+delta+messageStop), protocol.UsageContext{},
			protocol.NewMemory(0), settled)
		es := events(t, got.out)
		if u := es[len(es)-2]["usage"]; !cmp.Equal(u, map[string]any{"cost": 3.703701, "cost_currency": "USD"}) {
			t.Errorf("usage%s: ending usage %v", usage, u)
		}
	}
}

// TestRelayFail: an error ending says our error type in Messages' shape, with the usage reported so far, and the cost
// only when the settlement was written.
func TestRelayFail(t *testing.T) {
	stream := messageStart(`{"input_tokens":3,"cache_read_input_tokens":4,"output_tokens":1}`) + textBlock
	for _, c := range []struct {
		name  string
		final *protocol.Final
		usage map[string]any
	}{
		{"not settled", nil, map[string]any{
			"input_tokens": 3.0, "cache_creation_input_tokens": 0.0, "cache_read_input_tokens": 4.0, "output_tokens": 1.0,
			"cache_creation":  map[string]any{"ephemeral_5m_input_tokens": 0.0, "ephemeral_1h_input_tokens": 0.0},
			"server_tool_use": map[string]any{"web_search_requests": 0.0},
		}},
		{"settled", &protocol.Final{Cost: price.Mul(1e6), Currency: usd, Estimated: true}, map[string]any{
			"input_tokens": 0.0, "cache_creation_input_tokens": 0.0, "cache_read_input_tokens": 0.0, "output_tokens": 0.0,
			"cache_creation":  map[string]any{"ephemeral_5m_input_tokens": 0.0, "ephemeral_1h_input_tokens": 0.0},
			"server_tool_use": map[string]any{"web_search_requests": 0.0},
			"cost":            3.0, "cost_currency": "USD", "cost_estimated": true,
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := options
			o.Memory = protocol.NewMemory(0)
			r := protocol.NewRelay(protocol.Messages, protocol.Messages, strings.NewReader(stream), protocol.UsageContext{}, o)
			var out []byte
			for {
				step, err := r.Next()
				if err != nil {
					break
				}
				out = append(out, step.Out...)
			}
			out = append(out, r.Fail(protocol.Error{Type: protocol.CreditsInsufficient, Message: "credits ran out"}, c.final)...)
			es := events(t, out)
			end := es[len(es)-1]
			want := map[string]any{"type": "billing_error", "message": "credits ran out", "error_type": "credits_insufficient"}
			if diff := cmp.Diff(want, end["error"]); diff != "" {
				t.Errorf("error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(c.usage, end["usage"]); diff != "" {
				t.Errorf("usage (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRelayUnsupported(t *testing.T) {
	r := protocol.NewRelay(protocol.Chat, protocol.Messages, strings.NewReader(""), protocol.UsageContext{}, options)
	if _, err := r.Next(); err == nil {
		t.Fatal("relaying Messages to a Chat client succeeded")
	}
}

// TestKeepAlive: the keep-alive bytes may go anywhere after the commit point in each format's stream.
func TestKeepAlive(t *testing.T) {
	for dir, c := range map[string]struct {
		f     protocol.Format
		check func(io.Reader) error
		event string // the event the keep-alive is: Messages' ping, or none for a comment
	}{
		"messages/text":  {protocol.Messages, streamcheck.Messages, `{"type":"ping"}`},
		"responses/text": {protocol.Responses, streamcheck.Responses, ""},
		"chat/text":      {protocol.Chat, streamcheck.Chat, ""},
	} {
		stream := string(sample(t, dir+"/upstream.sse"))
		i := strings.Index(stream, "\n\n") + 2
		if err := c.check(strings.NewReader(stream[:i] + string(protocol.KeepAlive(c.f)) + stream[i:])); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
		data, err := sse.NewReader(bytes.NewReader(protocol.KeepAlive(c.f)), 1<<10).Next()
		if string(data) != c.event || (c.event == "") != errors.Is(err, io.EOF) {
			t.Errorf("%s: the keep-alive reads as %q, %v", dir, data, err)
		}
	}
}

// FuzzRelay: whatever valid Messages stream the upstream sends, however it is split into reads, the client gets the
// same valid Messages stream with exactly one ending.
func FuzzRelay(f *testing.F) {
	for _, dir := range []string{"messages/text", "messages/thinking", "messages/tool-use"} {
		f.Add(sample(f, dir+"/upstream.sse"), []byte{3, 200, 7})
	}
	f.Add([]byte(messageStart(`{"input_tokens":3}`)+textBlock+messageDelta(`{"output_tokens":2}`)+messageStop), []byte{1})
	f.Add([]byte(messageStart(`{}`)+`data: {"type":"error","error":{"type":"api_error","message":"x"}}`+"\n\n"), []byte{9})
	f.Fuzz(func(t *testing.T, stream, cuts []byte) {
		if streamcheck.Messages(bytes.NewReader(stream)) != nil {
			return
		}
		whole := relay(t, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
		split := relay(t, &chunks{stream, cuts}, protocol.UsageContext{}, protocol.NewMemory(1<<20), settled)
		if !bytes.Equal(whole.out, split.out) {
			t.Fatalf("split %v:\n%s\nwhole:\n%s", cuts, split.out, whole.out)
		}
		if err := streamcheck.Messages(bytes.NewReader(whole.out)); err != nil {
			t.Fatalf("client stream: %v\n%s", err, whole.out)
		}
	})
}

// chunks returns data in reads whose sizes come from cuts.
type chunks struct{ data, cuts []byte }

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := len(c.data)
	if len(c.cuts) > 0 {
		n = min(n, 1+int(c.cuts[0]))
		c.cuts = c.cuts[1:]
	}
	n = copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}
