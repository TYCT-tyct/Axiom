package protocol_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/sse"
)

// TestRelayProgress: Next returns after every read of at most 16 KiB, so an event larger than a read, or one that
// keeps arriving and never finishes, is counted while it arrives.
func TestRelayProgress(t *testing.T) {
	head := messageStart(`{"input_tokens":3}`) +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`
	for _, c := range []struct {
		name string
		src  io.Reader
		err  error // nil: the event finishes and commits
	}{
		{"large event", strings.NewReader(head + strings.Repeat(`\u00e9`, 10<<10) + strings.Repeat("é", 10<<10) + "\"}}\n\n"), nil},
		{"never finishes", io.MultiReader(strings.NewReader(head), endless{}), protocol.ErrUpstream},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := &largestRead{src: c.src}
			o := options
			o.Memory = protocol.NewMemory(4 << 20)
			r := protocol.NewRelay(protocol.Messages, protocol.Messages, src, protocol.UsageContext{}, o)
			steps, err := untilCommit(t, r)
			if !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			if steps < 2 || src.largest > 16<<10 {
				t.Errorf("%d progress steps, largest read %d", steps, src.largest)
			}
			// é is two bytes whether written raw or escaped.
			if p := r.Progress(); err == nil && p.ContentBytes != 40<<10 {
				t.Errorf("content bytes %d, want the unquoted text's %d", p.ContentBytes, 40<<10)
			}
		})
	}
}

// untilCommit steps r to its commit point or its first error, checking that each read that finishes no event counts
// what arrived, and returns how many there were.
func untilCommit(t *testing.T, r *protocol.Relay) (reads int, err error) {
	t.Helper()
	var partial int64
	step, err := r.Next()
	for ; err == nil && step.Kind != protocol.StepCommit; step, err = r.Next() {
		p := r.Progress()
		if step.Kind == protocol.StepProgress {
			if p.PartialBytes <= partial || p.PartialBytes > partial+16<<10 {
				t.Fatalf("partial bytes %d after %d", p.PartialBytes, partial)
			}
			reads++
		}
		partial = p.PartialBytes
	}
	return reads, err
}

// largestRead records the most a read asked for.
type largestRead struct {
	src     io.Reader
	largest int
}

func (r *largestRead) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	return r.src.Read(p)
}

// endless is text that never ends.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestRelayMeter: input counts are final once reported, output counts from the message_delta with the stop reason,
// whatever the order of its members; Usage reports only final counts, so after a stream that broke off billing
// estimates the output. Counts only go up: a later 0 keeps the earlier count and a smaller one makes the usage
// invalid.
func TestRelayMeter(t *testing.T) {
	last := strings.Replace(messageDelta(`{"input_tokens":0,"output_tokens":15}`), `"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}`,
		`"delta":{"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta"`, 1)
	stream := messageStart(`{"input_tokens":25,"output_tokens":1}`) + textBlock + last + messageStop
	o := options
	o.Memory = protocol.NewMemory(0)
	r := protocol.NewRelay(protocol.Messages, protocol.Messages, strings.NewReader(stream), protocol.UsageContext{}, o)
	check := func(when string, output int64, final bool) {
		t.Helper()
		p := r.Progress()
		u := p.Reported.Usage
		if u.Count(catalog.ItemInput) != 25 || !p.Final.Has(catalog.ItemInput) || u.Count(catalog.ItemOutput) != output ||
			!p.Reported.Reported.Has(catalog.ItemOutput) || p.Final.Has(catalog.ItemOutput) != final ||
			r.Usage().Segments[0].Reported.Has(catalog.ItemOutput) != final {
			t.Errorf("%s: input %d, output %d, reported %v, final %v", when, u.Count(catalog.ItemInput),
				u.Count(catalog.ItemOutput), p.Reported.Reported, p.Final)
		}
	}
	stepTo(t, r, protocol.StepCommit)
	check("after message_start", 1, false)
	stepTo(t, r, protocol.StepBuffered)
	check("after the last message_delta", 15, true)

	down := messageStart(`{"input_tokens":25,"output_tokens":9}`) + textBlock + messageDelta(`{"output_tokens":5}`) + messageStop
	got := relay(t, protocol.Messages, strings.NewReader(down), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	u := got.r.Usage()
	if u.Invalid == nil || u.Segments[0].Reported != 0 || got.r.Progress().Final != 0 {
		t.Errorf("a count that went down: invalid %v, reported %v", u.Invalid, u.Segments[0].Reported)
	}
}

