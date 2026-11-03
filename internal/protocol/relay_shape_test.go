package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/textshape"
	"axiom/internal/protocol"
)

// TestRelayTextShape: the relay tallies the shape of the decoded text it counts as content, one event at a time, as the
// whole text would be: deltas cut inside a word or a number, as an upstream's tokens cut them, count no more runs. So
// it does for a client of the upstream's format and for a Chat client of a converted stream alike, and an output the
// upstream does not report is estimated from it at the error measured on whole texts.
func TestRelayTextShape(t *testing.T) {
	t.Parallel()
	deltas := []string{`Hello, wor`, `ld\n20`, `26 \u4f60`, `\u597d\ud83d\ude42 ok`}
	stream := messageStart(`{"input_tokens":3,"output_tokens":1}`) + blockStart
	whole := ""
	for _, d := range deltas {
		stream += `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + d + `"}}` + "\n\n"
		var text string
		if err := jsonx.Unmarshal([]byte(`"`+d+`"`), &text); err != nil {
			t.Fatal(err)
		}
		whole += text
	}
	var want textshape.Shape
	want.Add([]byte(whole))
	stream += blockStop + messageDelta(`{"output_tokens":9}`) + messageStop
	for _, client := range []protocol.Format{protocol.Messages, protocol.Chat} {
		o := options
		o.Memory = protocol.NewMemory(0)
		r := protocol.NewRelay(client, protocol.Messages, strings.NewReader(stream), protocol.UsageContext{}, o)
		stepTo(t, r, protocol.StepUpstreamDone)
		if p := r.Progress(); p.Text != want || p.Text.Bytes != p.ContentBytes {
			t.Errorf("client %d: tallied %+v over %d content bytes, want %+v", client, p.Text, p.ContentBytes, want)
		}
	}
}
