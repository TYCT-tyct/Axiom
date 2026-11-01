package protocol

import "testing"

func TestServerCaptureChat(t *testing.T) {
	c := newServerCapture(Chat)
	for _, event := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"openrouter_web_search","arguments":"{\"query\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"axiom\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"keep","arguments":"{}"}}]}}]}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	calls := c.serverCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if len(c.calls) != 2 || !c.calls[1].Client {
		t.Fatalf("the client tool was not kept as part of the turn: %+v", c.calls)
	}
}

func TestServerCaptureMessages(t *testing.T) {
	c := newServerCapture(Messages)
	for _, event := range []string{
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"openrouter_web_search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"axiom\"}"}}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if calls := c.serverCalls(); len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestServerCaptureResponses(t *testing.T) {
	c := newServerCapture(Responses)
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"openrouter_web_search","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"query\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"axiom\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"query\":\"axiom\"}"}`,
	} {
		if err := c.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if calls := c.serverCalls(); len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Arguments != `{"query":"axiom"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestServerCaptureIgnoresOtherTools(t *testing.T) {
	c := newServerCapture(Chat)
	if err := c.event([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"keep","arguments":"{}"}}]}}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(c.serverCalls()) != 0 || !c.client || len(c.calls) != 1 {
		t.Fatalf("calls = %+v, client = %v", c.calls, c.client)
	}
}
