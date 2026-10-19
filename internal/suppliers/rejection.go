package suppliers

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// maxRejectionText bounds each member of a rejection. An error is a sentence or two; a relay that echoes the request
// back is not passed on.
const maxRejectionText = 2 << 10

// What names or reaches a supplier in the words of its error: the addresses it writes (its own, or a documentation page
// of the system behind it), the credentials it echoes, and the request id new-api appends to every message, which is
// also what tells a client which system is behind us.
var (
	addressPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s)\]>"']+`)
	secretPattern  = regexp.MustCompile(`(?i)\b(?:sk|ak)-[a-z0-9_-]{8,}|\bbearer\s+[a-z0-9._~+/=-]{8,}|\bapi[_-]?key\s*[:=]\s*\S+`)
	requestID      = regexp.MustCompile(`(?i)\s*\((?:request[ _-]?id|req[ _-]?id)[: ][^)]*\)`)
	// relayBrand is the name of the software behind a supplier. A supplier is usually a new-api or sub2api, and its own
	// errors say so: type new_api_error, a code named for it.
	relayBrand = regexp.MustCompile(`(?i)new[_-]?api|one[_-]?api|sub2api`)
)

// knownTypes are the error types of OpenAI's and Anthropic's own APIs. Any other type is a relay's (new-api's
// new_api_error, sub2api's scheduling_error), which names the software behind the supplier, so it is not passed on.
var knownTypes = map[string]bool{
	"invalid_request_error": true, "authentication_error": true, "permission_error": true, "not_found_error": true,
	"rate_limit_error": true, "api_error": true, "overloaded_error": true, "billing_error": true, "request_too_large": true,
	"timeout_error": true, "server_error": true,
}

// rejectionOf is how the supplier's 4xx body said the request is refused, with whatever names the supplier taken out:
// the error object's message, type, param and code of OpenAI's and Anthropic's shells (the same members in both), which
// new-api and sub2api pass on from their upstreams. key is the credential the call was made with and origin the
// endpoint's address. A body that is not such an error, or says no message, is nil: the client gets our own words.
func rejectionOf(body []byte, origin, key string) *protocol.Rejection {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    any    `json:"type"`
			Param   any    `json:"param"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if jsonx.Unmarshal(body, &e) != nil || e.Error.Message == "" {
		return nil
	}
	hide := []string{key}
	if u, err := url.Parse(origin); err == nil {
		hide = append(hide, u.Host, u.Hostname())
		if _, parent, ok := strings.Cut(u.Hostname(), "."); ok && strings.Contains(parent, ".") && net.ParseIP(u.Hostname()) == nil {
			hide = append(hide, parent) // the supplier's own domain: a message may name it without the subdomain
		}
	}
	clean := func(s string) string { return cleanText(s, hide) }
	r := &protocol.Rejection{Message: clean(e.Error.Message), Param: clean(textOf(e.Error.Param))}
	if typ := textOf(e.Error.Type); knownTypes[typ] {
		r.Type = typ
	}
	if code := textOf(e.Error.Code); !relayBrand.MatchString(code) {
		r.Code = clean(code)
	}
	if strings.TrimSpace(r.Message) == "" {
		return nil
	}
	return r
}

// textOf is a member that is a string, or a number written as one (an error code may be either), and "" for the rest.
func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// cleanText takes out of s the request id, every address, the strings in hide (the credential, the supplier's host), the
// name of the software behind the supplier and what looks like a credential, and cuts it to maxRejectionText.
func cleanText(s string, hide []string) string {
	s = requestID.ReplaceAllString(s, "")
	s = relayBrand.ReplaceAllString(s, "[hidden]")
	s = addressPattern.ReplaceAllString(s, "[address]")
	for _, h := range hide {
		if len(h) >= 4 { // a shorter string would cut words the client needs
			s = replaceFold(s, h, "[hidden]")
		}
	}
	s = secretPattern.ReplaceAllString(s, "[hidden]")
	if len(s) > maxRejectionText {
		s = s[:maxRejectionText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

// replaceFold replaces every occurrence of old in s, whatever its case. It compiles the pattern each time: this is the
// path of a refused request, not of a call.
func replaceFold(s, old, with string) string {
	return regexp.MustCompile(`(?i)`+regexp.QuoteMeta(old)).ReplaceAllLiteralString(s, with)
}
