package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the tools and turns are spliced into the body as written
	"fmt"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// ServerToolName is the function name the model sees for the gateway's web search tool. OpenRouter's server tool
// type, openrouter:web_search, is not a legal function name in any of the three formats (Anthropic allows only
// letters, digits, '-' and '_'), so the declaration the model reads uses this name and the gateway recognizes it in
// the reply.
const ServerToolName = "openrouter_web_search"

// DefaultServerToolCalls is OpenRouter's default and maximum max_tool_calls: one step per tool call, after which the
// model is asked for a final answer with the context it has (OpenRouter《Server Tools》, read 2026-11-01).
const DefaultServerToolCalls = platform.MaxServerToolSteps

// MaxServerResults bounds one engine search's result count. OpenRouter documents 1 to 25 per search; the engines this
// gateway runs return at most ten (websearch.MaxResults), so a larger request gets the engine's ten.
const MaxServerResults = 25

// MaxServerResultBytes bounds one search's text as it enters the conversation. The whole conversation's results share
// ServerResultBudget below, so a long loop cannot grow the body past what the model's context may hold.
const MaxServerResultBytes = 8 << 10

// ServerResultBudget bounds all search results appended to one request. A loop that reaches it stops searching and
// asks the model to answer with what it has (docs/domains/gateway.md「服务端工具」).
const ServerResultBudget = 32 << 10

// MaxServerToolBytes bounds the server tool's parameters. They are small settings, not content.
const MaxServerToolBytes = 4 << 10

// ServerWebSearch is the openrouter:web_search tool as the request declared it. The gateway runs the loop the tool
// stands for, or maps it to the provider's own search tool when the engine is native, and removes the declaration
// from the body before an endpoint sees it.
type ServerWebSearch struct {
	// Engine is "native", "auto" or an enabled backend's name; empty is auto.
	Engine string
	// MaxResults is the results one search asks for, DefaultWebSearchResults when unset.
	MaxResults int
	// MaxUses is how many searches the client allowed, 0 when it set no limit.
	MaxUses int64
	// MaxTotalResults is how many results the whole request allowed, 0 when it set no limit.
	MaxTotalResults int64
	// IncludeDomains and ExcludeDomains narrow every search.
	IncludeDomains, ExcludeDomains []string
	// Loop is set by the gateway when this request takes the model-side loop: each search samples the model again
	// with its own output, which billing reserves for (docs/domains/billing.md「预留与结算」).
	Loop bool
}

// ServerToolCall is one call of the gateway's web search tool as the upstream's stream carried it.
type ServerToolCall struct {
	ID        string
	Arguments string
	Name      string // set when captured; empty for a call built by a test
	Client    bool   // a function the client runs, not the gateway's web search
}

// RemoveServerTool removes the openrouter:web_search declaration from a request's tools array. Everything else is
// copied as written: the body stays the client's, so the positions Inspect recorded remain valid and a conversion
// never sees a tool it cannot express.
func RemoveServerTool(f Format, body []byte) ([]byte, error) {
	value, start, end, ok, err := topMember(body, "tools")
	if err != nil || !ok {
		return body, err
	}
	var tools []jsontext.Value
	if err := jsonx.Unmarshal(value, &tools); err != nil {
		return nil, fmt.Errorf("%w: tools is not an array", ErrInvalid)
	}
	kept, removed := tools[:0], false
	for _, tool := range tools {
		var t struct {
			Type string `json:"type"`
		}
		if err := jsonx.Unmarshal(tool, &t); err != nil {
			return nil, fmt.Errorf("%w: a tool is not an object", ErrInvalid)
		}
		if t.Type == "openrouter:web_search" {
			removed = true
			continue
		}
		kept = append(kept, tool)
	}
	if !removed {
		return body, nil
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:start]...)
	out = append(out, '[')
	for i, tool := range kept {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, tool...)
	}
	out = append(out, ']')
	out = append(out, body[end:]...)
	return out, nil
}

