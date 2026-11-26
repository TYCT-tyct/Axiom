package bedrock

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// frame is one event-stream message with string headers, as Bedrock frames them.
func frame(headers map[string]string, payload []byte) []byte {
	var h []byte
	for name, value := range headers {
		h = append(append(h, byte(len(name))), name...)                     //nolint:gosec // a short name
		h = binary.BigEndian.AppendUint16(append(h, 7), uint16(len(value))) //nolint:gosec // a short value
		h = append(h, value...)
	}
	m := binary.BigEndian.AppendUint32(nil, uint32(prelude+len(h)+len(payload)+4)) //nolint:gosec // a test's small message
	m = binary.BigEndian.AppendUint32(m, uint32(len(h)))                           //nolint:gosec // a test's small message
	m = binary.BigEndian.AppendUint32(m, crc32.ChecksumIEEE(m))
	m = append(append(m, h...), payload...)
	return binary.BigEndian.AppendUint32(m, crc32.ChecksumIEEE(m))
}

// chunk is a chunk event carrying one of Anthropic's events.
func chunk(event string) []byte {
	return frame(map[string]string{":message-type": "event", ":event-type": "chunk", ":content-type": "application/json"},
		[]byte(`{"bytes":"`+base64.StdEncoding.EncodeToString([]byte(event))+`","p":"abcdefgh"}`))
}