// TestRelayMeterSample: the official text sample reports output_tokens 1 in message_start, a lower bound that the
// message_delta's 15 replaces as the final count (internal/billing/README.md「接口草图」).
func TestRelayMeterSample(t *testing.T) {
	o := options
	o.Memory = protocol.NewMemory(0)
	r := protocol.NewRelay(protocol.Messages, protocol.Messages, bytes.NewReader(sample(t, "messages/text/upstream.sse")),
		protocol.UsageContext{}, o)
	for _, want := range []struct {
		kind   protocol.StepKind
		output int64
		final  bool
	}{{protocol.StepCommit, 1, false}, {protocol.StepUpstreamDone, 15, true}} {
		stepTo(t, r, want.kind)
		if p := r.Progress(); p.Reported.Usage.Count(catalog.ItemOutput) != want.output || p.Final.Has(catalog.ItemOutput) != want.final {
			t.Errorf("at step %d: output %d, final %v", want.kind, p.Reported.Usage.Count(catalog.ItemOutput), p.Final)
		}
	}
}

// stepTo steps r until a step of kind.
func stepTo(t *testing.T, r *protocol.Relay, kind protocol.StepKind) {
	t.Helper()
	for step, err := r.Next(); step.Kind != kind; step, err = r.Next() {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestRelayCommitPoint: a content block's start is the commit point when it already holds content, as a tool call
// or a server tool's result does, and not when its text or thinking starts empty; it counts what it holds, and a web
// search it starts.
func TestRelayCommitPoint(t *testing.T) {
	for _, c := range []struct {
		block           string
		kind            protocol.StepKind
		bytes, searches int64
	}{
		{`{"type":"text","text":""}`, protocol.StepBuffered, 0, 0},
		{`{"type":"thinking","thinking":"","signature":""}`, protocol.StepBuffered, 0, 0},
		{`{"type":"text","text":"Hi"}`, protocol.StepCommit, 2, 0},
		{`{"type":"tool_use","id":"t","name":"f","input":{"a":1}}`, protocol.StepCommit, int64(len(`{"a":1}`)), 0},
		{`{"type":"server_tool_use","id":"s","name":"web_search","input":{}}`, protocol.StepCommit, 2, 1},
		{`{"name":"web_fetch","type":"server_tool_use","id":"s","input":{}}`, protocol.StepCommit, 2, 0},
		{`{"type":"web_search_tool_result","tool_use_id":"s","content":[]}`, protocol.StepCommit, 0, 0},
	} {
		start := `data: {"type":"content_block_start","index":0,"content_block":` + c.block + "}\n\n"
		o := options
		o.Memory = protocol.NewMemory(0)
		r := protocol.NewRelay(protocol.Messages, protocol.Messages, strings.NewReader(messageStart(`{}`)+start), protocol.UsageContext{}, o)
		stepTo(t, r, protocol.StepBuffered) // message_start
		step, err := r.Next()
		if p := r.Progress(); err != nil || step.Kind != c.kind || p.ContentBytes != c.bytes || p.Searches != c.searches {
			t.Errorf("%s: step %d, %v; content %d, searches %d", c.block, step.Kind, err, p.ContentBytes, p.Searches)
		}
	}
}

// TestRelaySignatureContent: a stream whose only content is a thinking signature or a redacted thinking block
// delivers bytes the upstream bills as output tokens, so a client of the upstream's format and a Chat client of a
// converted stream both count them (README.md「字节上界」).
func TestRelaySignatureContent(t *testing.T) {
	const sign, redacted = "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxB", "EmwBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdj"
	for _, c := range []struct {
		name, block, delta string
		bytes              int64
	}{
		{
			"a signature delta", `{"type":"thinking","thinking":"","signature":""}`,
			`{"type":"signature_delta","signature":"` + sign + `"}`, int64(len(sign)),
		},
		{
			"a signature in the block", `{"type":"thinking","thinking":"","signature":"` + sign + `"}`, "",
			int64(len(sign)),
		},
		{"redacted thinking", `{"type":"redacted_thinking","data":"` + redacted + `"}`, "", int64(len(redacted))},
	} {
		t.Run(c.name, func(t *testing.T) {
			stream := messageStart(`{"input_tokens":3,"output_tokens":1}`) +
				`data: {"type":"content_block_start","index":0,"content_block":` + c.block + "}\n\n"
			if c.delta != "" {
				stream += `data: {"type":"content_block_delta","index":0,"delta":` + c.delta + "}\n\n"
			}
			stream += blockStop + messageDelta(`{"output_tokens":2}`) + messageStop
			got := relay(t, protocol.Messages, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if p := got.r.Progress(); got.err != nil || p.ContentBytes != c.bytes {
				t.Errorf("content %d, want the delivered %d: %v", p.ContentBytes, c.bytes, got.err)
			}
			if conv := progressAfter(t, protocol.Chat, protocol.Messages, stream); conv != c.bytes {
				t.Errorf("a Chat client counts %d, want %d", conv, c.bytes)
			}
		})
	}
}

// TestRelayUsageMerge: a later usage event that leaves out the tier, an unknown count or the iterations keeps what an
// earlier one reported.
func TestRelayUsageMerge(t *testing.T) {
	start := messageStart(`{"input_tokens":5,"service_tier":"priority","audio_tokens":4,` +
		`"iterations":[{"type":"compaction","input_tokens":100,"output_tokens":10}]}`)
	got := relay(t, protocol.Messages, strings.NewReader(start+textBlock+messageDelta(`{"output_tokens":2}`)+messageStop),
		protocol.UsageContext{}, protocol.NewMemory(0), settled)
	want := []protocol.Segment{
		segment(t, map[catalog.Item]int64{catalog.ItemInput: 5, catalog.ItemOutput: 2}, catalog.ServiceTierPriority,
			[]catalog.UnknownUsage{{Field: "usage.audio_tokens", Value: "4"}}),
		segment(t, map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 10}, catalog.ServiceTierPriority, nil),
	}
	if diff := cmp.Diff(want, got.r.Usage().Segments, cmp.AllowUnexported(catalog.Usage{})); diff != "" || got.err != nil {
		t.Errorf("error %v; segments (-want +got):\n%s", got.err, diff)
	}
}

// TestRelayUsage maps Messages usage onto billing items as catalog README.md「计费项与用量」 tabulates.
func TestRelayUsage(t *testing.T) {
	type counts = map[catalog.Item]int64
	for _, c := range []struct {
		name    string
		usage   string // the ending's
		ctx     protocol.UsageContext
		want    counts
		tier    catalog.ServiceTier
		unknown []catalog.UnknownUsage
		segs    []counts // compactions
		invalid bool
	}{
		{
			name: "tokens", usage: `{"input_tokens":5,"cache_read_input_tokens":6,"output_tokens":7}`,
			want: counts{catalog.ItemInput: 5, catalog.ItemCacheRead: 6, catalog.ItemOutput: 7},
		},
		{
			name: "cache write breakdown", usage: `{"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}`,
			want: counts{catalog.ItemCacheWrite: 10, catalog.ItemCacheWrite1h: 20},
		},
		{
			name: "cache write breakdown that does not add up", usage: `{"cache_creation_input_tokens":31,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}`,
			invalid: true,
		},
		{
			name: "cache write by a 5-minute breakpoint", usage: `{"cache_creation_input_tokens":30}`,
			want: counts{catalog.ItemCacheWrite: 30, catalog.ItemCacheWrite1h: 0},
		},
		{
			name: "cache write by a 1-hour breakpoint", usage: `{"cache_creation_input_tokens":30}`, ctx: protocol.UsageContext{CacheTTL1h: true},
			want: counts{catalog.ItemCacheWrite: 0, catalog.ItemCacheWrite1h: 30},
		},
		{
			name: "server tools", usage: `{"server_tool_use":{"web_search_requests":2,"code_execution_requests":1,"web_fetch_requests":3,"x_requests":0}}`,
			want:    counts{catalog.ItemWebSearch: 2, catalog.ItemCodeExecution: 1},
			unknown: []catalog.UnknownUsage{{Field: "usage.server_tool_use.web_fetch_requests", Value: "3"}},
		},
		{
			name: "unknown count", usage: `{"output_tokens":1,"audio_tokens":4,"zero_tokens":0,"inference_geo":"us","output_tokens_details":{"thinking_tokens":1}}`,
			want: counts{catalog.ItemOutput: 1}, unknown: []catalog.UnknownUsage{{Field: "usage.audio_tokens", Value: "4"}},
		},
		{
			name: "a relay's cost is not usage", usage: `{"output_tokens":1,"cost":0.5,"cost_details":{"x":1},"is_byok":false}`,
			want: counts{catalog.ItemOutput: 1},
		},
		{name: "tier from the request", usage: `{}`, ctx: protocol.UsageContext{Tier: catalog.ServiceTierPriority}, tier: catalog.ServiceTierPriority},
		{name: "tier from the reply", usage: `{"service_tier":"standard"}`, ctx: protocol.UsageContext{Tier: catalog.ServiceTierPriority}},
		{name: "priority", usage: `{"service_tier":"priority"}`, tier: catalog.ServiceTierPriority},
		{name: "fast", usage: `{"service_tier":"standard","speed":"fast"}`, tier: catalog.ServiceTierFast},
		{name: "unknown tier", usage: `{"service_tier":"batch"}`, unknown: []catalog.UnknownUsage{{Field: "usage.service_tier", Value: "batch"}}},
		{
			name: "compactions", usage: `{"input_tokens":5,"output_tokens":7,"iterations":[` +
				`{"type":"compaction","input_tokens":100,"output_tokens":10},{"type":"message","input_tokens":5,"output_tokens":7}]}`,
			want: counts{catalog.ItemInput: 5, catalog.ItemOutput: 7},
			segs: []counts{{catalog.ItemInput: 100, catalog.ItemOutput: 10}},
		},
		{name: "unknown iteration", usage: `{"iterations":[{"type":"other","input_tokens":1}]}`, invalid: true},
		{name: "not a count", usage: `{"input_tokens":1.5}`, invalid: true},
		{name: "count too large", usage: `{"input_tokens":20000000000}`, invalid: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			stream := messageStart(`{}`) + textBlock + messageDelta(c.usage) + messageStop
			got := relay(t, protocol.Messages, strings.NewReader(stream), c.ctx, protocol.NewMemory(0), settled)
			u := got.r.Usage()
			if got.err != nil || (u.Invalid != nil) != c.invalid {
				t.Fatalf("error %v, invalid %v", got.err, u.Invalid)
			}
			if c.invalid {
				return
			}
			want := []protocol.Segment{segment(t, c.want, c.tier, c.unknown)}
			for _, q := range c.segs {
				want = append(want, segment(t, q, c.tier, nil))
			}
			if diff := cmp.Diff(want, u.Segments, cmp.AllowUnexported(catalog.Usage{})); diff != "" {
				t.Errorf("segments (-want +got):\n%s", diff)
			}
		})
	}
}

// segment is the segment of counts, all of them reported.
func segment(t *testing.T, counts map[catalog.Item]int64, st catalog.ServiceTier, unknown []catalog.UnknownUsage) protocol.Segment {
	t.Helper()
	s := protocol.Segment{Usage: must(catalog.NewUsage(counts, st, unknown))}
	for it := range counts {
		s.Reported.Add(it)
	}
	return s
}

// TestRelayMemory: what a stream holds past its base allowance is drawn from the node's budget as its buffers grow
// and given back at its end; a budget that is spent ends the attempt before the commit point, writing nothing, and
// with an error ending after it.
func TestRelayMemory(t *testing.T) {
	big := strings.Repeat("x", 100<<10)
	bigDelta := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + big + "\"}}\n\n"
	start := messageStart(`{"input_tokens":3}`)
	for _, c := range []struct {
		name, stream string
		budget       int64
		err          error
		committed    bool
	}{
		{
			"spent before the commit point", strings.Replace(start, `"content":[]`, `"content":[],"pad":"`+big+`"`, 1) + textBlock,
			0, protocol.ErrMemory, false,
		},
		{"spent after the commit point", start + blockStart + hiDelta + bigDelta, 0, protocol.ErrMemory, true},
		{"within the budget", start + blockStart + bigDelta + blockStop + messageDelta(`{"output_tokens":2}`) + messageStop, 1 << 20, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := protocol.NewMemory(c.budget)
			var drew bool
			src := &onRead{src: strings.NewReader(c.stream), read: func() { drew = drew || mem.Left() < c.budget }}
			got := relay(t, protocol.Messages, src, protocol.UsageContext{}, mem, settled)
			if !errors.Is(got.err, c.err) || (c.err == nil) != (got.err == nil) {
				t.Fatalf("error %v, want %v", got.err, c.err)
			}
			committed := strings.Contains(string(got.out), "content_block_start")
			if committed != c.committed || mem.Left() != c.budget || drew != (c.budget > 0) {
				t.Errorf("committed %v, want %v; budget left %d of %d at the end; drawn on while reading: %v",
					committed, c.committed, mem.Left(), c.budget, drew)
			}
		})
	}
}

