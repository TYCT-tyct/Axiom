package protocol_test

import (
	"bytes"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
	"axiom/internal/protocol"
)

const convertedCallID = "01a152cc-b523-7cb6-a0da-e4134209295f"

func TestUnsignedThinkingReplaysToNativeMessages(t *testing.T) {
	for _, signature := range []string{"", `,"signature":""`, `,"signature":null`} {
		body := []byte(`{"model":"m","max_tokens":20,"messages":[{"role":"user","content":"old"},{"role":"assistant","content":[{"type":"thinking","thinking":"lost"` + signature + `},{"type":"text","text":"answer"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"orphan"` + signature + `}]},{"role":"user","content":"next"}]}`)
		r := inspected(t, protocol.Messages, body)
		up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Messages, Model: "up"}, protocol.Rewrites{Identity: derive})
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, `{"model":"up","max_tokens":20,"messages":[{"role":"user","content":"old"},{"role":"assistant","content":[{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`, up.Body)
	}
	const signed = `{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"valid","signature":"opaque"},{"type":"redacted_thinking","data":"encrypted"},{"type":"text","text":"yes"}]}]}`
	body := []byte(signed)
	r := inspected(t, protocol.Messages, body)
	up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Messages, Model: "m"}, protocol.Rewrites{Identity: derive})
	if err != nil || !bytes.Equal(up.Body, body) {
		t.Fatalf("signed reasoning changed: %v\n%s", err, up.Body)
	}
}

func TestConvertedResponsesIDsAreRemovedFromNativeReplay(t *testing.T) {
	for _, compact := range []bool{false, true} {
		body := []byte(`{"model":"m","input":[{"type":"message","id":"` + convertedCallID + `_0","role":"assistant","content":"answer"},{"type":"function_call","id":"` + convertedCallID + `_1","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"reasoning","id":"rs_native","encrypted_content":"opaque","summary":[]},{"role":"user","content":"next"}]}`)
		r := inspected(t, protocol.Responses, body)
		up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Responses, Model: "up", Compact: compact}, protocol.Rewrites{Identity: derive})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(up.Body), convertedCallID) || !strings.Contains(string(up.Body), `"id":"rs_native"`) || !strings.Contains(string(up.Body), `"call_id":"c1"`) {
			t.Fatalf("wrong native identities: %s", up.Body)
		}
	}
	history := [][]byte{
		[]byte(`{"role":"user","content":"old"}`),
		[]byte(`{"id":"` + convertedCallID + `_0","type":"message","role":"assistant","content":"answer"}`),
	}
	before := bytes.Clone(history[1])
	body := []byte(`{"model":"m","previous_response_id":"last","input":"next"}`)
	r := inspected(t, protocol.Responses, body)
	up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Responses, Model: "up"}, protocol.Rewrites{Identity: derive, History: history})
	if err != nil || strings.Contains(string(up.Body), convertedCallID) || !bytes.Equal(history[1], before) {
		t.Fatalf("history replay or caller history changed: %v\n%s", err, up.Body)
	}
}

func TestConvertedOutputCanReturnToNativeEndpoints(t *testing.T) {
	for _, format := range formats[:2] {
		stream := sample(t, "assembled/chat-reasoning-cache.sse")
		whole := aggregate(t, format.f, protocol.Chat, bytes.NewReader(stream), protocol.NewMemory(64<<20), unsettled)
		if whole.err != nil {
			t.Fatal(whole.err)
		}
		var members string
		if format.f == protocol.Messages {
			members = `"messages":[{"role":"assistant","content":` + string(mustMarshal(t, member(t, whole.body, "content"))) + `},{"role":"user","content":"next"}]`
		} else {
			items := string(mustMarshal(t, member(t, whole.body, "output")))
			items = strings.ReplaceAll(items, "gen-01", convertedCallID)
			members = `"input":` + items[:len(items)-1] + `,{"role":"user","content":"next"}]`
		}
		body := []byte(chatBody(members))
		r := inspected(t, format.f, body)
		up, err := protocol.Prepare(&r, body, protocol.Target{Format: format.f, Model: "up"}, protocol.Rewrites{Identity: derive})
		if err != nil || strings.Contains(string(up.Body), convertedCallID) || format.f == protocol.Messages && strings.Contains(string(up.Body), `"thinking"`) {
			t.Fatalf("converted %s output is not native-replayable: %v\n%s", format.dir, err, up.Body)
		}
	}
}

func TestNativeReplayCleanupFitsPrepareCost(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		members := `"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"why"},{"type":"text","text":"yes"}]}]`
		if f == protocol.Responses {
			members = `"input":[{"id":"` + convertedCallID + `_0","role":"assistant","content":"answer"}]`
		}
		t.Run(string(rune('0'+f)), func(t *testing.T) {
			body := []byte(chatBody(members))
			r := inspected(t, f, body)
			target, w := protocol.Target{Format: f, Model: "up"}, protocol.Rewrites{Identity: derive}
			cost := protocol.PrepareCost(&r, body, target, w)
			got := alloctest.Measure(t, alloctest.Function{Name: "prepare", F: func() {
				if _, err := protocol.Prepare(&r, body, target, w); err != nil {
					t.Fatal(err)
				}
			}})
			if got["prepare"].Bytes > float64(cost) {
				t.Fatalf("cleanup allocated %.0f, budget %d", got["prepare"].Bytes, cost)
			}
		})
	}
}
