package protocol

import (
	"bytes"
	"encoding/json"          //nolint:depguard // the tools array is decoded into a small typed value, not edited in place
	"encoding/json/jsontext" //nolint:depguard // the body is edited in place, keeping unknown members as written
	"fmt"
	"strconv"
)

// Prepare prepares the request and, when the web plugin chose the provider's own search, adds that search to the body
// the endpoint's format expects. The tool goes in after conversion, so the body it edits is the one that is sent.
//
//nolint:cyclop,gocognit // one branch per format and rewrite
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) {
	u, err := prepare(r, body, t, w)
	if err != nil {
		return u, err
	}
	if w.ServerLoop {
		if u.Body, err = RemoveMaxToolCalls(u.Body); err != nil {
			return u, err
		}
		if !w.ServerFunction {
			if u.Body, err = DropForcedToolChoice(t.Format, u.Body); err != nil {
				return u, err
			}
		}
	} else if w.HasMaxToolCalls && (t.Format == Chat || t.Format == Messages) {
		if u.Body, err = RemoveMaxToolCalls(u.Body); err != nil {
			return u, err
		}
	}
	if w.ServerFunction {
		if u.Body, err = injectServerFunction(t.Format, u.Body); err != nil {
			return u, err
		}
	}
	if w.NativeSearch <= 0 {
		return u, nil
	}
	if u.Body, err = injectNativeSearch(t.Format, u.Body, w.NativeSearch); err != nil {
		return u, err
	}
	return u, nil
}

// NativeSearchSupported reports whether a format has a way to turn on the provider's own web search: Chat's
// web_search_options, Responses' web_search tool, and Messages' web_search tool. The legacy and direct formats have
// none, so a request to them searches through a backend instead (docs/domains/gateway.md「联网搜索」).
func NativeSearchSupported(f Format) bool {
	return f == Chat || f == Responses || f == Messages
}

// injectNativeSearch adds the target format's own web search tool to a prepared body. The body is already in the
// endpoint's format, so the tool is written the way that format names it. A body that already turns the provider's
// search on is returned as it is, so one request can never search twice.
//
//nolint:cyclop,gocognit,funlen // one member per format and the tools splice are the grammar; splitting hides the order
func injectNativeSearch(format Format, body []byte, maxUses int) ([]byte, error) {
	if maxUses < 1 {
		maxUses = 1
	}
	if !NativeSearchSupported(format) {
		return nil, &NotSupported{Reason: "the endpoint's format has no native web search"}
	}
	trimmed := bytes.TrimRight(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	close := len(trimmed) - 1
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if dec.PeekKind() != '{' {
		return nil, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	toolsStart, toolsEnd := -1, -1
	var tools []byte
	var hasOptions bool
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		key := name.String() // before ReadValue, which voids the token
		value, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		switch key {
		case "tools":
			toolsStart, toolsEnd = end-len(value), end
			tools = bytes.Clone(bytes.TrimSpace(value)) // valid only until the next ReadValue
		case "web_search_options":
			hasOptions = true
		}
	}
	if format == Chat {
		if hasOptions || (toolsStart >= 0 && hasWebSearchTool(Chat, tools)) {
			return body, nil
		}
		return insertBefore(body, close, []byte(`,"web_search_options":{}`)), nil
	}
	tool := nativeSearchTool(format, maxUses)
	var out []byte
	switch {
	case toolsStart < 0:
		out = insertBefore(body, close, append(append([]byte(`,"tools":[`), tool...), ']'))
	case bytes.Equal(tools, []byte("null")):
		out = splice(body, toolsStart, toolsEnd, append(append([]byte{'['}, tool...), ']'))
	case len(tools) == 0 || tools[0] != '[' || tools[len(tools)-1] != ']':
		return nil, fmt.Errorf("%w: tools is not an array", ErrInvalid)
	case hasWebSearchTool(format, tools):
		out = body
	default:
		// Insert before the array's closing bracket, after a comma when it already holds a tool.
		at := toolsStart + bytes.Index(body[toolsStart:toolsEnd], tools) + len(tools) - 1
		insert := append([]byte{','}, tool...)
		if len(bytes.TrimSpace(tools[1:len(tools)-1])) == 0 {
			insert = tool
		}
		out = splice(body, at, at, insert)
	}
	if format == Responses {
		return withMaxToolCalls(out, maxUses)
	}
	return out, nil
}

// ResponsesMaxToolCalls reads Responses' max_tool_calls without rewriting the body. The gateway uses it to clamp a
// plugin's native search to a limit the client already set, even when the client did not declare the tool.
func ResponsesMaxToolCalls(body []byte) (int64, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if dec.PeekKind() != '{' {
		return 0, fmt.Errorf("%w: a request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return 0, err
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return 0, err
		}
		key := name.String()
		value, err := dec.ReadValue()
		if err != nil {
			return 0, err
		}
		if key != "max_tool_calls" {
			continue
		}
		n, err := strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 64)
		if err != nil {
			return 0, err
		}
		if n <= 0 {
			return 0, nil
		}
		return n, nil
	}
	return 0, nil
}

