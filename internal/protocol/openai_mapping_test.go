package protocol_test

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// overlapCase is a usage object and the billing items it maps to.
type overlapCase struct {
	name, body                                         string
	input, read, write, image, audio, output, imageOut int64
	unknown                                            int // members recorded as unknown usage
	invalid                                            bool
}

var overlapCases = []overlapCase{
	{name: "overlap", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80}}`, read: 80, write: 80},
	{name: "overlap with audio", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80,"audio_tokens":10}}`, read: 80, write: 80},
	{name: "disjoint", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`, input: 50, read: 20, write: 30},
	{name: "images and text", body: `{"input_tokens":100,"input_tokens_details":{"text_tokens":90,"image_tokens":10},"output_tokens":50,"output_tokens_details":{"text_tokens":30,"image_tokens":20}}`, input: 90, image: 10, output: 30, imageOut: 20},
	{name: "fully cached images", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":100,"image_tokens":100,"cached_tokens_details":{"image_tokens":100}}}`, read: 100},
	{name: "partly cached images", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"image_tokens":60,"cached_tokens_details":{"image_tokens":30,"text_tokens":10}}}`, input: 30, read: 40, image: 30},
	{name: "fully cached without intersection", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":100,"image_tokens":100}}`, read: 100},
	{name: "partly cached without intersection", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"image_tokens":60}}`, read: 80, image: 20},
	{name: "overlap and images", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80,"image_tokens":100}}`, read: 80, write: 80},
	{name: "video input", body: `{"input_tokens":100,"input_tokens_details":{"video_tokens":10}}`, input: 90, image: 10},
	{name: "video with images", body: `{"input_tokens":100,"input_tokens_details":{"image_tokens":10,"video_tokens":20}}`, input: 70, image: 30},
	{name: "fully cached video", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":100,"video_tokens":100}}`, read: 100},
	{name: "partly cached video", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"video_tokens":60}}`, read: 80, image: 20},
	{name: "video after audio", body: `{"input_tokens":100,"input_tokens_details":{"audio_tokens":80,"video_tokens":60}}`, audio: 80, image: 20},
	{name: "cached image among video", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"image_tokens":10,"video_tokens":30,"cached_tokens_details":{"image_tokens":10}}}`, input: 30, read: 40, image: 30},
	{name: "image output only", body: `{"output_tokens":100,"output_tokens_details":{"text_tokens":0,"image_tokens":100}}`, imageOut: 100},
	{name: "video in output details is billed with the output", body: `{"output_tokens":100,"output_tokens_details":{"video_tokens":20}}`, output: 100},
	{name: "explicit zero", body: `{"input_tokens":0,"output_tokens":0}`},
	{name: "cap", body: `{"input_tokens":10000000000,"input_tokens_details":{"cached_tokens":10000000000,"cache_write_tokens":10000000000}}`, read: 1e10, write: 1e10},
	{name: "read above total", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":101}}`, invalid: true},
	{name: "write above total", body: `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":101}}`, invalid: true},
	{name: "negative", body: `{"input_tokens":100,"input_tokens_details":{"cache_write_tokens":-1}}`, invalid: true},
	{name: "negative image", body: `{"input_tokens":100,"input_tokens_details":{"image_tokens":-1}}`, invalid: true},
	{name: "negative text", body: `{"input_tokens":100,"input_tokens_details":{"text_tokens":-1}}`, invalid: true},
	{name: "past cap", body: `{"input_tokens":10000000001}`, invalid: true},
	{name: "fraction", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":1.5}}`, invalid: true},
	{name: "image above total", body: `{"input_tokens":100,"input_tokens_details":{"image_tokens":101}}`, invalid: true},
	{name: "video above total", body: `{"input_tokens":100,"input_tokens_details":{"video_tokens":101}}`, invalid: true},
	{name: "cached image above cache", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"image_tokens":60,"cached_tokens_details":{"image_tokens":41}}}`, invalid: true},
	{name: "cached image above image", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"image_tokens":10,"cached_tokens_details":{"image_tokens":11}}}`, invalid: true},
	{name: "fully cached audio", body: `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":1000,"audio_tokens":800,"cached_tokens_details":{"audio_tokens":800}}}`, read: 1000},
	{name: "partly cached audio", body: `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":400,"audio_tokens":500,"cached_tokens_details":{"audio_tokens":300}}}`, input: 400, read: 400, audio: 200},
	{name: "cached audio without intersection", body: `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":1000,"audio_tokens":800}}`, read: 1000},
	{name: "cached audio above audio", body: `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":1000,"audio_tokens":10,"cached_tokens_details":{"audio_tokens":11}}}`, invalid: true},
	{name: "cached modalities above cache", body: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"audio_tokens":30,"image_tokens":30,"cached_tokens_details":{"audio_tokens":21,"image_tokens":20}}}`, invalid: true},
	{name: "unknown cached member is recorded", body: `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":1000,"cached_tokens_details":{"video_tokens":800}}}`, read: 1000, unknown: 1},
	{name: "output parts above total", body: `{"output_tokens":100,"output_tokens_details":{"image_tokens":90,"reasoning_tokens":20}}`, invalid: true},
	{name: "video in output details above total", body: `{"output_tokens":100,"output_tokens_details":{"video_tokens":101}}`, invalid: true},
}

