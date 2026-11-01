package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestRelayCountsServerToolCalls: the calls an upstream does not count in its usage are counted from the reply's own
// call events — OpenAI's file_search_call and code_interpreter_call output items, and Messages' code_execution
// server_tool_use block — once each: a done event for an item counts nothing more, and a client tool named
// code_execution is not a server tool.
func TestRelayCountsServerToolCalls(t *testing.T) {
	created := responsesEvent("response.created", 0, responsesObject)
	fileSearch := `,"output_index":0,"item":{"id":"fs_1","type":"file_search_call","status":"completed","queries":["q"],"results":[]}`
	codeInterpreter := `,"output_index":1,"item":{"id":"ci_1","type":"code_interpreter_call","status":"completed","code":"1","container_id":"c","outputs":[]}`
	completed := responsesEvent("response.completed", 4, strings.Replace(responsesObject, `"usage":null`,
		`"usage":{"input_tokens":5,"output_tokens":3}`, 1))
	responses := created +
		responsesEvent("response.output_item.added", 1, fileSearch) +
		responsesEvent("response.output_item.done", 2, fileSearch) +
		responsesEvent("response.output_item.added", 3, codeInterpreter) +
		completed
	got := relay(t, protocol.Responses, strings.NewReader(responses), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if p := got.r.Progress(); got.err != nil || p.FileSearches != 1 || p.CodeExecutions != 1 {
		t.Errorf("Responses calls: file %d, code %d, %v; want one of each", p.FileSearches, p.CodeExecutions, got.err)
	}

	serverTool := "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\"," +
		"\"id\":\"srvtoolu_1\",\"name\":\"code_execution\",\"input\":{}}}\n\n"
	clientTool := "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\"," +
		"\"id\":\"toolu_1\",\"name\":\"code_execution\",\"input\":{}}}\n\n"
	messages := messageStart(`{"input_tokens":5,"output_tokens":1}`) + serverTool +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" + clientTool +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" + messageDelta(`{"output_tokens":3}`) + messageStop
	got = relay(t, protocol.Messages, strings.NewReader(messages), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if p := got.r.Progress(); got.err != nil || p.CodeExecutions != 1 {
		t.Errorf("Messages calls: code %d, %v; want the server tool's one", p.CodeExecutions, got.err)
	}
}