// withMaxToolCalls puts the effective search bound into Responses' max_tool_calls, the only member that limits how
// many server tools one response may call. A stricter limit already in the body wins; anything else is replaced, so
// the upstream limit is never looser than the reservation.
func withMaxToolCalls(body []byte, maxUses int) ([]byte, error) {
	start, end, existing, err := maxToolCalls(body)
	if err != nil {
		return nil, err
	}
	if existing > 0 && existing < maxUses {
		maxUses = existing
	}
	limit := strconv.AppendInt(nil, int64(maxUses), 10)
	if start >= 0 {
		return splice(body, start, end, limit), nil
	}
	return insertBefore(body, len(bytes.TrimRight(body, " \t\r\n"))-1, append([]byte(`,"max_tool_calls":`), limit...)), nil
}

// maxToolCalls returns the value's span and value, or -1 when the body has no max_tool_calls.
func maxToolCalls(body []byte) (start, end, value int, err error) {
	trimmed := bytes.TrimRight(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		return 0, 0, 0, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	dec := jsontext.NewDecoder(bytes.NewReader(trimmed))
	if dec.PeekKind() != '{' {
		return 0, 0, 0, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return 0, 0, 0, err
	}
	start, end = -1, -1
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return 0, 0, 0, err
		}
		key := name.String()
		v, err := dec.ReadValue()
		if err != nil {
			return 0, 0, 0, err
		}
		if key == "max_tool_calls" {
			start, end = int(dec.InputOffset())-len(v), int(dec.InputOffset())
			if n, parseErr := strconv.Atoi(string(bytes.TrimSpace(v))); parseErr == nil {
				value = n
			}
		}
	}
	return start, end, value, nil
}

// nativeSearchTool is the tool object a format adds, maxUses searches at most. Chat's switch is a member, not a tool.
func nativeSearchTool(format Format, maxUses int) []byte {
	switch format { //nolint:exhaustive // Chat has a member instead of a tool; the other formats are refused above
	case Responses:
		return []byte(`{"type":"web_search"}`)
	case Messages:
		return fmt.Appendf(nil, `{"type":"web_search_20250305","name":"web_search","max_uses":%d}`, maxUses)
	}
	return nil
}

// hasWebSearchTool reports whether a tools array already turns the provider's web search on.
func hasWebSearchTool(format Format, tools []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(tools))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return false
	}
	for dec.More() {
		var tool struct {
			Type string `json:"type"`
		}
		if err := dec.Decode(&tool); err != nil {
			return false
		}
		switch format { //nolint:exhaustive // only the two tool-bearing formats reach here
		case Chat, Responses:
			if tool.Type == "web_search" || tool.Type == "web_search_preview" {
				return true
			}
		case Messages:
			if len(tool.Type) >= len("web_search") && tool.Type[:len("web_search")] == "web_search" {
				return true
			}
		}
	}
	return false
}

func splice(body []byte, start, end int, with []byte) []byte {
	out := make([]byte, 0, len(body)-(end-start)+len(with))
	out = append(out, body[:start]...)
	out = append(out, with...)
	return append(out, body[end:]...)
}

func insertBefore(body []byte, at int, with []byte) []byte { return splice(body, at, at, with) }