// Both wire formats accept both names used by OpenAI-compatible upstreams.
func TestOpenAIOverlapAndImages(t *testing.T) {
	for _, format := range []protocol.Format{protocol.Chat, protocol.Responses} {
		for _, alias := range []string{"prompt", "input"} {
			for _, tc := range overlapCases {
				t.Run(fmt.Sprintf("%v/%s/%s", format, alias, tc.name), func(t *testing.T) { tc.check(t, format, alias) })
			}
		}
	}
}

// check relays the usage object under the format's names, with the input and output names of the alias.
func (tc overlapCase) check(t *testing.T, format protocol.Format, alias string) {
	body := tc.body
	if alias == "prompt" {
		body = strings.NewReplacer("input_tokens", "prompt_tokens", "output_tokens", "completion_tokens").Replace(body)
	}
	got := relay(t, format, strings.NewReader(mappingStream(format, body, false)), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	u := got.r.Usage()
	if got.err != nil || (u.Invalid != nil) != tc.invalid {
		t.Fatalf("relay: %v, usage: %v", got.err, u.Invalid)
	}
	if tc.invalid {
		return
	}
	v := u.Segments[0].Usage
	want := map[catalog.Item]int64{
		catalog.ItemInput: tc.input, catalog.ItemCacheRead: tc.read, catalog.ItemCacheWrite: tc.write,
		catalog.ItemImageIn: tc.image, catalog.ItemAudioIn: tc.audio, catalog.ItemOutput: tc.output, catalog.ItemImageOut: tc.imageOut,
	}
	for it := range catalog.NumItems {
		if v.Count(it) != want[it] {
			t.Errorf("%s = %d, want %d", it, v.Count(it), want[it])
		}
	}
	if len(v.Unknown()) != tc.unknown {
		t.Errorf("unknown usage %v, want %d of it", v.Unknown(), tc.unknown)
	}
	total, present := decode(t, []byte(tc.body))["input_tokens"]
	n, ok := v.PhysicalInput()
	if ok != present || present && n != int64(total.(float64)) {
		t.Errorf("physical input = %d/%t, want %v/%t", n, ok, total, present)
	}
}

func mappingStream(format protocol.Format, usage string, broken bool) string {
	if format == protocol.Chat {
		s := chatRole + chatHi + chatStop + chatUsage(usage)
		if !broken {
			s += chatDone
		}
		return s
	}
	end := "response.completed"
	if broken {
		end = "response.in_progress"
	} // Failure-frame ingestion belongs to E2-3, not this mapping change.
	return responsesEvent("response.created", 0, responsesObject) + responsesEvent(end, 1,
		strings.Replace(responsesObject, `"usage":null`, `"usage":`+usage, 1))
}

func TestOpenAIPhysicalTotalsSurviveDelivery(t *testing.T) {
	for _, upstream := range []protocol.Format{protocol.Chat, protocol.Responses} {
		for _, client := range []protocol.Format{upstream, protocol.Chat} {
			for _, broken := range []bool{false, true} {
				for _, aggregated := range []bool{false, true} {
					t.Run(fmt.Sprintf("%v/%v/broken=%t/aggregate=%t", upstream, client, broken, aggregated), func(t *testing.T) {
						checkDelivered(t, upstream, client, broken, aggregated)
					})
				}
			}
		}
	}
}

// checkDelivered: the client reads the upstream's 100 input and 20 output tokens, not the sums of the overlapping items.
func checkDelivered(t *testing.T, upstream, client protocol.Format, broken, aggregated bool) {
	t.Helper()
	const body = `{"input_tokens":100,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":80},"output_tokens":20,"output_tokens_details":{"image_tokens":20}}`
	stream := mappingStream(upstream, body, broken)
	var wire []byte
	if aggregated {
		wire = aggregate(t, client, upstream, strings.NewReader(stream), protocol.NewMemory(0), settledFinal).body
	} else {
		wire = relayTo(t, client, upstream, strings.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled).out
	}
	input, output := "input_tokens", "output_tokens"
	if client == protocol.Chat {
		input, output = "prompt_tokens", "completion_tokens"
	}
	for _, field := range []string{fmt.Sprintf(`"%s":100`, input), fmt.Sprintf(`"%s":20`, output)} {
		if !strings.Contains(string(wire), field) {
			t.Errorf("missing %s in %s", field, wire)
		}
	}
}
