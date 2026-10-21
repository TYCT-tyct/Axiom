package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func prepareNativeChat(f protocol.Format, body, model string, history [][]byte) (protocol.Upstream, error) {
	r, err := protocol.Inspect(f, []byte(body))
	if err != nil {
		return protocol.Upstream{}, err
	}
	return protocol.Prepare(&r, []byte(body), protocol.Target{Format: protocol.Chat, Model: model, Streams: true, MaxOutput: 4096},
		protocol.Rewrites{Identity: derive, History: history})
}

// These are whole-request goldens: anything silently dropped or added changes the oracle.
func TestMessagesRequestsToChat(t *testing.T) {
	for _, c := range []struct{ name, members, want string }{
		{
			"system and sampling",
			`"system":"Rules","messages":[{"role":"user","content":"Hi"}],"max_tokens":20,"temperature":0.5,"top_p":0.9,"stop_sequences":["END"],"metadata":{"user_id":"u"},"service_tier":"standard_only"`,
			`"messages":[{"role":"system","content":"Rules"},{"role":"user","content":"Hi"}],"max_tokens":20,"temperature":0.5,"top_p":0.9,"stop":["END"],"user":"h(u)","service_tier":"default"`,
		},
		{
			"tools and forced choice",
			`"messages":[{"role":"user","content":"Go"}],"tools":[{"name":"f","description":"F","input_schema":{"type":"object"},"strict":true}],"tool_choice":{"type":"tool","name":"f","disable_parallel_tool_use":true}`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"f","description":"F","parameters":{"type":"object"},"strict":true}}],"tool_choice":{"type":"function","function":{"name":"f"}},"parallel_tool_calls":false`,
		},
		{
			"history and thinking",
			`"messages":[{"role":"user","content":"Go"},{"role":"assistant","content":[{"type":"thinking","thinking":"why","signature":"opaque"},{"type":"tool_use","id":"c1","name":"f","input":{"x":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"},{"type":"text","text":"next"}]}],"thinking":{"type":"enabled","budget_tokens":1024}`,
			`"messages":[{"role":"user","content":"Go"},{"role":"assistant","content":[],"reasoning_content":"why","tool_calls":[{"type":"function","id":"c1","function":{"name":"f","arguments":"{\"x\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"},{"role":"user","content":[{"type":"text","text":"next"}]}],"max_tokens":4096,"reasoning":{"enabled":true,"max_tokens":1024}`,
		},
		{
			"images and cache marker",
			`"messages":[{"role":"user","content":[{"type":"text","text":"look","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"image","source":{"type":"url","url":"https://example.test/i.png"}}]}]`,
			`"messages":[{"role":"user","content":[{"type":"text","text":"look","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}],"max_tokens":4096`,
		},
		{
			"adaptive effort",
			`"messages":[{"role":"user","content":"Hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`,
			`"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"low","max_tokens":4096`,
		},
		{
			"disabled thinking and any choice",
			`"messages":[{"role":"user","content":"Hi"}],"thinking":{"type":"disabled"},"tool_choice":{"type":"any"}`,
			`"messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"none","tool_choice":"required","max_tokens":4096`,
		},
		{
			"structured output",
			`"messages":[{"role":"user","content":"Hi"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","additionalProperties":false}}}`,
			`"messages":[{"role":"user","content":"Hi"}],"max_tokens":4096,"response_format":{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object","additionalProperties":false},"strict":true}}`,
		},
		{
			"inline document",
			`"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}}]}]`,
			`"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,AAAA"}}]}],"max_tokens":4096`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			up, err := prepareNativeChat(protocol.Messages, chatBody(c.members), "up", nil)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},`+c.want+`}`, up.Body)
		})
	}
}

func BenchmarkRequestsToChat(b *testing.B) {
	for _, format := range formats[:2] {
		b.Run(format.dir, func(b *testing.B) {
			body := sample(b, format.dir+"/text/request.json")
			r, err := protocol.Inspect(format.f, body)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := protocol.Prepare(&r, body, chatTarget(4096), protocol.Rewrites{Identity: derive}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestMessagesEmptyToolResultsToChat(t *testing.T) {
	for _, content := range []string{"", `,"content":null`, `,"content":[ ]`} {
		body := chatBody(`"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1"` + content + `}]}]`)
		up, err := prepareNativeChat(protocol.Messages, body, "up", nil)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},"max_tokens":4096,"messages":[{"role":"assistant","content":[],"tool_calls":[{"type":"function","id":"c1","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":""}]}`, up.Body)
	}
}

