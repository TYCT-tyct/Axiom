package protocol

import (
	"bytes"
	"encoding/json"          //nolint:depguard // the tool's small parameters are decoded, not edited in place
	"encoding/json/jsontext" //nolint:depguard // the tools and turns are spliced into the body as written
	"fmt"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// ServerToolName is the function name the model sees for the gateway's web search tool. OpenRouter's server tool
// type, openrouter:web_search, is not a legal function name in any of the three formats (Anthropic allows only
// letters, digits, '-' and '_'), so the declaration the model reads uses this name and the gateway recognizes it in
// the reply.
const ServerToolName = "openrouter_web_search"

// DefaultServerToolCalls is OpenRouter's default and maximum max_tool_calls: one step per tool call, after which the
// model is asked for a final answer with the context it has (OpenRouter《Server Tools》, read 2026-11-01).
const DefaultServerToolCalls = 30

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

// AppendServerTurn appends the model's tool calls and the gateway's results to a client-format body. One assistant turn
// carries every call of the step, and one result follows each, in the format's own tool shapes: Chat's tool_calls and
// tool messages, Messages' tool_use and tool_result blocks, Responses' function_call and function_call_output items.
// The body is the client's format, not the endpoint's, because the loop may fall back before its first search and
// because a later hop is prepared again like any other request.
//
//nolint:cyclop,funlen,gocognit // one branch per chat format and per call
func AppendServerTurn(f Format, body []byte, calls []ServerToolCall, results []string) ([]byte, error) {
	if len(calls) == 0 {
		return body, nil
	}
	if len(results) != len(calls) {
		return nil, fmt.Errorf("%w: %d calls and %d results", ErrInvalid, len(calls), len(results))
	}
	var assistant, following []byte
	//nolint:exhaustive // only the three chat formats carry tools
	switch f {
	case Chat:
		assistant = append(assistant, `{"role":"assistant","content":null,"tool_calls":[`...)
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			args, err := jsonx.MarshalCanonical(serverArguments(call.Arguments))
			if err != nil {
				return nil, err
			}
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			assistant = append(assistant, `{"id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"type":"function","function":{"name":"`+ServerToolName+`","arguments":`...)
			assistant = append(assistant, args...)
			assistant = append(assistant, `}}`...)
			following = appendResult(following, i, `{"role":"tool","tool_call_id":`, id, `,"content":`, results[i], `}`)
		}
		assistant = append(assistant, `]}`...)
	case Messages:
		assistant = append(assistant, `{"role":"assistant","content":[`...)
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			id, err := jsonx.MarshalCanonical(toolUseID(call.ID))
			if err != nil {
				return nil, err
			}
			input := jsontext.Value(serverArguments(call.Arguments))
			assistant = append(assistant, `{"type":"tool_use","id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"name":"`+ServerToolName+`","input":`...)
			assistant = append(assistant, input...)
			assistant = append(assistant, '}')
			following = appendResult(following, i, `{"role":"user","content":[{"type":"tool_result","tool_use_id":`, id,
				`,"content":[{"type":"text","text":`, results[i], `}]}]}`)
		}
		assistant = append(assistant, `]}`...)
	case Responses:
		for i, call := range calls {
			if i > 0 {
				assistant = append(assistant, ',')
			}
			id, err := jsonx.MarshalCanonical(call.ID)
			if err != nil {
				return nil, err
			}
			args, err := jsonx.MarshalCanonical(serverArguments(call.Arguments))
			if err != nil {
				return nil, err
			}
			assistant = append(assistant, `{"type":"function_call","call_id":`...)
			assistant = append(assistant, id...)
			assistant = append(assistant, `,"name":"`+ServerToolName+`","arguments":`...)
			assistant = append(assistant, args...)
			assistant = append(assistant, '}')
			following = appendResult(following, i, `{"type":"function_call_output","call_id":`, id, `,"output":`,
				results[i], `}`)
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
	if !ok {
		added := append(append(append([]byte{'['}, assistant...), ','), following...)
		added = append(added, ']')
		return insertMember(body, member, added)
	}
	var elems []jsontext.Value
	if err := jsonx.Unmarshal(value, &elems); err != nil {
		return nil, fmt.Errorf("%w: %s is not an array", ErrInvalid, member)
	}
	at := end - 1
	out := make([]byte, 0, len(body)+len(assistant)+len(following)+2)
	out = append(out, body[:at]...)
	if len(elems) > 0 {
		out = append(out, ',')
	}
	out = append(out, assistant...)
	out = append(out, ',')
	out = append(out, following...)
	out = append(out, body[at:]...)
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

// topMember finds a top-level member's raw value and the byte span it occupies. It walks the body's tokens, so a
// member of the same name inside a message is never the one found.
//
//nolint:cyclop // a token walk needs a branch per malformed shape
func topMember(body []byte, member string) (value []byte, start, end int, ok bool, err error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if k := dec.PeekKind(); k != '{' {
		return nil, 0, 0, false, fmt.Errorf("%w: the request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
		}
		name := key.String()
		from := int(dec.InputOffset())
		if err := dec.SkipValue(); err != nil {
			return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
		}
		if name != member {
			continue
		}
		to := int(dec.InputOffset())
		colon := bytes.IndexByte(body[from:to], ':')
		if colon < 0 {
			return nil, 0, 0, false, fmt.Errorf("%w: %s has no value", ErrInvalid, member)
		}
		at := from + colon + 1
		for at < to && (body[at] == ' ' || body[at] == '\t' || body[at] == '\r' || body[at] == '\n') {
			at++
		}
		return body[at:to], at, to, true, nil
	}
	return nil, 0, 0, false, nil
}

// insertMember adds a top-level member before the body's closing brace. The body has no such member.
func insertMember(body []byte, member string, value []byte) ([]byte, error) {
	at := len(bytes.TrimRight(body, " \t\r\n")) - 1
	if at < 1 || body[at] != '}' || bytes.TrimSpace(body[:at])[0] != '{' {
		return nil, fmt.Errorf("%w: the request is not an object", ErrInvalid)
	}
	out := make([]byte, 0, len(body)+len(member)+len(value)+4)
	out = append(out, body[:at]...)
	if len(bytes.TrimSpace(body[1:at])) > 0 {
		out = append(out, ',')
	}
	out = append(out, '"')
	out = append(out, member...)
	out = append(out, `":`...)
	out = append(out, value...)
	out = append(out, body[at:]...)
	return out, nil
}

// parseServerParams reads and validates the openrouter:web_search parameters. A parameter this gateway cannot express
// is refused rather than ignored: the model would otherwise run a search the client did not ask for.
//
//nolint:cyclop,funlen,gocognit // one case per documented parameter
func parseServerParams(v []byte) (*ServerWebSearch, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v, &raw); err != nil {
		return nil, fmt.Errorf("%w: the web search parameters are not an object", ErrInvalid)
	}
	t := &ServerWebSearch{MaxResults: DefaultWebSearchResults}
	for name, value := range raw {
		switch name {
		case "engine":
			if err := json.Unmarshal(value, &t.Engine); err != nil {
				return nil, fmt.Errorf("%w: the web search engine is not a string", ErrInvalid)
			}
			if t.Engine != "" && t.Engine != "auto" && t.Engine != "native" && !ValidEngineName(t.Engine) {
				return nil, fmt.Errorf("%w: %q is not a search engine name", ErrInvalid, t.Engine)
			}
		case "max_results":
			n, err := serverCount(value)
			if err != nil {
				return nil, err
			}
			if n < 1 || n > MaxServerResults {
				return nil, fmt.Errorf("%w: max_results is between 1 and %d", ErrInvalid, MaxServerResults)
			}
			t.MaxResults = int(n)
		case "max_uses":
			n, err := serverCount(value)
			if err != nil {
				return nil, err
			}
			if n < 1 || n > DefaultServerToolCalls {
				return nil, fmt.Errorf("%w: max_uses is between 1 and %d", ErrInvalid, DefaultServerToolCalls)
			}
			t.MaxUses = n
		case "max_total_results":
			n, err := serverCount(value)
			if err != nil {
				return nil, err
			}
			if n < 1 || n > 1000 {
				return nil, fmt.Errorf("%w: max_total_results is between 1 and 1000", ErrInvalid)
			}
			t.MaxTotalResults = n
		case "allowed_domains":
			list, err := serverDomains(value)
			if err != nil {
				return nil, err
			}
			t.IncludeDomains = list
		case "excluded_domains":
			list, err := serverDomains(value)
			if err != nil {
				return nil, err
			}
			t.ExcludeDomains = list
		case "mode", "search_context_size", "max_characters", "user_location", "x_search":
			return nil, fmt.Errorf("%w: the web search parameter %q is not supported yet", ErrInvalid, name)
		default:
			return nil, fmt.Errorf("%w: %q is not an OpenRouter web search parameter", ErrInvalid, name)
		}
	}
	return t, nil
}

// serverCount reads a non-negative count.
func serverCount(v json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, fmt.Errorf("%w: a web search count is not an integer", ErrInvalid)
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: a web search count is negative", ErrInvalid)
	}
	return n, nil
}