// TestRelayMemoryRewrite: rewriting a snapshot draws on the budget for the copy it writes, and the next snapshot
// reuses that copy, so what the relay holds after a large snapshot stays within what it has drawn.
func TestRelayMemoryRewrite(t *testing.T) {
	const size = 800 << 10
	big := strings.Replace(responsesObject, `"output":[]`, `"output":[],"instructions":"`+strings.Repeat("x", size)+`"`, 1)
	// After the commit point, so that the events held before it do not add up.
	head := responsesEvent("response.created", 0, responsesObject) +
		responsesEvent("response.output_text.delta", 1, `,"output_index":0,"content_index":0,"item_id":"m","delta":"Hi"`)
	drawn := func(snapshots int) int64 {
		stream := head
		for i := range snapshots {
			stream += responsesEvent("response.in_progress", 2+i, big)
		}
		stream += responsesEvent("response.completed", 2+snapshots, responsesObject)
		mem := protocol.NewMemory(64 << 20)
		o := options
		o.Memory = mem
		r := protocol.NewRelay(protocol.Responses, protocol.Responses, strings.NewReader(stream), protocol.UsageContext{}, o)
		least := mem.Left()
		for step, err := r.Next(); step.Kind != protocol.StepUpstreamDone; step, err = r.Next() {
			if err != nil {
				t.Fatal(err)
			}
			least = min(least, mem.Left())
		}
		return 64<<20 - least
	}
	// The reader's buffer, the held event and the rewritten copy each hold the snapshot.
	if one, two := drawn(1), drawn(2); one < 3*size-64<<10 || two-one > 64<<10 {
		t.Errorf("drew %d for one snapshot of %d, %d for two", one, size, two)
	}
}