func TestResponsesRequestsToChat(t *testing.T) {
	for _, c := range []struct{ name, members, want string }{
		{
			"instructions and identities",
			`"instructions":"Rules","input":"Hi","max_output_tokens":7,"user":"u","safety_identifier":"s","prompt_cache_key":"k","store":true`,
			`"messages":[{"role":"system","content":"Rules"},{"role":"user","content":"Hi"}],"max_tokens":7,"user":"h(u)","safety_identifier":"h(s)","prompt_cache_key":"h(k)"`,
		},
		{
			"tools default to best effort",
			`"input":"Go","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"f"},"parallel_tool_calls":false`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":false}}],"tool_choice":{"type":"function","function":{"name":"f"}},"parallel_tool_calls":false`,
		},
		{
			"implicit best effort preserves nested schemas",
			`"input":"Go","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"rows":{"type":"array","items":{"type":["object","null"],"properties":{"x":{"type":"string"}}}}}}}]`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"f","strict":false,"parameters":{"type":"object","properties":{"rows":{"type":"array","items":{"type":["object","null"],"properties":{"x":{"type":"string"}}}}}}}}]`,
		},
		{
			"explicit best effort keeps the schema",
			`"input":"Go","tools":[{"type":"function","name":"f","strict":false,"parameters":{"type":"object","properties":{"x":{"type":"string","default":"X"}}}}]`,
			`"messages":[{"role":"user","content":"Go"}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"f","strict":false,"parameters":{"type":"object","properties":{"x":{"type":"string","default":"X"}}}}}]`,
		},
		{
			"input items and summary",
			`"input":[{"role":"user","content":"Go"},{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"why"}],"encrypted_content":"opaque"},{"type":"function_call","id":"i1","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]`,
			`"messages":[{"role":"user","content":"Go"},{"role":"assistant","content":"","reasoning_content":"why","tool_calls":[{"type":"function","id":"c1","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}],"max_tokens":4096`,
		},
		{
			"reasoning and schema",
			`"input":"Hi","reasoning":{"effort":"medium","summary":"auto"},"text":{"verbosity":"low","format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":true}}`,
			`"messages":[{"role":"user","content":"Hi"}],"max_tokens":4096,"reasoning_effort":"medium","verbosity":"low","response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"},"strict":true}}`,
		},
		{
			"image detail",
			`"input":[{"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"https://example.test/i.png","detail":"low"}]}]`,
			`"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.test/i.png","detail":"low"}}]}],"max_tokens":4096`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			up, err := prepareNativeChat(protocol.Responses, chatBody(c.members), "up", nil)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},`+c.want+`}`, up.Body)
		})
	}
}

func TestNativeChatRejectsUnrepresentableMembers(t *testing.T) {
	for _, c := range []struct {
		f       protocol.Format
		members string
	}{
		{protocol.Messages, `"messages":[],"top_k":10`},
		{protocol.Messages, `"messages":[],"metadata":{"tenant":"private"}`},
		{protocol.Messages, `"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]`},
		{protocol.Messages, `"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.test/f.pdf"}}]}]`},
		{protocol.Messages, `"messages":[{"role":"user","content":[{"type":"text","text":"hi","citations":[{"x":1}]}]}]`},
		{protocol.Messages, `"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}]}]}]`},
		{protocol.Responses, `"input":"Hi","metadata":{"tenant":"private"}`},
		{protocol.Responses, `"input":"Hi","reasoning":{"summary":"detailed"}`},
		{protocol.Responses, `"input":"Hi","tools":[{"type":"web_search"}]`},
		{protocol.Responses, `"input":[{"type":"item_reference","id":"remote"}]`},
		{protocol.Responses, `"input":[{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"https://example.test/image.png"}]}]`},
		{protocol.Responses, `"input":"Hi","text":{"format":{"type":"json_object","unknown":true}}`},
	} {
		t.Run(c.members, func(t *testing.T) {
			_, err := prepareNativeChat(c.f, chatBody(c.members), "up", nil)
			if ns, _ := errors.AsType[*protocol.NotSupported](err); ns == nil {
				t.Fatalf("want NotSupported, got %v", err)
			}
		})
	}
}

