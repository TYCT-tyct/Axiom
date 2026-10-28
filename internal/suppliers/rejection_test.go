package suppliers

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
)

// TestRejectionOfKeepsTheWordsAndNotTheSupplier: the message, type, param and code of an upstream's error pass on as
// they are; whatever names the supplier does not (docs/domains/gateway.md「错误」).
func TestRejectionOfKeepsTheWordsAndNotTheSupplier(t *testing.T) {
	t.Parallel()
	const origin, key = "https://api.relay.example:8443", "sk-live-0123456789"
	for _, c := range []struct {
		name, body string
		want       *protocol.Rejection
	}{
		{
			"OpenAI's error", `{"error":{"message":"Invalid value for 'max_tokens': expected a value above 0.","type":"invalid_request_error","param":"max_tokens","code":"invalid_value"}}`,
			&protocol.Rejection{Message: "Invalid value for 'max_tokens': expected a value above 0.", Type: "invalid_request_error", Param: "max_tokens", Code: "invalid_value"},
		},
		{
			"Anthropic's error", `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"},"request_id":"req_1"}`,
			&protocol.Rejection{Message: "messages: at least one message is required", Type: "invalid_request_error"},
		},
		{
			"a code that is a number, a param that is null", `{"error":{"message":"m","code":4001,"param":null}}`,
			&protocol.Rejection{Message: "m", Code: "4001"},
		},
		{
			"new-api's request id", `{"error":{"message":"max_tokens: too small (request id: 20261019064811)","type":"invalid_request_error"}}`,
			&protocol.Rejection{Message: "max_tokens: too small", Type: "invalid_request_error"},
		},
		{
			"addresses", `{"error":{"message":"see (https://docs.relay.example/limits?token=abc) and http://10.0.0.1:3000/x. Done"}}`,
			&protocol.Rejection{Message: "see ([address]) and [address] Done"},
		},
		{
			"the host, in any case, and the key", `{"error":{"message":"API.relay.EXAMPLE:8443 refused ` + key + `"}}`,
			&protocol.Rejection{Message: "[hidden] refused [hidden]"},
		},
		{
			"the supplier's domain without its subdomain", `{"error":{"message":"ask support at relay.example about it"}}`,
			&protocol.Rejection{Message: "ask support at [hidden] about it"},
		},
		{
			"credentials of other kinds", `{"error":{"message":"bad Bearer abcdefghijkl and sk-ant-api03-abcdefgh and api_key=zzzz1234 end"}}`,
			&protocol.Rejection{Message: "bad [hidden] and [hidden] and [hidden] end"},
		},
		{
			"new-api's own error", `{"error":{"message":"Invalid request: invalid JSON request body (request id: 2026)","type":"new_api_error","code":"invalid_request"}}`,
			&protocol.Rejection{Message: "Invalid request: invalid JSON request body", Code: "invalid_request"},
		},
		{
			"sub2api's type and a code named for the software", `{"error":{"message":"no account for new-api group","type":"scheduling_error","code":"new_api_error_x"}}`,
			&protocol.Rejection{Message: "no account for [hidden] group"},
		},
		{"no message", `{"error":{"type":"invalid_request_error"}}`, nil},
		{"nothing left of the message", `{"error":{"message":" (request id: 1)"}}`, nil},
		{"a message that is not text", `{"error":{"message":{"a":1}}}`, nil},
		{"not JSON", `<html>Bad Request</html>`, nil},
		{"empty", ``, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, rejectionOf([]byte(c.body), origin, key)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// TestRejectionOfIsBounded: an upstream that echoes the request back is cut, at a character boundary.
func TestRejectionOfIsBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", maxRejectionText) // two bytes each
	r := rejectionOf([]byte(`{"error":{"message":"`+long+`"}}`), "https://relay.example", "k")
	if r == nil || len(r.Message) > maxRejectionText || r.Message == "" || strings.ContainsRune(r.Message, '\uFFFD') {
		t.Errorf("a message of %d bytes", len(long))
	}
}

// TestSanitizeCleansAStreamsWordsAsAnAnswers: an error event of a stream is cleaned by the same rules as a 4xx answer,
// with the credential and the endpoint's address that only this layer holds (docs/domains/gateway.md「错误」).
func TestSanitizeCleansAStreamsWordsAsAnAnswers(t *testing.T) {
	t.Parallel()
	const origin, key = "https://api.relay.example:8443", "sk-live-0123456789"
	said := protocol.Rejection{
		Message: "bad key " + key + " for api.relay.example (request id: 20261019)",
		Type:    "new_api_error",
		Param:   "max_tokens",
		Code:    "invalid_value",
	}
	body := `{"error":{"message":"` + said.Message + `","type":"` + said.Type + `","param":"` + said.Param +
		`","code":"` + said.Code + `"}}`
	a := &Attempt{ep: &endpoint{origin: origin}, cred: &credential{key: secret.New(key)}}
	if diff := cmp.Diff(rejectionOf([]byte(body), origin, key), a.Sanitize(said)); diff != "" {
		t.Errorf("the stream's words differ from an answer's (-want +got):\n%s", diff)
	}
	if a.Sanitize(protocol.Rejection{}) != nil {
		t.Error("an error with nothing left to say was passed on")
	}
	if a.Sanitize(protocol.Rejection{Message: " "}) != nil {
		t.Error("an error whose words were all removed was passed on")
	}
}

// TestRejectionOfHidesTheSupplierWhateverItsOrigin: the supplier's domain is hidden however many subdomains its origin has,
// and the credential whole before any pattern can cut it.
func TestRejectionOfHidesTheSupplierWhateverItsOrigin(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, origin, key, message, want string
	}{
		{"a parent of a deep subdomain", "https://api.us.relay.example", "x", "contact support@relay.example", "contact support@[hidden]"},
		{"the middle of three", "https://api.us.relay.example", "x", "see us.relay.example", "see [hidden]"},
		{"the host with a port", "https://api.relay.example:8443", "x", "api.relay.example:8443 said no", "[hidden] said no"},
		{"a two-label host", "https://relay.example", "x", "write to ops@relay.example", "write to ops@[hidden]"},
		{"an IP has no parent", "https://10.20.30.40:3000", "x", "20.30.40 is not 10.20.30.40", "20.30.40 is not [hidden]"},
		{"a credential with the software's name in it", "https://relay.example", "sk-newapi-channel-secret", "bad key sk-newapi-channel-secret", "bad key [hidden]"},
		{"a credential in a sentence with the name", "https://relay.example", "sk-newapi-channel-secret", "new-api rejected sk-newapi-channel-secret", "[hidden] rejected [hidden]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rejectionOf([]byte(`{"error":{"message":"`+c.message+`"}}`), c.origin, c.key)
			if r == nil || r.Message != c.want {
				t.Errorf("got %+v, want %q", r, c.want)
			}
		})
	}
}