// TestRelayMemoryFailure: after a failure the relay still holds the events Fail writes, and keeps them charged while
// the gateway settles; it gives back the rest at once, and all of it with Fail.
func TestRelayMemoryFailure(t *testing.T) {
	const budget, size = 8 << 20, 600 << 10
	pad := strings.Replace(messageStart(`{}`), `"content":[]`, `"content":[],"pad":"`+strings.Repeat("x", size)+`"`, 1)
	mem := protocol.NewMemory(budget)
	o := options
	o.Memory = mem
	r := protocol.NewRelay(protocol.Messages, protocol.Messages, strings.NewReader(pad), protocol.UsageContext{}, o)
	for _, err := r.Next(); err == nil; _, err = r.Next() {
	}
	// The settlement is pending: another stream draws on what is left, which is the budget less the held opening.
	if held := budget - mem.Left(); held < size-64<<10 || held > 2*size {
		t.Errorf("after the failure %d is still drawn for an opening of %d", held, size)
	}
	r.Fail(protocol.Error{Type: protocol.UpstreamFailed}, nil)
	if mem.Left() != budget {
		t.Errorf("after Fail %d of %d is left", mem.Left(), budget)
	}
}

// onRead calls read before each read.
type onRead struct {
	src  io.Reader
	read func()
}

