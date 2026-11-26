package billing

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// tokenPrices prices every token item apart, in billionths of a dollar a token, so a count charged as another item
// shows in the total: input 1, cache read 2, cache write 3, an hour's cache write 4, audio in 5, image in 6, output 7,
// reasoning 8, audio out 9, image out 10.
const tokenPrices = `{"v":1,"currency":"USD","rates":{"input":"0.000000001","cache_read":"0.000000002",
	"cache_write":"0.000000003","cache_write_1h":"0.000000004","audio_in":"0.000000005","image_in":"0.000000006",
	"output":"0.000000007","reasoning":"0.000000008","audio_out":"0.000000009","image_out":"0.00000001"}}`

// TestTokenItemsArePricedByTheirOwnItem: for each format, the usage an upstream reports, read by the relay the gateway
// runs and settled by billing, charges cache reads and writes, reasoning and the image and audio splits each at its
// own item's price, and the plain input and output only for what is left of the totals.
func TestTokenItemsArePricedByTheirOwnItem(t *testing.T) {
	t.Parallel()
	recorded := func(path string) string {
		b, err := os.ReadFile("../protocol/testdata/" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	replace := func(s, old, new string) string {
		if !strings.Contains(s, old) {
			t.Fatalf("the recorded stream has no %q", old)
		}
		return strings.Replace(s, old, new, 1)
	}
	chat := replace(recorded("chat/text/upstream.sse"), "data: [DONE]",
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-6-astra","choices":[],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,`+
			`"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":10,"audio_tokens":5,"image_tokens":7},`+
			`"completion_tokens_details":{"reasoning_tokens":15,"audio_tokens":4,"image_tokens":3}}}`+"\n\ndata: [DONE]")
	responses := replace(recorded("responses/text/upstream.sse"),
		`"usage":{"input_tokens":37,"output_tokens":11,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":48,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`,
		`"usage":{"input_tokens":100,"output_tokens":50,"output_tokens_details":{"reasoning_tokens":15,"audio_tokens":4,"image_tokens":3},"total_tokens":150,`+
			`"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":10,"audio_tokens":5,"image_tokens":7}}`)
	messages := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,` +
		`"usage":{"input_tokens":58,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20},"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Say hi."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`
	// The OpenAI formats split both totals: 100 in is 20 cache read, 10 cache write, 5 audio, 7 image and 58 plain; 50
	// out is 15 reasoning, 4 audio, 3 image and 28 plain. Messages reports its parts beside the input, splits the cache
	// write by its lifetime, and bills thinking as output, which is all it reports of it.
	openAI := int64(58*1 + 20*2 + 10*3 + 5*5 + 7*6 + 28*7 + 15*8 + 4*9 + 3*10)
	for _, c := range []struct {
		name   string
		format protocol.Format
		stream string
		charge int64
	}{
		{"Chat", protocol.Chat, chat, openAI},
		{"Responses", protocol.Responses, responses, openAI},
		{"Messages", protocol.Messages, messages, 58*1 + 20*2 + 10*3 + 20*4 + 50*7},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := relayedUsage(t, c.format, c.stream)
			b := newBank(t, 1_000_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
			entry := metered(t)
			entry.Price, entry.MaxOutput = mustSchedule(t, tokenPrices), 1000
			h := b.holdOn(entry, 10_000)
			segments := make([]Segment, len(u.Segments))
			for i, s := range u.Segments {
				segments[i] = Segment{Usage: s.Usage, Reported: s.Reported}
			}
			f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, segments...))
			if err != nil || f.Estimated || f.Charged != platform.AmountExact(c.charge) {
				t.Errorf("Settle = %+v, %v; want %d billionths, not estimated", f, err, c.charge)
			}
			b.balanced()
		})
	}
}

// relayedUsage is the usage the relay reads from an upstream stream of the format, as the gateway relays it to a
// client of the same format.
func relayedUsage(t *testing.T, f protocol.Format, stream string) protocol.Usage {
	t.Helper()
	r := protocol.NewRelay(f, f, bytes.NewReader([]byte(stream)), protocol.UsageContext{},
		protocol.RelayOptions{ID: "gen-01", Model: "client-name", Created: 1760000000})
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatalf("relay %v: %v", f, err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			u := r.Usage()
			if u.Invalid != nil || len(u.Segments) != 1 {
				t.Fatalf("relay %v: usage %+v", f, u)
			}
			return u
		}
	}
}