// injectServerFunction adds the gateway's web search function to a body already in the endpoint's format, after any
// conversion. It is what the model calls; the gateway runs the search and samples again.
func injectServerFunction(f Format, body []byte) ([]byte, error) {
	tool, err := serverFunction(f)
	if err != nil {
		return nil, err
	}
	value, start, end, ok, err := topMember(body, "tools")
	if err != nil {
		return nil, err
	}
	if !ok {
		return insertMember(body, "tools", append(append([]byte{'['}, tool...), ']'))
	}
	var tools []jsontext.Value
	if err := jsonx.Unmarshal(value, &tools); err != nil {
		return nil, fmt.Errorf("%w: tools is not an array", ErrInvalid)
	}
	at := end - 1 // the closing ']'
	comma := byte(0)
	if len(tools) > 0 {
		comma = ','
	} else if len(bytes.TrimSpace(value[1:at-start])) > 0 {
		comma = ','
	}
	out := make([]byte, 0, len(body)+len(tool)+1)
	out = append(out, body[:at]...)
	if comma != 0 {
		out = append(out, comma)
	}
	out = append(out, tool...)
	out = append(out, body[at:]...)
	return out, nil
}

// serverFunction is the function declaration in the endpoint format's shape.
func serverFunction(f Format) ([]byte, error) {
	schema, err := jsonx.MarshalCanonical(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "The search query."},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, err
	}
	const description = "Search the web and use the results to answer. Call this when you need current information from the internet."
	quoted, err := jsonx.MarshalCanonical(description)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	//nolint:exhaustive // only the three chat formats carry tools
	switch f {
	case Chat:
		b.WriteString(`{"type":"function","function":{"name":"` + ServerToolName + `","description":`)
		b.Write(quoted)
		b.WriteString(`,"parameters":`)
		b.Write(schema)
		b.WriteString(`}}`)
	case Responses:
		b.WriteString(`{"type":"function","name":"` + ServerToolName + `","description":`)
		b.Write(quoted)
		b.WriteString(`,"parameters":`)
		b.Write(schema)
		b.WriteString(`,"strict":false}`)
	case Messages:
		b.WriteString(`{"name":"` + ServerToolName + `","description":`)
		b.Write(quoted)
		b.WriteString(`,"input_schema":`)
		b.Write(schema)
		b.WriteString(`}`)
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d has no tools", f)}
	}
	return b.Bytes(), nil
}

