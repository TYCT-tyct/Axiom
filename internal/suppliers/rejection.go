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
	if jsonx.Unmarshal(body, &e) != nil {
		return nil
	}
	return cleanRejection(protocol.Rejection{
		Message: e.Error.Message, Type: textOf(e.Error.Type), Param: textOf(e.Error.Param), Code: textOf(e.Error.Code),
	}, origin, key)
}

// cleanRejection is what the client is told of a refusal: the members OpenAI's and Anthropic's shells share, with
// whatever names the supplier taken out, and only the type and code a client can act on. nil when no words are left.
func cleanRejection(r protocol.Rejection, origin, key string) *protocol.Rejection {
	hosts := hostNames(origin)
	clean := func(s string) string { return cleanText(s, key, hosts) }
	r.Message, r.Param = clean(r.Message), clean(r.Param)
	if !knownTypes[r.Type] {
		r.Type = ""
	}
	if relayBrand.MatchString(r.Code) {
		r.Code = ""
	} else {
		r.Code = clean(r.Code)
	}
	if strings.TrimSpace(r.Message) == "" {
		return nil
	}
	return &r
}

// Sanitize takes the supplier's identity out of an error the upstream's stream carried, as rejectionOf does for a
// refusal of the request's: the protocol reads an error's words bounded, and the credential, the addresses and the name
// of the software behind the supplier are hidden here, where they are known (docs/domains/gateway.md「错误」). nil is a
// refusal with nothing left to tell the client.
func (a *Attempt) Sanitize(r protocol.Rejection) *protocol.Rejection {
	return cleanRejection(r, a.ep.origin, a.cred.key.Reveal())
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

// hostNames are the names a message may call the supplier by: the host of its origin (with the port), the host, and the
// domain names the host belongs to, longest first. Every suffix of two labels or more counts, so the supplier's own domain
// is covered however many subdomains the origin has (api.us.relay.example hides us.relay.example and relay.example). The
// public suffix list would name the registrable domain exactly, but golang.org/x/net is not a dependency, and the
// suffixes it would add to this list (co.uk) are only over-masked, never leaked. An IP address has no parent domain.
func hostNames(origin string) []string {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	names := []string{u.Host, u.Hostname()}
	if net.ParseIP(u.Hostname()) == nil {
		labels := strings.Split(u.Hostname(), ".")
		for i := 1; i+1 < len(labels); i++ {
			names = append(names, strings.Join(labels[i:], "."))
		}
	}
	return names
}

// cleanText takes out of s the request id, the credential the call was made with, every address, the supplier's names,
// what looks like another credential and the name of the software behind the supplier, in that order, and cuts it to
// maxRejectionText. The credential goes first and whole: the patterns after it would otherwise cut it in pieces that
// neither it nor they match any more.
func cleanText(s, key string, hosts []string) string {
	s = requestID.ReplaceAllString(s, "")
	if len(key) >= 4 {
		s = replaceFold(s, key, "[hidden]")
	}
	s = addressPattern.ReplaceAllString(s, "[address]")
	for _, h := range hosts {
		if len(h) >= 4 { // a shorter string would cut words the client needs
			s = replaceFold(s, h, "[hidden]")
		}
	}
	s = secretPattern.ReplaceAllString(s, "[hidden]")
	s = relayBrand.ReplaceAllString(s, "[hidden]")
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
