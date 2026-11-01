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

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
