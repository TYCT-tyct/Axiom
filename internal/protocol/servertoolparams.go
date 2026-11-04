package protocol

import (
	"bytes"
	"encoding/json"          //nolint:depguard // the tool parameters are decoded, not edited in place
	"encoding/json/jsontext" //nolint:depguard // the tools are spliced into the body as written
	"fmt"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// topMember finds a top-level member's raw value and the byte span it occupies. It walks the body's tokens, so a
// member of the same name inside a message is never the one found.
//
//nolint:cyclop // a token walk needs a branch per malformed shape
func topMember(body []byte, member string) (value []byte, start, end int, ok bool, err error) {
	// The body is parsed in place, and names are not collected for a duplicate check (Inspect made it): a body of many
	// members costs this scan nothing per member.
	dec := jsontext.NewDecoder(bytes.NewBuffer(body), walkOptions[0], walkOptions[1])
	if k := dec.PeekKind(); k != '{' {
		return nil, 0, 0, false, fmt.Errorf("%w: the request is not an object", ErrInvalid)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadValue()
		if err != nil {
			return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
		}
		from := int(dec.InputOffset())
		if err := dec.SkipValue(); err != nil {
			return nil, 0, 0, false, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
		}
		if !nameIs(key, member) {
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

// nameIs reports whether a member name as written, quotes and escapes included, is member. A name longer than member
// with every byte escaped is not it and is not unquoted; one that may be is unquoted into an array of its own, so a scan
// of the body allocates nothing for its names, however long they are. member is at most 32 bytes.
func nameIs(raw jsontext.Value, member string) bool {
	if len(raw) > quoted(len(member)) {
		return false
	}
	var buf [quotedNameRoom]byte
	name, err := jsontext.AppendUnquote(buf[:0], raw)
	return err == nil && string(name) == member
}

// quotedNameRoom holds a name of up to 32 bytes with every byte escaped.
const quotedNameRoom = 6*32 + 2

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
func parseServerParams(v []byte) (*ServerWebSearch, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v, &raw); err != nil {
		return nil, fmt.Errorf("%w: the web search parameters are not an object", ErrInvalid)
	}
	t := &ServerWebSearch{MaxResults: DefaultWebSearchResults}
	for name, value := range raw {
		if err := t.set(name, value); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// set reads one documented parameter into t. On an error t is left half set; the caller discards it.
func (t *ServerWebSearch) set(name string, value json.RawMessage) (err error) {
	switch name {
	case "engine":
		t.Engine, err = serverEngine(value)
	case "max_results":
		var n int64
		n, err = serverBounded(name, value, MaxServerResults)
		t.MaxResults = int(n)
	case "max_uses":
		t.MaxUses, err = serverBounded(name, value, DefaultServerToolCalls)
	case "max_total_results":
		t.MaxTotalResults, err = serverBounded(name, value, 1000)
	case "allowed_domains":
		t.IncludeDomains, err = serverDomains(value)
	case "excluded_domains":
		t.ExcludeDomains, err = serverDomains(value)
	case "mode", "search_context_size", "max_characters", "user_location", "x_search":
		err = fmt.Errorf("%w: the web search parameter %q is not supported yet", ErrInvalid, name)
	default:
		err = fmt.Errorf("%w: %q is not an OpenRouter web search parameter", ErrInvalid, name)
	}
	return err
}

// serverEngine reads the engine: empty, auto, native or a backend's name.
func serverEngine(v json.RawMessage) (string, error) {
	var engine string
	if err := json.Unmarshal(v, &engine); err != nil {
		return "", fmt.Errorf("%w: the web search engine is not a string", ErrInvalid)
	}
	if engine != "" && engine != "auto" && engine != "native" && !ValidEngineName(engine) {
		return "", fmt.Errorf("%w: %q is not a search engine name", ErrInvalid, engine)
	}
	return engine, nil
}

// serverBounded reads the count named name, which is between 1 and limit.
func serverBounded(name string, v json.RawMessage, limit int64) (int64, error) {
	n, err := serverCount(v)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > limit {
		return 0, fmt.Errorf("%w: %s is between 1 and %d", ErrInvalid, name, limit)
	}
	return n, nil
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
	// Each tool is walked where it lies: a tools array of many small objects is not decoded into a slice of them.
	var w walker
	var name []byte
	found := false
	match := func(member []byte, v jsontext.Value) error { //nolint:unparam // the walk's callback: other callbacks fail
		if string(member) == "name" {
			name = unquote(name[:0], v)
			found = found || string(name) == ServerToolName
		}
		return nil
	}
	_ = w.array(0, value, func(tool jsontext.Value) error { //nolint:errcheck // a tools array that does not walk declares nothing
		if f != Chat {
			return w.object(1, tool, match)
		}
		return w.object(1, tool, func(member []byte, v jsontext.Value) error {
			if string(member) == "function" {
				return w.object(2, v, match)
			}
			return nil
		})
	})
	return found
}

// serverArguments keeps a malformed model call from making the history invalid: an arguments string that is not a
// JSON object is replaced with an empty one, the same as Messages' input.
func serverArguments(args string) string {
	if v := jsontext.Value(args); v.Kind() == '{' && jsonx.Validate(v) == nil {
		return args
	}
	return "{}"
}

// RemoveMaxToolCalls removes OpenRouter's loop control from a prepared body. The client's cap is kept in Request for
// the gateway and must not reach a Chat or Messages upstream that does not know the member.
func RemoveMaxToolCalls(body []byte) ([]byte, error) { return removeTopMember(body, "max_tool_calls") }

// DropForcedToolChoice removes a forced tool choice when the body has no tool left to force. The loop removes its own
// function after the step cap; a client's tools, if any, keep their choice.
func DropForcedToolChoice(f Format, body []byte) ([]byte, error) {
	has, err := hasTool(body, "tools")
	if err != nil {
		return nil, err
	}
	if !has && (f == Chat || f == Messages) {
		if has, err = hasTool(body, "functions"); err != nil {
			return nil, err
		}
	}
	if has {
		return body, nil
	}
	if body, err = removeTopMember(body, "tool_choice"); err != nil {
		return nil, err
	}
	if f == Chat {
		return removeTopMember(body, "function_call")
	}
	return body, nil
}

// hasTool reports whether the top-level member names a non-empty array.
func hasTool(body []byte, member string) (bool, error) {
	value, _, _, ok, err := topMember(body, member)
	if err != nil || !ok {
		return false, err
	}
	v := bytes.TrimSpace(value)
	if bytes.Equal(v, []byte("null")) || bytes.Equal(v, []byte("[]")) {
		return false, nil
	}
	return true, nil
}