func TestNativeChatCapAndContinuation(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		members := `"messages":[{"role":"user","content":"Hi"}],"max_tokens":9`
		if f == protocol.Responses {
			members = `"input":"Hi","max_output_tokens":9`
		}
		up, err := prepareNativeChat(f, chatBody(members), "o3", nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(up.Body), `"max_tokens"`) || !strings.Contains(string(up.Body), `"max_completion_tokens":9`) {
			t.Fatalf("cap: %s", up.Body)
		}
	}
	body := chatBody(`"previous_response_id":"r","instructions":"new","input":"next"`)
	history := [][]byte{[]byte(`{"role":"user","content":"old"}`), []byte(`{"type":"mess\u0061ge","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[ ]}],"status":"compl\u0065ted"}`)}
	up, err := prepareNativeChat(protocol.Responses, body, "up", history)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, `{"model":"up","stream":true,"stream_options":{"include_usage":true},"max_tokens":4096,"messages":[{"role":"system","content":"new"},{"role":"user","content":"old"},{"role":"assistant","content":[{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`, up.Body)
	if _, err := prepareNativeChat(protocol.Responses, body, "up", nil); err == nil {
		t.Fatal("continuation without resolved history accepted")
	}
}

func TestNativeChatPrepareCostBoundsAllocations(t *testing.T) {
	for _, c := range []struct {
		name, members string
		f             protocol.Format
	}{
		{"messages/long text", `"messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<16) + `"}]`, protocol.Messages},
		{"messages/many turns", `"messages":[` + strings.Repeat(`{"role":"user","content":"a"},`, 1000) + `{"role":"user","content":"b"}]`, protocol.Messages},
		{"responses/long text", `"input":"` + strings.Repeat("x", 1<<16) + `"`, protocol.Responses},
		{"responses/many turns", `"input":[` + strings.Repeat(`{"role":"user","content":"a"},`, 1000) + `{"role":"user","content":"b"}]`, protocol.Responses},
		{"messages/large conversation", `"messages":[` + strings.Repeat(`{"role":"user","content":[{"type":"text","text":"a"}]},`, 10000) + `{"role":"user","content":"b"}]`, protocol.Messages},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(chatBody(c.members))
			r := inspected(t, c.f, body)
			target, w := chatTarget(4096), protocol.Rewrites{Identity: derive}
			cost := protocol.PrepareCost(&r, body, target, w)
			got := alloctest.Measure(t, alloctest.Function{Name: "prepare", F: func() {
				if _, err := protocol.Prepare(&r, body, target, w); err != nil {
					t.Fatal(err)
				}
			}})
			t.Logf("allocated %.0f bytes; budget %d bytes", got["prepare"].Bytes, cost)
			if got["prepare"].Bytes > float64(cost) {
				t.Fatalf("allocated %.0f bytes within a %d-byte budget", got["prepare"].Bytes, cost)
			}
			if float64(cost) > 2*got["prepare"].Bytes {
				t.Fatalf("budget %d exceeds twice the measured %.0f bytes", cost, got["prepare"].Bytes)
			}
		})
	}
}

func TestResponsesToolSchemaKeepsLargeIntegerConstraints(t *testing.T) {
	const bound = "9007199254740993"
	body := chatBody(`"input":"Go","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"n":{"type":"integer","minimum":` + bound + `}}}}]`)
	up, err := prepareNativeChat(protocol.Responses, body, "up", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up.Body), `"minimum":`+bound) {
		t.Fatalf("integer constraint rounded: %s", up.Body)
	}
}

func TestNativeChatUsesJSONValuesRegardlessOfSpelling(t *testing.T) {
	for _, tc := range []struct {
		format  protocol.Format
		members string
	}{
		{protocol.Messages, `"messages":[{"role":"user","content":[{"type":"t\u0065xt","text":"hi"}]}],"service_tier":"stand\u0061rd_only","thinking":{"type":"dis\u0061bled"},"tool_choice":{"type":"\u0061ny"}`},
		{protocol.Responses, `"input":[{"type":"mess\u0061ge","role":"assistant","content":[{"type":"output_t\u0065xt","text":"hi","annotations":[ ]}],"status":"compl\u0065ted"}],"reasoning":{"summary":"\u0061uto"},"tools":[{"type":"funct\u0069on","name":"f","strict":false}]`},
	} {
		body := []byte(chatBody(tc.members))
		canonical, err := jsonx.MarshalCanonical(jsonx.Raw(body))
		if err != nil {
			t.Fatal(err)
		}
		a, err := prepareNativeChat(tc.format, string(body), "up", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := prepareNativeChat(tc.format, string(canonical), "up", nil)
		if err != nil || !bytes.Equal(a.Body, b.Body) {
			t.Fatalf("JSON spelling changed conversion: %v\n%s\n%s", err, a.Body, b.Body)
		}
	}
}