// serverDomains reads one domain list. A list is a narrowing hint, so a malformed entry is a request error rather than
// a silently ignored filter.
func serverDomains(v json.RawMessage) ([]string, error) {
	var list []string
	if err := json.Unmarshal(v, &list); err != nil {
		return nil, fmt.Errorf("%w: a web search domain list is not an array of strings", ErrInvalid)
	}
	if len(list) > 20 {
		return nil, fmt.Errorf("%w: a web search domain list holds at most 20 entries", ErrInvalid)
	}
	for _, d := range list {
		if d == "" || len(d) > 253 || !utf8.ValidString(d) {
			return nil, fmt.Errorf("%w: a web search domain is empty or too long", ErrInvalid)
		}
	}
	return list, nil
}

// ServerFunctionDeclared reports whether the body already declares a function with the gateway's web search name.
// Running the loop would otherwise make the model's call ambiguous: the client's function and the gateway's would
// share one name, and the result the gateway appends would go to a tool the client meant to run itself.
func ServerFunctionDeclared(f Format, body []byte) bool {
	value, _, _, ok, err := topMember(body, "tools")
	if err != nil || !ok {
		return false
	}
	var tools []struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if jsonx.Unmarshal(value, &tools) != nil {
		return false
	}
	for _, t := range tools {
		name := t.Name
		if f == Chat {
			name = t.Function.Name
		}
		if name == ServerToolName {
			return true
		}
	}
	return false
}

// serverArguments keeps a malformed model call from making the history invalid: an arguments string that is not a
// JSON object is replaced with an empty one, the same as Messages' input.
func serverArguments(args string) string {
	if v := jsontext.Value(args); v.Kind() == '{' && jsonx.Validate(v) == nil {
		return args
	}
	return "{}"
}
