package gateway

import (
	"net/http"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/protocol"
)

// The request headers that go upstream are the endpoint's protocol's, not the client's: a conversation header reaches an
// endpoint only when the format behind it reads one, and its value is the session derived from the client's own, so
// that the client's session never leaves the node and two accounts never share a derived one
// (docs/domains/gateway.md「转发规则」).
//
// The session names are the ones sub2api reads to schedule a call to the account it pinned the session to (sub2api
// backend/internal/service/openai_gateway_scheduling.go, explicitOpenAIHeaderSessionNames, read 2026-10-19), beside the
// prompt_cache_key of the body: both carry the same derived value, so either path pins one session. They go to every
// OpenAI dialect — Responses and Chat — since that is what the relay reads them as. Anthropic has no session header:
// there the session travels in metadata.user_id (internal/protocol/README.md「同格式改写」).
// x-codex-turn-state is the vendor's own state, minted on an earlier turn and replayed by the client: on Responses it goes
// as it came, since a derived one would be an invented one. No other format has one.
var requestHeaders = [...][]headerRule{
	protocol.Messages:   {{name: "Anthropic-Beta"}, {name: "Anthropic-Version"}},
	protocol.Responses:  slices.Concat(openAISessionHeaders, []headerRule{{name: stateHeader}}),
	protocol.Chat:       openAISessionHeaders,
	protocol.Embeddings: nil,
	protocol.Rerank:     nil,
}

// openAISessionHeaders are the names a client may name its conversation in for an endpoint of an OpenAI dialect, each
// with the value a session is derived from.
var openAISessionHeaders = []headerRule{
	{name: "session_id", session: true},
	{name: "session-id", session: true},
	{name: "conversation_id", session: true},
	{name: "X-Session-Affinity", session: true},
	{name: "X-Session-Id", session: true},
	{name: "X-OpenCode-Session", session: true},
	{name: "X-Conversation-ID", session: true},
}

// headerRule is one request header a format's endpoints take, and how its value is made.
type headerRule struct {
	name    string
	session bool // the client's value is not sent: the tenant's derivation of it is
}

// stateHeader is the one response header of an upstream that reaches the client: the turn state it mints, which the
// client replays on its next turn (docs/domains/gateway.md「转发规则」).
const stateHeader = "X-Codex-Turn-State"

// taken are the client's headers forwardable keeps: every name a protocol takes, so that one copy of the request's
// headers serves whichever endpoint answers.
var taken = func() []string {
	var names []string
	for _, rules := range requestHeaders {
		for _, r := range rules {
			if name := http.CanonicalHeaderKey(r.name); !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}()

// forwardable copies the client's headers that any protocol takes, their values as they came, under the canonical name
// attempts read them by: attempt.upstreamHeader cuts them to its endpoint's protocol and derives the session ones. Every
// other header is dropped here, which includes the client's credentials, x-axiom-*, x-claude-code-*, HTTP-Referer and
// X-Title (docs/domains/gateway.md「转发规则」).
func forwardable(in http.Header) http.Header {
	out := make(http.Header, len(taken))
	for name, values := range in {
		if name = http.CanonicalHeaderKey(name); !slices.Contains(taken, name) {
			continue
		}
		out[name] = append(out[name], values...)
	}
	return out
}

// upstreamHeader is what one attempt sends: the client's values of the headers its endpoint's protocol takes, with the
// session ones derived for this account and only those. A header the client did not send is not sent: nothing is made up,
// and a value that is blank names no session, so it is dropped rather than derived into one every client would share.
func (a *attempt) upstreamHeader() http.Header {
	s := a.s
	rules := requestHeaders[a.entry.Target.Format]
	derive := s.x.identity(uuid.UUID(s.c.caller.AccountID()))
	out := make(http.Header, len(rules))
	for _, r := range rules {
		for _, v := range s.c.Header.Values(r.name) {
			if v = strings.TrimSpace(v); v != "" {
				if r.session {
					v = derive(v)
				}
				out.Add(r.name, v)
			}
		}
	}
	return out
}

// deviceID is the value that stands for the account itself rather than one of its sessions: the device member of
// Anthropic's metadata.user_id. One account has one device whatever its sessions are, so that only the session member
// tells conversations apart, and another account has another (docs/domains/gateway.md「转发规则」).
func (x *Executor) deviceID(account uuid.UUID) string { return x.identity(account)("device") }

// relayState writes the state header of the attempt whose content answers the call, when its upstream set one: the
// client gets the upstream's own blob back, never a value it sent itself. wmu is held.
func (s *session) relayState() {
	if s.state != "" {
		s.w.Header().Set(stateHeader, s.state)
	}
}
