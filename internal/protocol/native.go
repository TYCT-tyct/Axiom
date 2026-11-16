package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the body is edited in place, keeping unknown members as written
	"errors"
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
// the budget is ErrMemory and leaves nothing charged. A memory refusal that competing preparations' charges could
// cover is retried once exclusively. A nil m charges nothing, as Prepare does.
func PrepareBudgeted(m *Memory, r *Request, body []byte, t Target, w Rewrites) (Upstream, int64, error) {
	if m == nil {
		return noBudget.prepare(r, body, t, w)
	}
	if m.left.Load() < int64(len(body)) {
		return Upstream{}, 0, ErrMemory
	}
	retry := false
	if u, held, err := func() (Upstream, int64, error) {
		m.preparing.RLock()
		defer m.preparing.RUnlock()
		b := &budget{m: m}
		u, held, err := b.prepare(r, body, t, w)
		retry = b.retry
		return u, held, err
	}(); !errors.Is(err, ErrMemory) {
		return u, held, err
	}
	if !retry {
		return Upstream{}, 0, ErrMemory
	}
	// The failed preparation and its partial output are gone before the retry draws again.
	// Taking the write gate drains competing partial charges and blocks new preparations.
	m.preparing.Lock()
	defer m.preparing.Unlock()
	return (&budget{m: m}).prepare(r, body, t, w)
}

// prepare is PrepareBudgeted on a budget of the caller's, which the tests read what it drew from.
func (b *budget) prepare(r *Request, body []byte, t Target, w Rewrites) (u Upstream, held int64, err error) {
	u, err = prepareWithin(r, body, t, w, b)
	if err == nil {
		u.Body, err = b.serverRewrites(u.Body, t, w)
	}
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
	if b.m != nil {
		b.m.preparationMu.Lock()
		b.m.preparationHeld -= held // the sent body remains charged, but cannot be reclaimed by waiting for preparation
		b.m.preparationMu.Unlock()
	}
	return u, held, nil
}

