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
		{protocol.Messages, `"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}]`, "tool_search_tool_"},
		{protocol.Messages, `"tools":[{"type":"tool_search_tool_bm25_20251119","name":"tool_search_tool_bm25"}]`, "tool_search_tool_"},
		{protocol.Messages, `"tools":[{"type":"advisor_20260301","name":"advisor"}]`, "advisor_"},
		{protocol.Responses, `"tools":[{"type":"mcp","server_label":"m","server_url":"https://example.com/mcp"}]`, "mcp"},
		{protocol.Responses, `"tools":[{"type":"image_generation"}]`, "image_generation"},
		{protocol.Responses, `"tools":[{"type":"programmatic_tool_calling"}]`, "programmatic_tool_calling"},
		{protocol.Responses, `"tools":[{"type":"shell","environment":{"type":"container_auto"}}]`, "shell"},
		{protocol.Responses, `"tools":[{"environment":{"type":"container_reference","container_id":"cntr_test"},"type":"shell"}]`, "shell"},
		{protocol.Responses, `"tools":[{"type":"shell"}]`, "shell"},
		{protocol.Responses, `"tools":[{"type":"shell","environment":{"type":"local"}},{"type":"shell"}]`, "shell"},
		{protocol.Responses, `"tools":[{"type":"shell","execution":"client","environment":{"type":"container_auto"}}]`, "shell"},
		{protocol.Responses, `"tools":[{"type":"shell","environment":{"type":"future_hosted"}}]`, "shell"},
		{protocol.Responses, `"tools":[{"type":"tool_search"}]`, "tool_search"},
		{protocol.Responses, `"tools":[{"type":"tool_search","execution":"client"},{"type":"tool_search"}]`, "tool_search"},
		{protocol.Responses, `"tools":[{"type":"tool_search","environment":{"type":"local"}}]`, "tool_search"},
		{protocol.Responses, `"tools":[{"execution":"server","type":"tool_search"}]`, "tool_search"},
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

// TestInspectRefusesOpenRouterServerTools: every server tool OpenRouter documents is recognized by its own name in
// every format and refused before admission, whether or not the request caps its calls; a name the namespace does not
// document is refused as unknown rather than as an unsupported tool.
//
//nolint:cyclop // a table of refusals
func TestInspectRefusesOpenRouterServerTools(t *testing.T) {
	tools := []string{
		"openrouter:web_fetch", "openrouter:datetime", "openrouter:image_generation",
		"openrouter:apply_patch", "openrouter:shell", "openrouter:bash", "openrouter:fusion", "openrouter:advisor",
		"openrouter:subagent", "openrouter:experimental__search_models", "openrouter:tool_search", "openrouter:files",
	}
	for _, format := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
		for _, name := range tools {
			refusedTool(t, format, name)
		}
	}
	// The web search tool is this gateway's to run now: Inspect keeps it for the gateway, which either runs the loop
	// or maps it to the provider's own search, and refuses it only while no way to search is configured.
	req, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","tools":[{"type":"openrouter:web_search"}]}`))
	if err != nil || req.ServerSearch == nil || req.ServerSearch.MaxResults != protocol.DefaultWebSearchResults ||
		req.Needs != protocol.Tools|protocol.WebSearch || req.NativeWeb {
		t.Errorf("openrouter:web_search: %+v, %v", req.ServerSearch, err)
	}
	_, err = protocol.Inspect(protocol.Chat, []byte(`{"model":"m","tools":[{"type":"openrouter:teleport"}]}`))
	if err == nil || !strings.Contains(err.Error(), "not an OpenRouter server tool") {
		t.Errorf("openrouter:teleport: %v, want an unknown-tool refusal", err)
	}
	// The web plugin is read into the request; the gateway refuses it while no backend is configured.
	req, err = protocol.Inspect(protocol.Chat, []byte(`{"model":"m","plugins":[{"id":"web"}]}`))
	if err != nil || req.Web == nil || req.Web.MaxResults != protocol.DefaultWebSearchResults {
		t.Errorf("the web plugin: %+v, %v", req.Web, err)
	}
}

// refusedTool reports whether Inspect refuses the named OpenRouter tool in format, with and without a cap on the
// call's tool calls.
func refusedTool(t *testing.T, format protocol.Format, name string) {
	t.Helper()
	for _, limit := range []string{"", `,"max_tool_calls":2`} {
		body := []byte(`{"model":"m","tools":[{"type":"` + name + `"}]` + limit + `}`)
		_, err := protocol.Inspect(format, body)
		if !errors.Is(err, protocol.ErrInvalid) || !strings.Contains(err.Error(), name+" server tool is not supported") {
			t.Errorf("Inspect(%d, %s) = %v, want a refusal naming %s", format, name, err, name)
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

func TestInspectKeepsClientToolExecution(t *testing.T) {
	for _, tool := range []string{
		`{"type":"shell","environment":{"type":"local"}}`,
		`{"environment":{"type":"local"},"type":"shell"}`,
		`{"type":"tool_search","execution":"client"}`,
		`{"execution":"client","type":"tool_search"}`,
		`{"type":"function","name":"tool_search"}`,
	} {
		req, err := protocol.Inspect(protocol.Responses, []byte(`{"model":"m","tools":[`+tool+`]}`))
		if err != nil || req.Needs != protocol.Tools {
			t.Errorf("Inspect(%s) = %+v, %v", tool, req, err)
		}
	}
	for _, kind := range []string{"bash_20250124", "text_editor_20250728", "memory_20250818", "computer_toolset_20260801", "browser_toolset_20260801"} {
		req, err := protocol.Inspect(protocol.Messages, []byte(`{"model":"m","tools":[{"type":"`+kind+`","name":"local_tool"}]}`))
		if err != nil || req.Needs != protocol.Tools {
			t.Errorf("Inspect(%s) = %+v, %v", kind, req, err)
		}
	}
}