// TestEventStreamBecomesSSE: each chunk becomes the SSE event of the Anthropic event it carries, read a byte at a time
// as from a slow connection; an exception ends the stream with Anthropic's error event naming only its type; a CRC
// that does not match, or a stream cut inside a message, is an error, not a clean end.
func TestEventStreamBecomesSSE(t *testing.T) {
	t.Parallel()
	start := `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":25,"output_tokens":1}}}`
	delta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`
	stop := `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":25,"outputTokenCount":15}}`
	stream := bytes.Join([][]byte{chunk(start), chunk(delta), chunk(stop)}, nil)
	got, err := io.ReadAll(newSSE(io.NopCloser(iotest.OneByteReader(bytes.NewReader(stream)))))
	if want := "event: message_start\ndata: " + start + "\n\nevent: content_block_delta\ndata: " + delta + "\n\nevent: message_stop\ndata: " + stop + "\n\n"; err != nil || string(got) != want {
		t.Errorf("the SSE is %q (%v), want %q", got, err, want)
	}

	exception := frame(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"}, []byte(`{"message":"quote of the request"}`))
	got, err = io.ReadAll(newSSE(io.NopCloser(bytes.NewReader(append(chunk(start), exception...)))))
	if err != nil || !strings.HasSuffix(string(got), `data: {"type":"error","error":{"type":"overloaded_error","message":"Bedrock ended the stream: throttlingException"}}`+"\n\n") ||
		strings.Contains(string(got), "quote") {
		t.Errorf("an exception: %q %v", got, err)
	}

	corrupt := chunk(delta)
	corrupt[len(corrupt)-1] ^= 1
	for name, s := range map[string][]byte{"a bad CRC": corrupt, "a cut message": chunk(delta)[:20]} {
		if _, err := io.ReadAll(newSSE(io.NopCloser(bytes.NewReader(s)))); err == nil {
			t.Errorf("%s ended cleanly", name)
		}
	}
}

var example = secret.New(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`)

// TestSign: a stream goes to InvokeModelWithResponseStream of the region, the body without its model and stream, with
// Bedrock's version and the client's beta features, signed for bedrock in the region; one that does not stream goes to
// InvokeModel.
func TestSign(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		body, url, want, accept string
	}{
		{
			`{"model":"anthropic.claude-x-v1:0","stream":true,"max_tokens":8}`, "https://bedrock-runtime.eu-central-1.amazonaws.com/model/eu.anthropic.claude-x-v1:0/invoke-with-response-stream",
			`{"max_tokens":8,"anthropic_version":"bedrock-2023-05-31","anthropic_beta":["context-1m-2025-08-07","compact-2026-01-12"]}`, eventStream,
		},
		{
			`{"model":"anthropic.claude-x-v1:0","max_tokens":8}`, "https://bedrock-runtime.eu-central-1.amazonaws.com/model/eu.anthropic.claude-x-v1:0/invoke",
			`{"max_tokens":8,"anthropic_version":"bedrock-2023-05-31","anthropic_beta":["context-1m-2025-08-07","compact-2026-01-12"]}`, "application/json",
		},
	} {
		req, f := quirks{}.Sign(t.Context(), nil, adapter.SignedCall{
			Format: protocol.Messages, Region: "eu-central-1", Model: "eu.anthropic.claude-x-v1:0", Body: []byte(tc.body),
			Header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07, claude-code-20250219", "compact-2026-01-12"}}, Key: example, At: at,
		})
		if f != nil {
			t.Fatalf("%+v", f)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		auth := req.Header.Get("Authorization")
		if req.URL.String() != strings.Replace(tc.url, "v1:0", "v1%3A0", 1) || string(body) != tc.want || req.Header.Get("Accept") != tc.accept ||
			!strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261126/eu-central-1/bedrock/aws4_request, SignedHeaders=accept;content-type;host;x-amz-date, Signature=") {
			t.Errorf("sent %s %s, accept %s, %s", req.URL, body, req.Header.Get("Accept"), auth)
		}
	}
}

// TestBetas: the features Bedrock takes stay once each, Anthropic's tool search under Bedrock's name and with its
// examples; the rest go, and with none left there is no anthropic_beta at all.
func TestBetas(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"kept and dropped", []string{"claude-code-20250219,context-1m-2025-08-07", "interleaved-thinking-2025-05-14, context-1m-2025-08-07"}, `["context-1m-2025-08-07"]`},
		{"tool search renamed, examples added", []string{"advanced-tool-use-2025-11-20"}, `["tool-search-tool-2025-10-19","tool-examples-2025-10-29"]`},
		{"none left", []string{"claude-code-20250219, oauth-2025-04-20"}, ``},
		{"none given", nil, ``},
	} {
		if got := string(betas(tc.values)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestClassify: a key AWS does not know (403 with its error type) is the credential refused; a permission it lacks is
// the endpoint's; a throttle rests the region.
func TestClassify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		kind   string
		want   adapter.Failure
	}{
		{403, "UnrecognizedClientException:http://internal.amazon.com/coral/com.amazon.coral.service/", adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
		{403, "InvalidSignatureException", adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}},
		{403, "AccessDeniedException", adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.NotPermitted{}}},
		{429, "ThrottlingException", adapter.Failure{Scope: adapter.ScopeCredential, Cause: adapter.RateLimited{}}},
		{400, "ValidationException", adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}},
	} {
		if got := (quirks{}).Classify(tc.status, http.Header{"X-Amzn-Errortype": {tc.kind}}, []byte(`{"message":"x"}`)); got != tc.want {
			t.Errorf("%d %s: %+v", tc.status, tc.kind, got)
		}
	}
}

// fakeBedrock answers the control plane's listings: an on-demand model, one served only through profiles (a global
// and a geography's), one served only through a profile the region lacks, and one that is retired.
type fakeBedrock struct {
	t      *testing.T
	status int
	kind   string
}

func (f fakeBedrock) Do(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "bedrock.us-east-1.amazonaws.com" || !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/bedrock/aws4_request") {
		f.t.Errorf("asked %s with %q", r.URL, r.Header.Get("Authorization"))
	}
	body := `{"inferenceProfileSummaries":[{"inferenceProfileId":"global.anthropic.claude-new-v1:0","status":"ACTIVE"},
		{"inferenceProfileId":"us.anthropic.claude-new-v1:0","status":"ACTIVE"}]}`
	if r.URL.Path == "/foundation-models" {
		body = `{"modelSummaries":[
		{"modelId":"anthropic.claude-old-20240620-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["ON_DEMAND"],"modelLifecycle":{"status":"ACTIVE"}},
		{"modelId":"anthropic.claude-new-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["INFERENCE_PROFILE"],"modelLifecycle":{"status":"ACTIVE"}},
		{"modelId":"anthropic.claude-elsewhere-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["INFERENCE_PROFILE"],"modelLifecycle":{"status":"ACTIVE"}},
		{"modelId":"anthropic.claude-gone-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["ON_DEMAND"],"modelLifecycle":{"status":"LEGACY"}}]}`
	}
	status := http.StatusOK
	if f.status != 0 {
		status, body = f.status, `{"message":"The security token included in the request is invalid."}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"X-Amzn-Errortype": {f.kind}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// TestCheckRegion: the region's models are its on-demand ones by their IDs and its profile-only ones through the
// geography's profile, each known by Anthropic's name; a key AWS does not know fails as refused.
func TestCheckRegion(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	got := New().CheckRegion(t.Context(), fakeBedrock{t: t}, origin, "us-east-1", example, at)
	want := []adapter.CloudModel{
		{Name: "anthropic.claude-old-20240620-v1:0", Known: "claude-old-20240620"},
		{Name: "anthropic.claude-new-v1:0", Call: "us.anthropic.claude-new-v1:0", Known: "claude-new"},
	}
	if got.Outcome != adapter.KeyPassed || len(got.Models) != 2 || got.Models[0] != want[0] || got.Models[1] != want[1] {
		t.Errorf("the check is %+v", got)
	}
	refused := New().CheckRegion(t.Context(), fakeBedrock{t: t, status: 403, kind: "UnrecognizedClientException"}, origin, "us-east-1", example, at)
	if refused.Outcome != adapter.KeyFailed || refused.Cause != adapter.CauseRejected || refused.Status != 403 {
		t.Errorf("a key AWS does not know: %+v", refused)
	}
}