// serverRewrites applies the gateway's server-loop edits to a prepared body: the loop's own cap and forced choice
// out, its search function in. Each is a step under b.within.
func (b *budget) serverRewrites(body []byte, t Target, w Rewrites) ([]byte, error) {
	var err error
	if w.ServerLoop || w.HasMaxToolCalls && (t.Format == Chat || t.Format == Messages) {
		if body, err = b.within(body, 1, RemoveMaxToolCalls); err != nil {
			return nil, err
		}
	}
	if w.ServerLoop && !w.ServerFunction {
		drop := func(body []byte) ([]byte, error) { return DropForcedToolChoice(t.Format, body) }
		// Chat loses function_call too, into a body of its own.
		if body, err = b.within(body, 2, drop); err != nil {
			return nil, err
		}
	}
	if w.ServerFunction {
		inject := func(body []byte) ([]byte, error) { return injectServerFunction(t.Format, body) }
		if body, err = b.within(body, 1, inject); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// within runs step, which splices members in or out of body, making at most bodies new bodies, and scans it, under a
// reservation drawn before it starts: that many bodies of the input's size, the room the server function takes and a
// decoder's state. The scans allocate nothing that grows with the body (topMember). Of the reservation only the
// output's capacity stays drawn; the input stays drawn too until prepare settles what it holds.
// TestPrepareAllocatesOnlyWhatItDrew holds the bound.
func (b *budget) within(body []byte, bodies int, step func([]byte) ([]byte, error)) ([]byte, error) {
	reserve := int64(bodies)*rounded(int64(len(body))+serverFunctionCost(true)) + bodyDecoderMemory
	if err := b.draw(reserve); err != nil {
		return nil, err
	}
	out, err := step(body)
	if err != nil || len(out) == 0 || len(body) > 0 && &out[0] == &body[0] { // refused, or the body as it was
		b.give(reserve)
		return out, err
	}
	if kept := rounded(int64(cap(out))); kept > reserve {
		if err := b.draw(kept - reserve); err != nil {
			return nil, err
		}
	} else {
		b.give(reserve - kept)
	}
	return out, nil
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
func injectNativeSearch(format Format, body []byte, maxUses int, b *budget) ([]byte, error) {
	if maxUses < 1 {
		maxUses = 1
	}
	if !NativeSearchSupported(format) {
		return nil, &NotSupported{Reason: "the endpoint's format has no native web search"}
	}
	m, err := readSearchMembers(body)
	if err != nil {
		return nil, err
	}
	searches, err := m.searching(format, b)
	if err != nil {
		return nil, err
	}
	if format == Chat {
		if m.options || searches {
			return body, nil
		}
		return splice(body, m.close, m.close, []byte(`,"web_search_options":{}`), b)
	}
	out, err := m.addTool(body, nativeSearchTool(format, maxUses), searches, b)
	if err != nil || format != Responses {
		return out, err
	}
	return withMaxToolCalls(out, maxUses, b)
}

// searchMembers is where a prepared body declares its tools, and whether it sets Chat's web_search_options.
type searchMembers struct {
	close                int    // the body's closing brace
	toolsStart, toolsEnd int    // the tools value's span, -1 when the body has none
	tools                []byte // the tools value without its whitespace, a slice of the body
	options              bool
}

func readSearchMembers(body []byte) (searchMembers, error) {
	trimmed := bytes.TrimRight(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		return searchMembers{}, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	m := searchMembers{close: len(trimmed) - 1, toolsStart: -1, toolsEnd: -1}
	dec := newDecoder(body) // the prepared body: no names are repeated in it
	if dec.PeekKind() != '{' {
		return searchMembers{}, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return searchMembers{}, err
	}
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue()
		if err != nil {
			return searchMembers{}, err
		}
		value, err := dec.ReadValue()
		if err != nil {
			return searchMembers{}, err
		}
		end := int(dec.InputOffset())
		switch {
		case nameIs(name, "tools"):
			m.toolsStart, m.toolsEnd = end-len(value), end
			m.tools = bytes.TrimSpace(value) // a slice of body: the decoder reads a bytes.Buffer in place
		case nameIs(name, "web_search_options"):
			m.options = true
		}
	}
	return m, nil
}

// searching reports whether the body's tools already turn the provider's search on. Chat's web_search_options does
// so whatever its tools are, so they are not read.
func (m *searchMembers) searching(format Format, b *budget) (bool, error) {
	if m.toolsStart < 0 || format == Chat && m.options {
		return false, nil
	}
	return hasWebSearchTool(format, m.tools, b)
}

// addTool adds tool to the body's tools; searches says one of them already turns the provider's search on.
func (m *searchMembers) addTool(body, tool []byte, searches bool, b *budget) ([]byte, error) {
	tools := m.tools
	switch {
	case m.toolsStart < 0:
		return splice(body, m.close, m.close, append(append([]byte(`,"tools":[`), tool...), ']'), b)
	case bytes.Equal(tools, []byte("null")):
		return splice(body, m.toolsStart, m.toolsEnd, append(append([]byte{'['}, tool...), ']'), b)
	case len(tools) == 0 || tools[0] != '[' || tools[len(tools)-1] != ']':
		return nil, fmt.Errorf("%w: tools is not an array", ErrInvalid)
	case searches:
		return body, nil
	}
	// Insert before the array's closing bracket, after a comma when it already holds a tool.
	at := m.toolsStart + bytes.Index(body[m.toolsStart:m.toolsEnd], tools) + len(tools) - 1
	insert := append([]byte{','}, tool...)
	if len(bytes.TrimSpace(tools[1:len(tools)-1])) == 0 {
		insert = tool
	}
	return splice(body, at, at, insert, b)
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
		name, err := dec.ReadValue()
		if err != nil {
			return 0, err
		}
		limit := nameIs(name, "max_tool_calls") // name is only valid until the next read refills the buffer
		value, err := dec.ReadValue()
		if err != nil {
			return 0, err
		}
		if !limit {
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
	dec := newDecoder(trimmed)
	if dec.PeekKind() != '{' {
		return 0, 0, 0, fmt.Errorf("%w: a prepared request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return 0, 0, 0, err
	}
	start, end = -1, -1
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue()
		if err != nil {
			return 0, 0, 0, err
		}
		v, err := dec.ReadValue()
		if err != nil {
			return 0, 0, 0, err
		}
		if nameIs(name, "max_tool_calls") {
			start, end = int(dec.InputOffset())-len(v), int(dec.InputOffset())
			value = smallInt(v, value)
		}
	}
	return start, end, value, nil
}

// smallInt is v as an int, or otherwise when it is not one. No int is longer than 20 digits: a longer value is not
// one, and is not copied to a string to learn so.
func smallInt(v []byte, otherwise int) int {
	digits := bytes.TrimSpace(v)
	if len(digits) > 20 {
		return otherwise
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil {
		return otherwise
	}
	return n
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
// place and each tool's type by its decoded value, as encoding/json did, into a small array of its own: a type longer
// than that array, which only a long or escaped spelling is, is decoded into a buffer drawn from b. A member name
// matches as encoding/json matched it, whatever its case; an element that is not a tool ends the search, as it did.
func hasWebSearchTool(format Format, tools []byte, b *budget) (bool, error) {
	dec := newDecoder(tools) // a value of the prepared body: no names are repeated in it
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return false, nil //nolint:nilerr // not an array: it holds no search tool
	}
	var name [32]byte
	var value [128]byte
	for dec.PeekKind() != ']' {
		kind, err := toolType(dec, &name, &value, b)
		if errors.Is(err, errNotTool) {
			return false, nil
		}
		if err != nil || searchType(format, kind) {
			return err == nil, err
		}
	}
	return false, nil
}

// searchType reports whether a tool of the decoded type kind is the format's own web search.
func searchType(format Format, kind []byte) bool {
	if format == Messages {
		return bytes.HasPrefix(kind, []byte("web_search"))
	}
	return string(kind) == "web_search" || string(kind) == "web_search_preview"
}

// errNotTool is an element of a tools array that is not an object.
var errNotTool = errors.New("protocol: not a tool")

// toolType reads one tool and returns its type, the last one given, decoded; nil when it has none.
func toolType(dec *jsontext.Decoder, name *[32]byte, value *[128]byte, b *budget) ([]byte, error) {
	if dec.PeekKind() != '{' {
		return nil, errNotTool
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, errNotTool
	}
	var kind []byte
	for dec.PeekKind() != '}' {
		n, errName := dec.ReadValue()
		v, errValue := dec.ReadValue()
		if errName != nil || errValue != nil {
			return nil, errNotTool
		}
		if len(n) > len(name) { // "type" with every byte escaped is 26 bytes
			continue
		}
		unquoted, _ := jsontext.AppendUnquote(name[:0], n) //nolint:errcheck // a member name is a valid string
		if bytes.EqualFold(unquoted, []byte("type")) {
			var err error
			if kind, err = decodedType(v, value, b); err != nil {
				return nil, err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, errNotTool
	}
	return kind, nil
}

// decodedType is a tool's type unquoted into value, or into a buffer drawn from b when it does not fit; nil when it is
// not a string.
func decodedType(v jsontext.Value, value *[128]byte, b *budget) ([]byte, error) {
	if v.Kind() != '"' {
		return nil, nil
	}
	buf := value[:0]
	if len(v) > len(value) {
		if err := b.draw(rounded(int64(len(v)))); err != nil {
			return nil, err
		}
		buf = make([]byte, 0, len(v))
	}
	kind, _ := jsontext.AppendUnquote(buf, v) //nolint:errcheck // the kind says it is a string
	return kind, nil
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
