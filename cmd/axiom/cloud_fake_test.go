package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"hash/crc32"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
)

// cloudHosts are the platforms' own addresses the fake answers at, by the names a node calls them by: the outbound
// client routes each to the fake (egresstest.Policy.Route), which holds a certificate for all of them.
var cloudHosts = []string{
	"oauth2.googleapis.com", "us-east5-aiplatform.googleapis.com",
	"bedrock.us-east-1.amazonaws.com", "bedrock.us-east-2.amazonaws.com", "bedrock.us-west-2.amazonaws.com",
	"bedrock-runtime.us-east-1.amazonaws.com", "harbor.openai.azure.com",
}

// The fake's credentials: a key that starts as these do passes, any other is refused.
const (
	cloudAccessKey = "AKIAFAKEHARBOR0001"
	cloudAPIKey    = "az-good-key-0042"
	cloudToken     = "ya29.fake-token"
)

// fakeCloud is Vertex AI, Bedrock and Azure OpenAI over TLS on loopback. Each lists or serves the models named, takes
// the credentials above and refuses every other, and refuses all of them once refuse is set; Bedrock's us-west-2 is a
// region the key may not use. It streams the Messages sample (in Bedrock's event-stream framing on Bedrock) and the
// Chat sample.
type fakeCloud struct {
	server         *httptest.Server
	cert           *x509.Certificate
	messages, chat string
	refuse         atomic.Bool
	mu             sync.Mutex
	seen           []supplied
}

func newFakeCloud(t *testing.T, messages, chat string) *fakeCloud {
	t.Helper()
	c := &fakeCloud{messages: messages, chat: chat}
	c.server = httptest.NewUnstartedServer(http.HandlerFunc(c.serve))
	certificate, leaf := cloudCertificate(t)
	c.server.TLS, c.cert = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}, leaf
	c.server.StartTLS()
	t.Cleanup(c.server.Close)
	return c
}

// cloudCertificate is a self-signed certificate for every one of cloudHosts.
func cloudCertificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: cloudHosts, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// routes are where the outbound client sends each of cloudHosts: the fake.
func (c *fakeCloud) routes(t *testing.T) map[string]netip.AddrPort {
	t.Helper()
	at, err := netip.ParseAddrPort(c.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	routes := make(map[string]netip.AddrPort, len(cloudHosts))
	for _, h := range cloudHosts {
		routes[h] = at
	}
	return routes
}

// egress is an outbound client that reaches the fake by the platforms' names.
func (c *fakeCloud) egress(t *testing.T) *egress.Client {
	t.Helper()
	return egress.NewForTesting(withRoutes(egresstest.Loopback(t, c.cert), c.routes(t)))
}

// withRoutes is policy with routes added.
func withRoutes(policy egresstest.Policy, routes map[string]netip.AddrPort) egresstest.Policy {
	for host, at := range routes {
		policy = policy.Route(host, at)
	}
	return policy
}

func (c *fakeCloud) sent() []supplied {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]supplied(nil), c.seen...)
}

func (c *fakeCloud) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	c.mu.Lock()
	c.seen = append(c.seen, supplied{host + r.URL.Path, r.Header.Get("Authorization") + r.Header.Get("Api-Key"), string(body)})
	c.mu.Unlock()
	switch {
	case host == "oauth2.googleapis.com":
		c.token(w, string(body))
	case strings.HasSuffix(host, "aiplatform.googleapis.com"):
		c.vertex(w, r)
	case strings.HasPrefix(host, "bedrock-runtime."):
		c.invoke(w)
	case strings.HasPrefix(host, "bedrock."):
		c.bedrockList(w, r, host)
	default:
		c.azure(w, r)
	}
}

func (c *fakeCloud) token(w http.ResponseWriter, body string) {
	if form, err := url.ParseQuery(body); err != nil || c.refuse.Load() || form.Get("assertion") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`) //nolint:errcheck // a test server
		return
	}
	_, _ = io.WriteString(w, `{"access_token":"`+cloudToken+`","expires_in":3600,"token_type":"Bearer"}`) //nolint:errcheck // a test server
}

func (c *fakeCloud) vertex(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Header.Get("Authorization") != "Bearer "+cloudToken:
		w.WriteHeader(http.StatusUnauthorized)
	case r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"name":"projects/harbor-prod/locations/us-east5"}`) //nolint:errcheck // a test server
	case strings.HasSuffix(r.URL.Path, ":streamRawPredict"):
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, c.messages) //nolint:errcheck // a test server
	default:
		http.NotFound(w, r)
	}
}

