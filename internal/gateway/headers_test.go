package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The identifiers the zero account of a test derives from "device" and from "conv-1" with the harness's secret, written
// out rather than computed: a relay of this deployment pins a conversation by exactly these, so a change to the
// derivation must show here and in TestSub2apiPinsTwoTurnsToOneAccount.
const (
	derivedDevice = "4d18b626afe1c93e61564c049c13a5cd"
	derivedConv1  = "c4bbbfa5ecaa4615727ca7e5a49facde"
)

// upstreamHeaders is what an attempt to an endpoint of the format sends of the client's headers.
func upstreamHeaders(t *testing.T, in http.Header, format protocol.Format) http.Header {
	t.Helper()
	x := new(Executor)
	x.IdentitySecret = []byte("0123456789abcdef")
	s := &session{x: x, c: &Call{Header: forwardable(in)}}
	a := &attempt{s: s, entry: routing.Entry{Target: protocol.Target{Format: format}}}
	return a.upstreamHeader()
}

// TestUpstreamHeaders: an attempt sends the headers of its endpoint's protocol and no other, each session value replaced
// by the account's derivation of it, the Anthropic ones and the turn state as they came, and a blank one not at all
// (internal/gateway/README.md「头白名单」).
func TestUpstreamHeaders(t *testing.T) {
	t.Parallel()
	in := http.Header{
		"Anthropic-Beta": {"context-1m-2026-01-01"}, "Anthropic-Version": {"2023-06-01"},
		"session_id": {"conv-1"}, "session-id": {"conv-1"}, "conversation_id": {"conv-1"},
		"X-Session-Affinity": {"conv-1", " "}, "X-Session-Id": {"  "}, "X-OpenCode-Session": {"conv-1"},
		"X-Conversation-ID": {"conv-1"}, "x-codex-turn-state": {"state-1"},
		"Authorization": {"Bearer secret"}, "X-Axiom-Preset": {"x"}, "X-Claude-Code-Session-Id": {"s"},
		"Http-Referer": {"https://app.example"}, "X-Title": {"app"}, "Cookie": {"a=b"}, "User-Agent": {"claude-cli/2.0.0"},
		"OpenAI-Beta": {"responses=experimental"}, "Originator": {"codex_cli_rs"}, "Content-Type": {"application/json"},
	}
	cases := []struct {
		name   string
		format protocol.Format
		want   http.Header
	}{
		{"messages", protocol.Messages, http.Header{
			"Anthropic-Beta": {"context-1m-2026-01-01"}, "Anthropic-Version": {"2023-06-01"},
		}},
		{"responses", protocol.Responses, http.Header{
			"Session_id": {derivedConv1}, "Session-Id": {derivedConv1}, "Conversation_id": {derivedConv1},
			"X-Session-Affinity": {derivedConv1}, "X-Opencode-Session": {derivedConv1}, "X-Conversation-Id": {derivedConv1},
			"X-Codex-Turn-State": {"state-1"},
		}},
		{"chat", protocol.Chat, http.Header{}},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, upstreamHeaders(t, in, c.format)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}

// TestTheTurnStateComesBackFromTheUpstream: the state header of the upstream of the attempt that answered goes back to the
// client, an upstream that minted none leaves the header off, and a state the client sent itself is never echoed — for a
// stream, an aggregated reply and the compact endpoint alike (docs/domains/gateway.md「转发规则」).
func TestTheTurnStateComesBackFromTheUpstream(t *testing.T) {
	no := false
	// minting is an upstream that answers as then does, with the turn state it sets on the response headers.
	minting := func(state string, then scripted) scripted {
		return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
			resp, fail := then(ctx)
			if resp != nil && state != "" {
				resp.Header.Set(stateHeader, state)
			}
			return resp, fail
		}
	}
	cases := []struct {
		name  string
		o     options
		state string
		want  string
	}{
		{"a stream", options{}, "t1", "t1"},
		{"a stream without one", options{}, "", ""},
		{"an aggregated reply", options{stream: &no}, "t2", "t2"},
		{"an aggregated reply without one", options{stream: &no}, "", ""},
		{"the compact endpoint", options{mutate: func(c *Call) { compact(t, c) }}, "t3", "t3"},
		{"the compact endpoint without one", options{mutate: func(c *Call) { compact(t, c) }}, "", ""},
	}
	for _, c := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, c.o)
			h.call.Header.Set(stateHeader, "the-client's-own")
			then := whole(h)
			if h.call.Compact {
				then = wholeAnswer(compacted)
			}
			h.up.eps[uuidOf(ep(1))].script = []scripted{minting(c.state, then)}
			h.run()
			if got := h.w.header.Get(stateHeader); got != c.want {
				t.Errorf("%s: the client got state %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// sub2apiSessionHeaders are the names sub2api looks for the session of an OpenAI call in, in its order: the first one it
// finds names the session, and the prompt_cache_key of the body is the fallback (sub2api
// backend/internal/service/openai_gateway_scheduling.go, explicitOpenAIHeaderSessionNames and explicitOpenAISessionID,
// read 2026-10-19).
var sub2apiSessionHeaders = []string{
	"session-id", "session_id", "conversation_id", "X-Session-Affinity", "X-Session-Id", "X-OpenCode-Session", "X-Conversation-ID",
}

// parsedUserID is what sub2api reads out of a metadata.user_id: the device and the session of the JSON object the string
// holds, both required, and nothing else it needs.
type parsedUserID struct{ device, session string }

// parseUserID reads a metadata.user_id the way sub2api does: after trimming, a string that starts an object with a
// device_id and a session_id, both non-empty; anything else is no session at all (ParseMetadataUserID).
func parseUserID(t *testing.T, raw string) (parsedUserID, bool) {
	t.Helper()
	if raw = strings.TrimSpace(raw); !strings.HasPrefix(raw, "{") {
		return parsedUserID{}, false
	}
	var id struct {
		Device  string `json:"device_id"`
		Session string `json:"session_id"`
	}
	if err := jsonx.Unmarshal([]byte(raw), &id); err != nil {
		return parsedUserID{}, false
	}
	if id.Device == "" || id.Session == "" {
		return parsedUserID{}, false
	}
	return parsedUserID{device: id.Device, session: id.Session}, true
}

// sessionOf is the session sub2api schedules a call by, rewritten here from its source rather than copied
// (backend/internal/service/gateway_service.go GenerateSessionHash, metadata_userid.go ParseMetadataUserID, read
// 2026-10-19): the session_id of a metadata.user_id it can parse for an Anthropic call, the first session header it knows
// for an OpenAI one, then the body's prompt_cache_key. What it does with the session afterwards — masking the identifier,
// overwriting x-claude-code-session-id, deriving a prompt cache key — the gateway neither sees nor depends on.
func sessionOf(t *testing.T, call suppliers.Call) string {
	t.Helper()
	var body map[string]any
	if err := jsonx.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("what went upstream is not an object: %v\n%s", err, call.Body)
	}
	session := ""
	if call.Format == protocol.Messages {
		metadata, _ := body["metadata"].(map[string]any)
		raw, _ := metadata["user_id"].(string)
		if id, ok := parseUserID(t, raw); ok {
			session = id.session
		}
	} else {
		session = openAISession(call, body)
	}
	if session == "" {
		t.Fatalf("a call of format %d reached the relay with no session: it would schedule it by hashing the content, "+
			"which differs between two turns of one conversation", call.Format)
	}
	return session
}

// openAISession is the session of an OpenAI call: the first session header sub2api knows, then the body's cache key.
func openAISession(call suppliers.Call, body map[string]any) string {
	for _, name := range sub2apiSessionHeaders {
		if v := strings.TrimSpace(call.Header.Get(name)); v != "" {
			return v
		}
	}
	key, _ := body["prompt_cache_key"].(string)
	return strings.TrimSpace(key)
}

// clientSession is the session the client names in its bodies and headers: the value that must not reach the relay.
const clientSession = "conv-1"

// clientTurn is one turn of a conversation as the client sends it: the body, and the header it names its session in where
// its format has one.
type clientTurn struct{ body, header string }

// conversation is one case of TestSub2apiPinsTwoTurnsToOneAccount.
type conversation struct {
	name   string
	format protocol.Format // the client's format
	turns  []clientTurn
	want   string // the session sub2api must read out of both turns
}

// TestSub2apiPinsTwoTurnsToOneAccount: two turns of one conversation are scheduled by sub2api to the same account of its
// pool, an Anthropic call by the structure the gateway writes into metadata.user_id and a Codex call by the session header
// it forwards, both of them the one derivation of the client's value that the gateway also puts in the body's cache key;
// the client's own value never reaches the relay (docs/domains/gateway.md「转发规则」).
func TestSub2apiPinsTwoTurnsToOneAccount(t *testing.T) {
	messages, responses := protocol.Messages, protocol.Responses
	cases := []conversation{
		{
			name:   "an Anthropic call, from metadata.user_id",
			format: messages,
			turns: []clientTurn{
				{body: `{"model":"client-model","stream":true,"metadata":{"user_id":"conv-1"},"messages":[{"role":"user","content":"first"}]}`},
				{body: `{"model":"client-model","stream":true,"metadata":{"user_id":"conv-1"},"messages":[{"role":"user","content":"first"},` +
					`{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`},
			},
			want: derivedConv1,
		},
		{
			name:   "a Codex call, from its session header",
			format: responses,
			turns: []clientTurn{
				{body: `{"model":"gpt-5.3-codex","stream":true,"input":"first","prompt_cache_key":"conv-1"}`, header: "session_id"},
				{body: `{"model":"gpt-5.3-codex","stream":true,"input":"second","prompt_cache_key":"conv-1"}`, header: "session_id"},
			},
			want: derivedConv1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var read []string
			for _, turn := range c.turns {
				read = append(read, sessionOf(t, runTurn(t, c, turn)))
			}
			if len(read) != len(c.turns) || read[0] != c.want || read[1] != c.want {
				t.Errorf("the relay read %v, want two turns of the session %q", read, c.want)
			}
		})
	}
}

