package suppliers

import (
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
	addressPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	secretPattern  = regexp.MustCompile(`(?i)\b(?:sk|ak)-[a-z0-9_-]{8,}|\bbearer\s+[a-z0-9._~+/=-]{8,}|\bapi[_-]?key\s*[:=]\s*\S+`)
	requestID      = regexp.MustCompile(`(?i)\s*\((?:request[ _-]?id|req[ _-]?id)[: ][^)]*\)`)
)

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
	}
	clean := func(s string) string { return cleanText(s, hide) }
	r := &protocol.Rejection{
		Message: clean(e.Error.Message), Type: clean(textOf(e.Error.Type)), Param: clean(textOf(e.Error.Param)), Code: clean(textOf(e.Error.Code)),
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

// cleanText takes out of s the request id, every address, the strings in hide (the credential, the supplier's host) and
// what looks like a credential, and cuts it to maxRejectionText.
func cleanText(s string, hide []string) string {
	s = requestID.ReplaceAllString(s, "")
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

// replaceFold replaces every occurrence of old in s, whatever its case.
func replaceFold(s, old, with string) string {
	return regexp.MustCompile(`(?i)`+regexp.QuoteMeta(old)).ReplaceAllLiteralString(s, with)
}
