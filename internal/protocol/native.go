package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the body is edited in place, keeping unknown members as written
	"fmt"
	"strconv"
)

// Prepare prepares the request and, when the web plugin chose the provider's own search, adds that search to the body
// the endpoint's format expects. The tool goes in after conversion, so the body it edits is the one that is sent.
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) {
	u, _, err := PrepareBudgeted(nil, r, body, t, w)
	return u, err
}

// PrepareBudgeted is Prepare drawing every allocation it makes from m first, and returning what it leaves charged:
// the capacity of the body it made, which the caller gives back once the request is sent. A request that would surpass
// the budget is ErrMemory and leaves nothing charged. A nil m charges nothing, as Prepare does.
func PrepareBudgeted(m *Memory, r *Request, body []byte, t Target, w Rewrites) (u Upstream, held int64, err error) {
	b := &noBudget
	if m != nil {
		b = &budget{m: m}
	}
	return b.prepare(r, body, t, w)
}

// prepare is PrepareBudgeted on a budget of the caller's, which the tests read what it drew from.
func (b *budget) prepare(r *Request, body []byte, t Target, w Rewrites) (u Upstream, held int64, err error) {
	u, err = prepareWithin(r, body, t, w, b)
	if err != nil {
		b.give(b.held)
		return u, 0, err
	}
	if w.NativeSearch > 0 {
		if u.Body, err = injectNativeSearch(t.Format, u.Body, w.NativeSearch, b); err != nil {
			b.give(b.held)
			return u, 0, err
		}
	}
	held = int64(cap(u.Body))
	if b.held < held { // a path that wrote its body without drawing for it, as the direct rewrites do
		if err := b.draw(held - b.held); err != nil {
			b.give(b.held)
			return u, 0, err
		}
	}
	b.give(b.held - held)
	return u, held, nil
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
// Each body it makes is drawn from b before it is made, and the one it replaces given back; the decoders read in place.
//
//nolint:cyclop,gocognit,funlen // one member per format and the tools splice are the grammar; splitting hides the order
func injectNativeSearch(format Format, body []byte, maxUses int, b *budget) ([]byte, error) {
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
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
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
			tools = bytes.TrimSpace(value) // a slice of body: the decoder reads a bytes.Buffer in place
		case "web_search_options":
			hasOptions = true
		}
	}
	if format == Chat {
		if hasOptions || (toolsStart >= 0 && hasWebSearchTool(Chat, tools)) {
			return body, nil
		}
		return splice(body, close, close, []byte(`,"web_search_options":{}`), b)
	}
	tool := nativeSearchTool(format, maxUses)
	var out []byte
	var err error
	switch {
	case toolsStart < 0:
		out, err = splice(body, close, close, append(append([]byte(`,"tools":[`), tool...), ']'), b)
	case bytes.Equal(tools, []byte("null")):
		out, err = splice(body, toolsStart, toolsEnd, append(append([]byte{'['}, tool...), ']'), b)
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
		out, err = splice(body, at, at, insert, b)
	}
	if err != nil || format != Responses {
		return out, err
	}
	return withMaxToolCalls(out, maxUses, b)
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
func withMaxToolCalls(body []byte, maxUses int, b *budget) ([]byte, error) {
	start, end, existing, err := maxToolCalls(body)
	if err != nil {
		return nil, err
	}
	if existing > 0 && existing < maxUses {
		maxUses = existing
	}
	limit := strconv.AppendInt(nil, int64(maxUses), 10)
	if start >= 0 {
		return splice(body, start, end, limit, b)
	}
	at := len(bytes.TrimRight(body, " \t\r\n")) - 1
	return splice(body, at, at, append([]byte(`,"max_tool_calls":`), limit...), b)
}

// maxToolCalls returns the value's span and value, or -1 when the body has no max_tool_calls.
func maxToolCalls(body []byte) (start, end, value int, err error) {
	trimmed := bytes.TrimRight(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		return 0, 0, 0, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	dec := jsontext.NewDecoder(bytes.NewBuffer(trimmed))
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

// hasWebSearchTool reports whether a tools array already turns the provider's web search on. It reads the array in
// place and each tool's type into a small array of its own, so it allocates nothing however many tools there are; a
// member name matches as encoding/json matched it, whatever its case.
func hasWebSearchTool(format Format, tools []byte) bool {
	dec := jsontext.NewDecoder(bytes.NewBuffer(tools), jsontext.AllowDuplicateNames(true))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return false
	}
	var name, value [32]byte
	for dec.PeekKind() != ']' {
		kind, ok := toolType(dec, &name, &value)
		if !ok {
			return false
		}
		switch format { //nolint:exhaustive // only the two tool-bearing formats reach here
		case Chat, Responses:
			if string(kind) == "web_search" || string(kind) == "web_search_preview" {
				return true
			}
		case Messages:
			if bytes.HasPrefix(kind, []byte("web_search")) {
				return true
			}
		}
	}
	return false
}

// toolType reads one tool and returns its type, the last one given, unquoted into value; nil when it has none or one
// too long to be a web search tool's. It reports false for an element that is not an object.
func toolType(dec *jsontext.Decoder, name, value *[32]byte) ([]byte, bool) {
	if dec.PeekKind() != '{' {
		return nil, false
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, false
	}
	var kind []byte
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return nil, false
		}
		v, err := dec.ReadValue()
		if err != nil {
			return nil, false
		}
		if len(n) > len(name) {
			continue
		}
		unquoted, _ := jsontext.AppendUnquote(name[:0], n) //nolint:errcheck // a member name is a valid string
		if !bytes.EqualFold(unquoted, []byte("type")) {
			continue
		}
		kind = nil
		if v.Kind() == '"' && len(v) <= len(value) {
			kind, _ = jsontext.AppendUnquote(value[:0], v) //nolint:errcheck // the kind says it is a string
		}
	}
	_, err := dec.ReadToken()
	return kind, err == nil
}

// splice is body with its bytes from start to end replaced by with, in a body of its own drawn from b before it is made;
// body, which it replaces, is given back.
func splice(body []byte, start, end int, with []byte, b *budget) ([]byte, error) {
	size := len(body) - (end - start) + len(with)
	if err := b.draw(rounded(int64(size))); err != nil {
		return nil, err
	}
	out := make([]byte, 0, size)
	out = append(out, body[:start]...)
	out = append(out, with...)
	out = append(out, body[end:]...)
	b.give(rounded(int64(cap(body))))
	return out, nil
}
