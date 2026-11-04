package protocol_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/alloctest"
	"axiom/internal/protocol"
)

func TestPrepareLegacyChat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fields string
		legacy       bool
		want         []string
	}{
		{"definitions", `"messages":[],"functions":[{"name":"f","parameters":{ "x" : "\u0061" }}],"function_call":{"name":"f"}`, true, []string{`"tools":[{"type":"function","function":{"name":"f","parameters":{ "x" : "\u0061" }}}]`, `"tool_choice":{"type":"function","function":{"name":"f"}}`, `"parallel_tool_calls":false`}},
		{"escaped", `"messages":[],"funct\u0069ons":[{"name":"f"}],"function_c\u0061ll":"\u0061uto","tool_choice":null,"tools":null`, true, []string{`"tool_choice":"\u0061uto"`, `"tools":[{"type":"function","function":{"name":"f"}}]`}},
		{"mixed", `"messages":[],"tools":[{"type":"function","function":{"name":"g"},"cache_control":{"type":"ephemeral"}}],"functions":[{"name":"f"}]`, false, []string{`"tools":[{"type":"function","function":{"name":"g"},"cache_control":{"type":"ephemeral"}},{"type":"function","function":{"name":"f"}}]`}},
		{"empty tools", `"messages":[],"tools":[ ],"functions":[{"name":"f"}],"parallel_tool_calls":true`, true, []string{`"parallel_tool_calls":false`}},
		{"null tool calls", `"messages":[{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{}"},"tool_calls":null},{"role":"function","name":"f","content":"done"}]`, true, []string{`"tool_calls":[{"id":"call_legacy_1"`}},
		{"empty tool calls", `"messages":[{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{}"},"tool_calls":[]},{"role":"function","name":"f","content":"done"}]`, true, []string{`"tool_calls":[{"id":"call_legacy_1"`}},
		{"history only", `"messages":[{"role":"assistant","function_call":{"name":"f","arguments":"{ \"q\":\"\\u0061\" }"}},{"role":"funct\u0069on","name":"f"}]`, true, []string{`"arguments":"{ \"q\":\"\\u0061\" }"`, `"role":"tool","tool_call_id":"call_legacy_1"`}},
		{"id collision", `"messages":[{"role":"assistant","tool_calls":[{"id":"call_legacy_1","type":"function","function":{"name":"g","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_legacy_1","content":"\u004fK"},{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"one"},{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"two"}]`, true, []string{`"tool_call_id":"call_legacy_2"`, `"tool_call_id":"call_legacy_3"`, `"content":"\u004fK"`}},
		{"modern", `"messages":[{"role":"user","content":"functions function_call"}],"tools":[{"type":"function","function":{"name":"f"}}]`, false, []string{`"content":"functions function_call"`}},
		{"null legacy", `"messages":[],"functions":null,"function_call":null`, false, nil},
		{"empty legacy", `"messages":[],"functions":[]`, false, nil},
		{"nested payload", `"messages":[{"role":"user","content":[{"type":"text","text":"x","function_call":{"name":"opaque"},"role":"function"}]}]`, false, []string{`"function_call":{"name":"opaque"},"role":"function"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"m",` + tc.fields + `,"vendor":{"signature":"\u0053"}}`)
			r := inspected(t, protocol.Chat, body)
			target := protocol.Target{Format: protocol.Chat, Model: "up", Streams: true}
			u, err := protocol.Prepare(&r, body, target, rewrites())
			if err != nil {
				t.Fatal(err)
			}
			checkPreparedLegacy(t, u, tc.legacy, tc.want)

			inspected(t, protocol.Chat, u.Body)
			if tc.legacy || tc.name == "mixed" {
				checkLegacyPrepareCost(t, &r, body, target)
			}
		})
	}
}

func TestPrepareLegacyRejectsConflicts(t *testing.T) {
	t.Parallel()
	for _, fields := range []string{
		`"function_call":"auto","tool_choice":"none"`,
		`"messages":[{"role":"assistant","function_call":{"name":"f","arguments":{}}}]`,
		`"messages":[{"role":"assistant","function_call":{"name":"f","arguments":"{}"},"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]`,
		`"functions":[{"description":"missing name"}]`,
	} {
		t.Run(fmt.Sprint(fields), func(t *testing.T) {
			body := []byte(`{"model":"m",` + fields + `}`)
			r := inspected(t, protocol.Chat, body)
			_, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Chat, Model: "u"}, rewrites())
			if !errors.Is(err, protocol.ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestControlSpellings(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{`"auto"`, `"\u0061uto"`, `"none"`, `"n\u006fne"`, `"required"`, `"requir\u0065d"`} {
		modern := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(choice, `\u0061`, `a`), `\u006f`, `o`), `\u0065`, `e`)
		for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
			a, err := convertBody(f, chatBody(`"messages":[`+userHi+`],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":`+choice), func(t *protocol.Target) { t.ForcedToolChoice = catalog.ClaimYes })
			if err != nil {
				t.Fatal(err)
			}
			b, err := convertBody(f, chatBody(`"messages":[`+userHi+`],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":`+modern), func(t *protocol.Target) { t.ForcedToolChoice = catalog.ClaimYes })
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, string(b), a)
		}
	}
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		body := []byte(`{ "model":"m", "str\u0065am" : true , "messages":[] }`)
		r := inspected(t, f, body)
		u, err := protocol.Prepare(&r, body, protocol.Target{Format: f, Model: "u", Streams: true}, rewrites())
		if err != nil || !bytes.Contains(u.Body, []byte(`"str\u0065am" : true`)) {
			t.Fatalf("%s: %v", u.Body, err)
		}
	}
}

func checkPreparedLegacy(t *testing.T, u protocol.Upstream, legacy bool, wants []string) {
	t.Helper()
	body := decode(t, u.Body)
	if _, tools := body["tools"]; legacy && !tools {
		if _, parallel := body["parallel_tool_calls"]; parallel {
			t.Fatalf("parallel switch without tools: %s", u.Body)
		}
	}
	if u.Usage.LegacyFunctions != legacy {
		t.Fatalf("legacy=%t, want %t: %s", u.Usage.LegacyFunctions, legacy, u.Body)
	}
	for _, want := range append(wants, `"vendor":{"signature":"\u0053"}`) {
		if !bytes.Contains(u.Body, []byte(want)) {
			t.Errorf("missing %s: %s", want, u.Body)
		}
	}
	if legacy && strings.Contains(string(u.Body), `"parallel_tool_calls":true`) {
		t.Fatalf("parallel calls enabled: %s", u.Body)
	}
}

func checkLegacyPrepareCost(t *testing.T, r *protocol.Request, body []byte, target protocol.Target) {
	t.Helper()
	cost := protocol.PrepareCost(r, body, target, rewrites())
	got := alloctest.Measure(t, alloctest.Function{Name: "prepare", F: func() {
		_, err := protocol.Prepare(r, body, target, rewrites())
		if err != nil {
			t.Fatal(err)
		}
	}})
	if got["prepare"].Bytes > float64(cost) {
		t.Fatalf("allocated %.0f, cost %d", got["prepare"].Bytes, cost)
	}
}