// runTurn runs the call of one turn of c and holds what went upstream to what the relay must never see: the client's own
// session, in the body or in any header.
func runTurn(t *testing.T, c conversation, turn clientTurn) suppliers.Call {
	t.Helper()
	var got suppliers.Call
	run(t, func(t *testing.T, ctx context.Context) {
		format := c.format
		h := newHarness(t, ctx, options{client: format, upstream: &format, mutate: func(call *Call) {
			turnRequest(t, call, format, turn)
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		got = h.up.endpoint(1).calls[0]
		if strings.Contains(string(got.Body), clientSession) {
			t.Errorf("the client's session reached the relay in the body:\n%s", got.Body)
		}
		for name, values := range got.Header {
			if strings.Contains(strings.Join(values, " "), clientSession) {
				t.Errorf("the client's session reached the relay in header %s: %v", name, values)
			}
		}
	})
	return got
}

// turnRequest is what the harness hands the gateway for one turn: the client's body read as its format, the route in that
// format, and the header the client named its session in.
func turnRequest(t *testing.T, call *Call, format protocol.Format, turn clientTurn) {
	t.Helper()
	call.Body = []byte(turn.body)
	req, err := protocol.Inspect(format, call.Body)
	if err != nil {
		t.Fatal(err)
	}
	req.ServiceTiers = 1 << catalog.ServiceTierStandard
	call.Request = req
	for i := range call.Plan.Entries {
		call.Plan.Entries[i].Target.Format = format
	}
	if turn.header != "" {
		call.Header.Set(turn.header, clientSession)
	}
}

// TestTheMetadataCarriesTheDeviceAndTheSessionHeader: the structure of an Anthropic call names the account's device and
// the session the gateway also gives the session headers of an OpenAI one — one derivation, two carriers — and the whole
// identifier stays within the length Prepare sizes its output for.
func TestTheMetadataCarriesTheDeviceAndTheSessionHeader(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) {
			c.Body = []byte(`{"model":"client-model","stream":true,"metadata":{"user_id":"conv-1"},"messages":[{"role":"user","content":"hi"}]}`)
			req, err := protocol.Inspect(protocol.Messages, c.Body)
			if err != nil {
				t.Fatal(err)
			}
			req.ServiceTiers = 1 << catalog.ServiceTierStandard
			c.Request = req
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		account := uuid.UUID(h.call.caller.AccountID())
		header := upstreamHeaders(t, http.Header{"Session_id": {clientSession}}, protocol.Responses)
		var body map[string]any
		if err := jsonx.Unmarshal(h.up.endpoint(1).calls[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		metadata, _ := body["metadata"].(map[string]any)
		raw, _ := metadata["user_id"].(string)
		if !strings.HasPrefix(raw, "{") {
			t.Fatalf("metadata.user_id is %q: it is not the structure the session is read out of", raw)
		}
		// the bound Prepare sizes its output for (protocol's defaultIdentityLen): a longer one is refused, not written
		if len(raw) > 128 {
			t.Errorf("metadata.user_id takes %d bytes, over the 128 Prepare sizes its output for", len(raw))
		}
		id, ok := parseUserID(t, raw)
		if !ok || id.device != derivedDevice || id.device != h.exec.deviceID(account) {
			t.Errorf("the device of %q: %+v, want %q", raw, id, derivedDevice)
		}
		if want := header.Get("Session_id"); id.session != want || want != h.exec.identity(account)(clientSession) {
			t.Errorf("the session of %q is %q, want %q, the header's", raw, id.session, want)
		}
	})
}