// AppendServerTurn appends the model's assistant turn and the gateway's results to a client-format body. The turn is
// the one captured from the hop, so text, thinking blocks and signatures survive; if a caller has no captured turn, the
// assistant message is built from the server calls alone. One result follows each server call, in the format's own tool
// shapes: Chat's tool_calls and tool messages, Messages' tool_use and tool_result blocks, Responses' function_call and
// function_call_output items. The body is the client's format, not the endpoint's, because the loop may fall back
// before its first search and because a later hop is prepared again like any other request.
//
//nolint:cyclop,funlen,gocognit // one branch per chat format and per call
func AppendServerTurn(f Format, body []byte, turn ServerTurn, results []string) ([]byte, error) {
	calls := make([]ServerToolCall, 0, len(turn.Calls))
	for _, call := range turn.Calls {
		if !call.Client {
			calls = append(calls, call)
		}
	}
	if len(calls) == 0 && len(turn.Assistant) == 0 {
		return body, nil
	}
	if len(results) != len(calls) {
		return nil, fmt.Errorf("%w: %d calls and %d results", ErrInvalid, len(calls), len(results))
	}
	assistant := append([]byte(nil), turn.Assistant...)
	if len(assistant) == 0 {
		var err error
		assistant, err = serverAssistant(f, calls)
		if err != nil {
			return nil, err
		}
	}
	var following []byte
	//nolint:exhaustive // only the three chat formats carry tools
	switch f {
	case Chat:
		for i, call := range calls {
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			following = appendResult(following, i, `{"role":"tool","tool_call_id":`, id, `,"content":`, results[i], `}`) //nolint:gosec // results has one entry per server call, checked above
		}
	case Messages:
		for i, call := range calls {
			id, err := jsonx.MarshalCanonical(toolUseID(call.ID))
			if err != nil {
				return nil, err
			}
			following = appendResult(following, i, `{"role":"user","content":[{"type":"tool_result","tool_use_id":`, id,
				`,"content":[{"type":"text","text":`, results[i], `}]}]}`) //nolint:gosec // results has one entry per server call, checked above
		}
	case Responses:
		for i, call := range calls {
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			following = appendResult(following, i, `{"type":"function_call_output","call_id":`, id, `,"output":`,
				results[i], `}`) //nolint:gosec // results has one entry per server call, checked above
		}
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d has no tools", f)}
	}

	member := "messages"
	if f == Responses {
		member = "input"
	}
	value, start, end, ok, err := topMember(body, member)
	if err != nil {
		return nil, err
	}
	if f == Responses && ok && jsontext.Value(value).Kind() == '"' {
		// A string input is one user message (OpenAI《Create a model response》, input): keep it as that message.
		msg := make([]byte, 0, len(value)+32)
		msg = append(msg, `[{"role":"user","content":`...)
		msg = append(msg, value...)
		msg = append(msg, `}]`...)
		replaced := make([]byte, 0, len(body)-len(value)+len(msg))
		replaced = append(replaced, body[:start]...)
		replaced = append(replaced, msg...)
		replaced = append(replaced, body[end:]...)
		body = replaced
		value, _, end, ok, err = topMember(body, member)
		if err != nil || !ok {
			return nil, err
		}
	}
	parts, err := serverTurnParts(f, assistant, following)
	if err != nil {
		return nil, err
	}
	if !ok {
		added := append(append([]byte{'['}, parts...), ']')
		return insertMember(body, member, added)
	}
	var elems []jsontext.Value
	if err := jsonx.Unmarshal(value, &elems); err != nil {
		return nil, fmt.Errorf("%w: %s is not an array", ErrInvalid, member)
	}
	at := end - 1
	out := make([]byte, 0, len(body)+len(parts)+2)
	out = append(out, body[:at]...)
	if len(elems) > 0 {
		out = append(out, ',')
	}
	out = append(out, parts...)
	out = append(out, body[at:]...)
	return out, nil
}

// serverTurnParts produces the array elements to append to the conversation. Responses' captured turn is already an
// array of output items: its elements are flattened into input, never inserted as one nested array. The other formats
// have one assistant object followed by their tool results.
func serverTurnParts(f Format, assistant, following []byte) ([]byte, error) {
	if f != Responses {
		if len(following) == 0 {
			return assistant, nil
		}
		out := make([]byte, 0, len(assistant)+len(following)+1)
		out = append(out, assistant...)
		out = append(out, ',')
		return append(out, following...), nil
	}
	var items []jsontext.Value
	if err := jsonx.Unmarshal(assistant, &items); err != nil {
		return nil, fmt.Errorf("%w: the Responses assistant turn is not an array", ErrInvalid)
	}
	out := make([]byte, 0, len(assistant)+len(following)+1)
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	if len(following) > 0 {
		if len(items) > 0 {
			out = append(out, ',')
		}
		out = append(out, following...)
	}
	return out, nil
}

// appendResult appends one tool result, quoted, with a comma before it when it is not the first.
func appendResult(dst []byte, i int, prefix string, id []byte, middle, text, suffix string) []byte {
	if i > 0 {
		dst = append(dst, ',')
	}
	dst = append(dst, prefix...)
	dst = append(dst, id...)
	dst = append(dst, middle...)
	quoted, err := jsonx.MarshalCanonical(text)
	if err != nil {
		panic(err) // a Go string always encodes
	}
	dst = append(dst, quoted...)
	return append(dst, suffix...)
}
