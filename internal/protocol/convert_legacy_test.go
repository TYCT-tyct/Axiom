package protocol_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestConvertLegacyToMessages: Chat's legacy function calling becomes the members that replaced it at the conversion
// entry, and its calls are paired with the results that answer them (README.md「跨格式转换」).
func TestConvertLegacyToMessages(t *testing.T) {
	convertCases(t, protocol.Messages, []convertCase{
		{
			"legacy functions and function_call", chatBody(`"messages":[` + userHi + `],"functions":[{"name":"f","description":"d","parameters":{"type":"object","properties":{}}}],"function_call":"auto"`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"tools":[{"name":"f","description":"d","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"auto"}}`,
		},
		{
			"a legacy choice of a named function", chatBody(`"messages":[` + userHi + `],"functions":[{"name":"f","description":"d","parameters":{"type":"object","properties":{}}}],"function_call":{"name":"f"}`),
			func(t *protocol.Target) { t.ForcedToolChoice = catalog.ClaimYes },
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],"tools":[{"name":"f","description":"d","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"tool","name":"f"}}`,
		},
		{
			"a legacy call and its result", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{\"a\":1}"}},` +
				`{"role":"function","name":"f","content":"done"}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_1","name":"f","input":{"a":1}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_1","content":"done"}]}]}`,
		},
		{
			"legacy calls of one name, answered in the order they were made", chatBody(`"messages":[` + userHi + `,` +
				`{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"one"},` +
				`{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"two"}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_1","content":"one"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_2","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_2","content":"two"}]}]}`,
		},
		{
			"legacy calls made before either is answered", chatBody(`"messages":[` + userHi + `,` +
				`{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},` +
				`{"role":"function","name":"f","content":"one"},{"role":"function","name":"f","content":"two"}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_1","name":"f","input":{}}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_2","name":"f","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_1","content":"one"},{"type":"tool_result","tool_use_id":"call_legacy_2","content":"two"}]}]}`,
		},
		{
			"a legacy ID that a call of the request already uses", chatBody(`"messages":[` + userHi + `,` +
				`{"role":"assistant","tool_calls":[{"id":"call_legacy_1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_legacy_1","content":"done"},` +
				`{"role":"assistant","function_call":{"name":"g","arguments":"{}"}},{"role":"function","name":"g","content":"x"}]`), nil,
			messagesHead + `"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_1","content":"done"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_legacy_2","name":"g","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_legacy_2","content":"x"}]}]}`,
		},
		{"tool_choice and function_call together", chatBody(`"messages":[` + userHi + `],"functions":[{"name":"f"}],"tool_choice":"auto","function_call":"auto"`), nil, "invalid"},
		{"a legacy result without its call", chatBody(`"messages":[` + userHi + `,{"role":"function","name":"f","content":"x"}]`), nil, "unsupported"},
		{"a legacy call where the client called tools too", chatBody(`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}],"function_call":{"name":"g","arguments":"{}"}},` +
			`{"role":"tool","tool_call_id":"c","content":"x"}]`), nil, "invalid"},
		{"a legacy choice of other words", chatBody(`"messages":[` + userHi + `],"function_call":"sometimes"`), nil, "invalid"},
		{"a legacy call without a name", chatBody(`"messages":[` + userHi + `,{"role":"assistant","function_call":{"arguments":"{}"}}]`), nil, "invalid"},
		{"a legacy call outside an assistant message", chatBody(`"messages":[{"role":"user","function_call":{"name":"f","arguments":"{}"}}]`), nil, "invalid"},
		{"legacy arguments must be a string", chatBody(`"messages":[` + userHi + `,{"role":"assistant","function_call":{"name":"f","arguments":{}}}]`), nil, "invalid"},
		{"a legacy result without a name", chatBody(`"messages":[` + userHi + `,{"role":"function","content":"done"}]`), nil, "invalid"},
		{"a legacy call without its result", chatBody(`"messages":[` + userHi + `,{"role":"assistant","function_call":{"name":"f","arguments":"{}"}}]`), nil, "unsupported"},
		{"a legacy result answered twice", chatBody(`"messages":[` + userHi + `,{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},` +
			`{"role":"function","name":"f","content":"one"},{"role":"function","name":"f","content":"two"}]`), nil, "unsupported"},
		{"a legacy function without a name", chatBody(`"messages":[` + userHi + `],"functions":[{"description":"d"}]`), nil, "invalid"},
	})
}

// TestConvertLegacyToResponses: the same legacy members become function tools, a function choice and function calls.
func TestConvertLegacyToResponses(t *testing.T) {
	const head = `{"model":"up","input":[`
	convertCases(t, protocol.Responses, []convertCase{
		{
			"legacy functions and function_call", chatBody(`"messages":[` + userHi + `],"functions":[{"name":"f","description":"d"}],"function_call":{"name":"f"}`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"tools":[{"type":"function","name":"f","description":"d","strict":false}],` +
				`"tool_choice":{"type":"function","name":"f"},"store":false}`,
		},
		{
			"a legacy call and its result", chatBody(`"messages":[` + userHi + `,{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{\"a\":1}"}},` +
				`{"role":"function","name":"f","content":"done"}]`), nil,
			head + `{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},{"type":"function_call","call_id":"call_legacy_1","name":"f","arguments":"{\"a\":1}"},` +
				`{"type":"function_call_output","call_id":"call_legacy_1","output":"done"}],"store":false}`,
		},
	})
}

// Legacy and current spellings must produce the same conversation on either endpoint.
func TestLegacyConversionMatchesTools(t *testing.T) {
	for _, format := range formats[:2] {
		for _, c := range []struct{ name, legacy, current string }{
			{
				"definitions", `"messages":[` + userHi + `],"functions":[{"name":"f"}],"function_call":"\u0061uto"`,
				`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"auto"`,
			},
			{
				"mixed definitions", `"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"g"}}],"functions":[{"name":"f","strict":false}]`,
				`"messages":[` + userHi + `],"tools":[{"type":"function","function":{"name":"g"}},{"type":"function","function":{"name":"f","strict":false}}]`,
			},
			{
				"escaped call name", `"messages":[` + userHi + `,{"role":"assistant","function_call":{"name":"\u0066","arguments":"{}"}},{"role":"function","name":"f","content":"done"}]`,
				`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"call_legacy_1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_legacy_1","content":"done"}]`,
			},
			{
				"explicit result ID", `"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"function","name":"f","tool_call_id":"c","content":"done"}]`,
				`"messages":[` + userHi + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"done"}]`,
			},
		} {
			t.Run(c.name+"/"+format.dir, func(t *testing.T) {
				got, err := convertBody(format.f, chatBody(c.legacy), nil)
				if err != nil {
					t.Fatal(err)
				}
				want, err := convertBody(format.f, chatBody(c.current), nil)
				if err != nil {
					t.Fatal(err)
				}
				sameJSON(t, string(want), got)
			})
		}
	}
}