// bedrockList is the control plane: an on-demand model of each of the tests' catalogs; us-west-2 denies the key.
func (c *fakeCloud) bedrockList(w http.ResponseWriter, r *http.Request, host string) {
	switch {
	case c.refuse.Load() || !strings.Contains(r.Header.Get("Authorization"), "Credential="+cloudAccessKey[:14]):
		w.Header().Set("X-Amzn-Errortype", "UnrecognizedClientException:http://internal.amazon.com/coral/com.amazon.coral.service/")
		w.WriteHeader(http.StatusForbidden)
	case strings.Contains(host, "us-west-2"):
		w.Header().Set("X-Amzn-Errortype", "AccessDeniedException:http://internal.amazon.com/coral/com.amazon.coral.service/")
		w.WriteHeader(http.StatusForbidden)
	case r.URL.Path == "/foundation-models":
		_, _ = io.WriteString(w, `{"modelSummaries":[`+ //nolint:errcheck // a test server
			`{"modelId":"anthropic.claude-direct-1-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["ON_DEMAND"],"modelLifecycle":{"status":"ACTIVE"}},`+
			`{"modelId":"anthropic.claude-opus-5-5-v1:0","outputModalities":["TEXT"],"inferenceTypesSupported":["ON_DEMAND"],"modelLifecycle":{"status":"ACTIVE"}}]}`)
	default:
		_, _ = io.WriteString(w, `{"inferenceProfileSummaries":[]}`) //nolint:errcheck // a test server
	}
}

// invoke streams the Messages sample in Bedrock's framing; a refused key is Bedrock's 403.
func (c *fakeCloud) invoke(w http.ResponseWriter) {
	if c.refuse.Load() {
		w.Header().Set("X-Amzn-Errortype", "UnrecognizedClientException:http://internal.amazon.com/coral/com.amazon.coral.service/")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"The security token included in the request is invalid."}`) //nolint:errcheck // a test server
		return
	}
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	_, _ = w.Write(eventFrames(c.messages)) //nolint:errcheck // a test server
}

func (c *fakeCloud) azure(w http.ResponseWriter, r *http.Request) {
	switch {
	case c.refuse.Load() || r.Header.Get("Api-Key") != cloudAPIKey:
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/openai/deployments":
		_, _ = io.WriteString(w, `{"data":[{"id":"prod-astra","model":"gpt-6-astra","status":"succeeded"},`+ //nolint:errcheck // a test server
			`{"id":"prod-direct","model":"claude-direct-1","status":"succeeded"}],"object":"list"}`)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, c.chat) //nolint:errcheck // a test server
	default:
		http.NotFound(w, r)
	}
}

// eventFrames is each event of an SSE stream as a Bedrock chunk.
func eventFrames(stream string) []byte {
	var out bytes.Buffer
	for line := range strings.SplitSeq(stream, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || !strings.HasPrefix(data, "{") {
			continue
		}
		payload := `{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(data)) + `"}`
		out.Write(eventFrame(map[string]string{":message-type": "event", ":event-type": "chunk"}, payload))
	}
	return out.Bytes()
}

// eventFrame is one message of AWS's event-stream framing with string headers.
func eventFrame(headers map[string]string, payload string) []byte {
	var h bytes.Buffer
	for name, value := range headers {
		h.WriteByte(byte(len(name)))
		h.WriteString(name)
		h.WriteByte(7)
		h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(value)))) //nolint:gosec // a short header
		h.WriteString(value)
	}
	total := 12 + h.Len() + len(payload) + 4
	m := binary.BigEndian.AppendUint32(nil, uint32(total)) //nolint:gosec // a short message
	m = binary.BigEndian.AppendUint32(m, uint32(h.Len()))  //nolint:gosec // a short message
	m = binary.BigEndian.AppendUint32(m, crc32.ChecksumIEEE(m))
	m = append(append(m, h.Bytes()...), payload...)
	return binary.BigEndian.AppendUint32(m, crc32.ChecksumIEEE(m))
}

// serviceAccountKey is the JSON key file of a service account of the project harbor-prod, with a key made once.
var serviceAccountKey = sync.OnceValue(func() string {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	b, err := jsonx.MarshalCanonical(map[string]string{
		"type": "service_account", "project_id": "harbor-prod", "private_key_id": "0123456789abcdef0123456789abcdef0123cafe",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "client_email": "relay@harbor-prod.iam.gserviceaccount.com",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
})

// quoted is s as a JSON string.
func quoted(s string) string {
	b, err := jsonx.MarshalCanonical(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
