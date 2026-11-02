package protocol_test

import (
	"bytes"
	"testing"

	"axiom/internal/protocol"
)

func TestControlNormalizationKeepsPayloadBytes(t *testing.T) {
	const body = `"messages":[{"role":"user","content":[{"type":"te\u0078t","text":"\u0048i"}]},` +
		`{"role":"assistant","reasoning_details":[{"type":"reasoning.text","format":"anthropic-claude-v1","text":"\u0068m","signature":"\u0053"}],` +
		`"function_call":{"name":"f","arguments":"{ \"q\" : \"\\u0061\" }"}},` +
		`{"role":"function","name":"f","content":"\u0064one"}],` +
		`"functions":[{"name":"f"}],"function_call":"\u0061uto","modalities":[ "te\u0078t" ]`
	for _, c := range []struct {
		name string
		to   protocol.Format
		want []string
	}{
		{
			"messages", protocol.Messages,
			[]string{`"text":"\u0048i"`, `"thinking":"\u0068m"`, `"signature":"\u0053"`, `"input":{ "q" : "\u0061" }`, `"content":"\u0064one"`},
		},
		{
			"responses", protocol.Responses,
			[]string{`"text":"\u0048i"`, `"arguments":"{ \"q\" : \"\\u0061\" }"`, `"output":"\u0064one"`},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := convertBody(c.to, chatBody(body), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range c.want {
				if !bytes.Contains(got, []byte(want)) {
					t.Errorf("payload %s changed: %s", want, got)
				}
			}
		})
	}
}

func TestPrepareChatUpgradesLegacyBytes(t *testing.T) {
	const members = `"messages":[{"role":"user","content":"\u0048i"}],"functions":[{"name":"\u0066"}],"function_call":"\u0061uto"`
	body := []byte(chatBody(members))
	r := inspected(t, protocol.Chat, body)
	u, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Chat, Model: "up"}, rewrites())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(u.Body, []byte(`"content":"\u0048i"`)) ||
		!bytes.Contains(u.Body, []byte(`"tools":[{"type":"function","function":{"name":"\u0066"}}]`)) ||
		!bytes.Contains(u.Body, []byte(`"tool_choice":"\u0061uto"`)) || bytes.Contains(u.Body, []byte(`"functions":`)) {
		t.Fatalf("same-format legacy fields changed: %s", u.Body)
	}
}
