package protocol

import (
	"bytes"
	"encoding/json"          //nolint:depguard // the tools array is decoded into a small typed value, not edited in place
	"encoding/json/jsontext" //nolint:depguard // the body is edited in place, keeping unknown members as written
	"fmt"
)

// Prepare prepares the request and, when the web plugin chose the provider's own search, adds that search to the body
// the endpoint's format expects. The tool goes in after conversion, so the body it edits is the one that is sent.
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) {
	u, err := prepare(r, body, t, w)
	if err != nil || w.NativeSearch <= 0 {
		return u, err
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
	if toolsStart < 0 {
		return insertBefore(body, close, append(append([]byte(`,"tools":[`), tool...), ']')), nil
	}
	switch {
	case bytes.Equal(tools, []byte("null")):
		return splice(body, toolsStart, toolsEnd, append(append([]byte{'['}, tool...), ']')), nil
	case len(tools) == 0 || tools[0] != '[' || tools[len(tools)-1] != ']':
		return nil, fmt.Errorf("%w: tools is not an array", ErrInvalid)
	case hasWebSearchTool(format, tools):
		return body, nil
	}
	// Insert before the array's closing bracket, after a comma when it already holds a tool.
	at := toolsStart + bytes.Index(body[toolsStart:toolsEnd], tools) + len(tools) - 1
	insert := append([]byte{','}, tool...)
	if len(bytes.TrimSpace(tools[1:len(tools)-1])) == 0 {
		insert = tool
	}
	return splice(body, at, at, insert), nil
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
