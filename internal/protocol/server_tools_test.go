package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

func TestInspectRefusesUnpricedServerTools(t *testing.T) {
	for _, tc := range []struct {
		format       protocol.Format
		member, name string
	}{
		{protocol.Messages, `"tools":[{"type":"web_fetch_20250910","name":"web_fetch","max_uses":2}]`, "web_fetch_"},
		{protocol.Messages, `"tools":[{"type":"web_fetch_20260318","name":"web_fetch"}]`, "web_fetch_"},
		{protocol.Messages, `"mcp_servers":[{"type":"url","url":"https://example.com/mcp","name":"m"}]`, "mcp_servers"},
		{protocol.Messages, `"tools":[{"type":"mcp_toolset","mcp_server_name":"m"}]`, "mcp_toolset"},
		{protocol.Responses, `"tools":[{"type":"mcp","server_label":"m","server_url":"https://example.com/mcp"}]`, "mcp"},
		{protocol.Responses, `"tools":[{"type":"file_search","vector_store_ids":["vs_test"]}]`, "file_search"},
		{protocol.Responses, `"tools":[{"type":"image_generation"}]`, "image_generation"},
	} {
		for _, limit := range []string{"", `,"max_tool_calls":2`} {
			body := []byte(`{"model":"m",` + tc.member + limit + `}`)
			_, err := protocol.Inspect(tc.format, body)
			if !errors.Is(err, protocol.ErrInvalid) || !strings.Contains(err.Error(), tc.name+" server tool is not supported") && !strings.Contains(err.Error(), tc.name+" is not supported") {
				t.Errorf("Inspect(%s) = %v, want a named unsupported tool", body, err)
			}
		}
	}
}

// These members were all copied by same-format Prepare before admission learned to refuse them. Keep this probe
// independent of Inspect: the native rewriter is deliberately not a second request-policy implementation.
func TestPrepareCopiesNativeServerTools(t *testing.T) {
	for _, tc := range []struct {
		format protocol.Format
		member string
	}{
		{protocol.Messages, `"tools":[{"type":"web_fetch_20250910","max_uses":2}]`},
		{protocol.Messages, `"mcp_servers":[{"type":"url","url":"https://example.com/mcp","name":"m"}]`},
		{protocol.Responses, `"tools":[{"type":"mcp","server_label":"m"}]`},
		{protocol.Responses, `"tools":[{"type":"file_search","vector_store_ids":["vs_test"]}]`},
		{protocol.Responses, `"tools":[{"type":"image_generation"}]`},
	} {
		body := []byte(`{"model":"m",` + tc.member + `}`)
		r := protocol.Request{Format: tc.format, Model: "m"}
		u, err := protocol.Prepare(&r, body, protocol.Target{Format: tc.format, Model: "up"}, protocol.Rewrites{})
		if err != nil || !strings.Contains(string(u.Body), tc.member) {
			t.Errorf("Prepare(%s) = %s, %v", body, u.Body, err)
		}
	}
}

func TestInspectEmptyMCPServersAndClientToolNames(t *testing.T) {
	for _, member := range []string{`"mcp_servers":[]`, `"mcp_servers":null`, `"tools":[{"name":"web_fetch","input_schema":{"type":"object"}}]`} {
		if _, err := protocol.Inspect(protocol.Messages, []byte(`{"model":"m",`+member+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := protocol.Inspect(protocol.Responses, []byte(`{"model":"m","tools":[{"type":"function","name":"mcp"}]}`)); err != nil {
		t.Fatal(err)
	}
}