func (r *onRead) Read(p []byte) (int, error) {
	r.read()
	return r.src.Read(p)
}

// deltas is a stream of format f with n text deltas of size bytes each.
func deltas(f protocol.Format, n, size int) []byte {
	text := strings.Repeat("x", size)
	switch f {
	case protocol.Responses:
		delta := `data: {"type":"response.output_text.delta","sequence_number":2,"output_index":0,"content_index":0,` +
			`"item_id":"msg_1","delta":"` + text + "\"}\n\n"
		return []byte(responsesEvent("response.created", 0, responsesObject) + strings.Repeat(delta, n) +
			responsesEvent("response.completed", 3, strings.Replace(responsesObject, `"usage":null`, `"usage":{"input_tokens":3}`, 1)))
	case protocol.Chat:
		delta := chatHead + `[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":null}]}` + "\n\n"
		return []byte(chatRole + strings.Repeat(delta, n) + chatStop + chatUsage(`{"prompt_tokens":3}`) + chatDone)
	case protocol.Messages:
	}
	delta := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + "\"}}\n\n"
	return []byte(messageStart(`{"input_tokens":3}`) + textBlock + strings.Repeat(delta, n) + messageDelta(`{"output_tokens":2}`) + messageStop)
}

// drain relays stream as the gateway does, writing nowhere, so it allocates only what the relay does.
func drain(tb testing.TB, f protocol.Format, stream []byte) {
	r := protocol.NewRelay(f, f, bytes.NewReader(stream), protocol.UsageContext{}, protocol.RelayOptions{Memory: protocol.NewMemory(1 << 30)})
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

// steadyAllocs is testing.AllocsPerRun without the runtime's own allocations, which a collection during a run adds
// (a few, once the heap has grown) and which only ever raise a count: the collector is off and runs just before.
func steadyAllocs(f func()) float64 {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	runtime.GC()
	return testing.AllocsPerRun(5, f)
}

// TestRelayAllocs: beyond what reading the events allocates, which has a constant bound per event (README.md「读
// SSE」), relaying them allocates nothing once the buffers have grown, in each format, so a longer reply costs the
// relay no more allocations (docs/performance.md「调用路径上的规则」).
func TestRelayAllocs(t *testing.T) {
	extra := func(f protocol.Format, stream []byte) float64 {
		read := steadyAllocs(func() {
			r := sse.NewReader(bytes.NewReader(stream), 1<<20)
			for _, err := r.Next(); err == nil; _, err = r.Next() {
			}
		})
		return steadyAllocs(func() { drain(t, f, stream) }) - read
	}
	for _, format := range formats {
		for _, size := range []int{1 << 10, 64 << 10} {
			// 30 more events would add at least 30 allocations if each allocated; the race detector's own add a few.
			if few, many := extra(format.f, deltas(format.f, 10, size)), extra(format.f, deltas(format.f, 40, size)); many-few > 5 {
				t.Errorf("%s, %d-byte deltas: the relay allocates %v more than reading 10 events, %v more for 40",
					format.dir, size, few, many)
			}
		}
	}
}

// The benchmark keeps the runtime's own allocations out of the timed region, which otherwise add a varying few to the
// count and made bench-compare fail on a commit that did not touch the relay. Between collections the collector is off, so
// none falls inside a measurement; every collectEvery relays one runs outside the timer, and warmUp relays after it, and
// before the first measurement, take the extra allocations that follow a collection and a growing heap (they cannot be
// attributed to any allocation site in the code). One processor, as the relay is one goroutine, keeps the scavenger and
// the sweeper off the timed goroutine's counts. What is left is a few allocations in about one run in six, which
// tools/benchgate tolerates for this benchmark alone. 16 relays of the largest stream leave about 300 MiB of garbage.
const (
	collectEvery = 16
	warmUp       = 10
)

// raceDetector reports whether the test binary was built with the race detector.
func raceDetector() bool {
	info, ok := debug.ReadBuildInfo()
	return ok && slices.ContainsFunc(info.Settings, func(s debug.BuildSetting) bool { return s.Key == "-race" && s.Value == "true" })
}

func BenchmarkRelay(b *testing.B) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	warm := warmUp
	if raceDetector() { // `just bench-once` runs every benchmark once under the race detector, whose counts nobody compares
		warm = 0
	}
	for _, format := range formats {
		for _, n := range []int{16, 256} { // 256 64-KiB deltas is 16 MiB: enough to see growth with the count
			for _, size := range []int{1 << 10, 64 << 10} {
				stream := deltas(format.f, n, size)
				b.Run(fmt.Sprintf("%s/deltas=%d/size=%d", format.dir, n, size), func(b *testing.B) {
					// Here, not once above: the testing package restores GOMAXPROCS before every repetition of -count.
					defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
					b.ReportAllocs()
					b.SetBytes(int64(len(stream)))
					relayTimed(b, format.f, stream, warm)
				})
			}
		}
	}
}

// relayTimed relays the stream b.N times, warming up first and again after each collection, as the comment above says.
func relayTimed(b *testing.B, f protocol.Format, stream []byte, warm int) {
	for range warm {
		drain(b, f, stream)
	}
	for i := 1; b.Loop(); i++ {
		drain(b, f, stream)
		if i%collectEvery == 0 {
			b.StopTimer()
			runtime.GC()
			for range warm {
				drain(b, f, stream)
			}
			b.StartTimer()
		}
	}
}
