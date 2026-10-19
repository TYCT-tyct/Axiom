package suppliers

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
)

// TestRejectionOfKeepsTheWordsAndNotTheSupplier: the message, type, param and code of an upstream's error pass on as
// they are; whatever names the supplier does not (docs/domains/gateway.md「错误」).
func TestRejectionOfKeepsTheWordsAndNotTheSupplier(t *testing.T) {
	t.Parallel()
	const origin, key = "https://relay.example:8443", "sk-live-0123456789"
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
			"addresses", `{"error":{"message":"see https://docs.relay.example/limits?token=abc and http://10.0.0.1:3000/x. Done"}}`,
			&protocol.Rejection{Message: "see [address] and [address] Done"},
		},
		{
			"the host, in any case, and the key", `{"error":{"message":"RELAY.example:8443 refused ` + key + `"}}`,
			&protocol.Rejection{Message: "[hidden] refused [hidden]"},
		},
		{
			"credentials of other kinds", `{"error":{"message":"bad Bearer abcdefghijkl and sk-ant-api03-abcdefgh and api_key=zzzz1234 end"}}`,
			&protocol.Rejection{Message: "bad [hidden] and [hidden] and [hidden] end"},
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
