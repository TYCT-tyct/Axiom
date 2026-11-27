//go:build contract

package adaptertest

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"

	"axiom/internal/platform/jsonx"
)

// The fake upstream answers every request with the same usage, so that what a relay in front of it logs is known
// without asking it: a charge of the adapter's own reading is compared with the system's log, and the system's price
// of these tokens is worked out by hand.
const (
	UpstreamInputTokens  = 100
	UpstreamOutputTokens = 50
	// UpstreamOpenAIModel is served in OpenAI's formats (Chat and Responses), UpstreamAnthropicModel in Anthropic's
	// (Messages); the upstream answers either model in any of its endpoints, like a real one that maps names.
	UpstreamOpenAIModel    = "gpt-6-astra"
	UpstreamAnthropicModel = "claude-opus-5-5"
	// UpstreamKey is the credential a relay's channel or account holds for the upstream. The upstream takes any.
	UpstreamKey = "sk-upstream-contract-secret"
)

// UpstreamCall is one request the upstream received.
type UpstreamCall struct {
	Path, Model string
	Stream      bool
}

// Upstream is an OpenAI- and Anthropic-compatible server on the host that systems under test (new-api, sub2api, ...)
// relay to. It answers /v1/chat/completions, /v1/responses and /v1/messages, whole and as a stream, and /v1/models.
type Upstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []UpstreamCall
}

// StartUpstream serves the upstream until the test ends. It listens where the containers can reach it, which is decided
// by the way they reach the host (Reach).
func StartUpstream(t testing.TB, r Reach) *Upstream {
	t.Helper()
	u := &Upstream{}
	u.server = httptest.NewUnstartedServer(http.HandlerFunc(u.serve))
	if r.mode == reachHostGateway {
		// The container reaches the host gateway, not the loopback the server listens on by default. Everything it
		// serves is fixed text, with no credential and no user data.
		if err := u.server.Listener.Close(); err != nil {
			t.Fatal(err)
		}
		var config net.ListenConfig
		listener, err := config.Listen(t.Context(), "tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		u.server.Listener = listener
	}
	u.server.Start()
	t.Cleanup(u.server.Close)
	return u
}

// Port is the port the upstream listens on, on the host.
func (u *Upstream) Port() int { return u.server.Listener.Addr().(*net.TCPAddr).Port }

// Calls lists the requests received so far.
func (u *Upstream) Calls() []UpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]UpstreamCall(nil), u.calls...)
}

// reachMode is how containers reach the host (Reach).
type reach int

const (
	// reachTunnel is testcontainers' SSH port forwarding, which developers' machines use.
	reachTunnel reach = iota
	// reachHostGateway is the Docker host gateway of the CI server, which the library's tunnel cannot join to the
	// CI slice (diff_newapi_test.go does the same).
	reachHostGateway
	// reachHostNetwork runs the container in the host's network namespace, for a machine whose firewall refuses
	// connections from the bridge to the host and which cannot pull the tunnel's image: AXIOM_TEST_HOST_NETWORK=1.
	reachHostNetwork
)

func reachMode() reach {
	switch {
	case os.Getenv("AXIOM_TEST_HOST_NETWORK") == "1":
		return reachHostNetwork
	case os.Getenv("AXIOM_TEST_CGROUP_PARENT") != "":
		return reachHostGateway
	default:
		return reachTunnel
	}
}

// Reach is how a system under test in a container and the test on the host reach each other.
type Reach struct{ mode reach }

// HostReach reads the way this machine's containers reach the host. A test whose system is several containers that talk to
// each other on Docker's default network (sharesNetwork) cannot use the library's tunnel, which moves its container to
// a network of its own, and uses the host gateway instead.
func HostReach(sharesNetwork bool) Reach {
	mode := reachMode()
	if sharesNetwork && mode == reachTunnel {
		mode = reachHostGateway
	}
	return Reach{mode: mode}
}

// HostNetwork reports that containers share the host's network, so that they listen on a port the test picks and are
// reached on loopback; the ports they publish are not used.
func (r Reach) HostNetwork() bool { return r.mode == reachHostNetwork }

// Customize lets the container reach the host port of the upstream.
func (r Reach) Customize(u *Upstream) testcontainers.CustomizeRequestOption {
	switch r.mode {
	case reachHostNetwork:
		return testcontainers.WithHostConfigModifier(func(host *container.HostConfig) { host.NetworkMode = "host" })
	case reachHostGateway:
		return testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
			host.ExtraHosts = append(host.ExtraHosts, testcontainers.HostInternal+":host-gateway")
		})
	case reachTunnel:
	}
	return testcontainers.WithHostPortAccess(u.Port())
}

// UpstreamURL is the address of the upstream as a container reaches it: what a channel or an account is given as its
// base URL.
func (r Reach) UpstreamURL(u *Upstream) string {
	host := testcontainers.HostInternal
	if r.HostNetwork() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(u.Port()))
}

// FreePort is a port nothing on this machine listens on now, from below the kernel's ephemeral range as containertest
// picks them: a container in the host's network cannot have Docker pick its port.
func FreePort(t testing.TB) int {
	t.Helper()
	var config net.ListenConfig
	for range 100 {
		n, err := rand.Int(rand.Reader, big.NewInt(10000))
		if err != nil {
			t.Fatal(err)
		}
		port := 20000 + int(n.Int64())
		l, err := config.Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			return port
		}
	}
	t.Fatal("no free port from 20000 to 29999")
	return 0
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if r.Method == http.MethodPost && jsonx.Unmarshal(body, &req) != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	u.mu.Lock()
	u.calls = append(u.calls, UpstreamCall{Path: r.URL.Path, Model: req.Model, Stream: req.Stream})
	u.mu.Unlock()
	// Some relays add a query to the path (sub2api sends Messages with ?beta=true).
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		reply(w, models())
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		chat(w, req.Model, req.Stream)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		responses(w, req.Model, req.Stream)
	case strings.HasSuffix(r.URL.Path, "/messages"):
		messages(w, req.Model, req.Stream)
	default:
		http.NotFound(w, r)
	}
}

func reply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if _, err := io.WriteString(w, body); err != nil {
		return // a client that left is the relay's to handle
	}
}

// stream writes the events one by one, so that a relay sees them arrive as a real stream.
func stream(w http.ResponseWriter, events []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for _, e := range events {
		if _, err := io.WriteString(w, e); err != nil {
			return // a client that left is the relay's to handle
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func models() string {
	item := `{"id":%q,"object":"model","created":1,"owned_by":"contract","type":"model","display_name":%[1]q}`
	return `{"object":"list","data":[` + fmt.Sprintf(item, UpstreamOpenAIModel) + `,` + fmt.Sprintf(item, UpstreamAnthropicModel) + `]}`
}

func chat(w http.ResponseWriter, model string, streaming bool) {
	usage := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}`,
		UpstreamInputTokens, UpstreamOutputTokens, UpstreamInputTokens+UpstreamOutputTokens)
	if !streaming {
		reply(w, fmt.Sprintf(`{"id":"chatcmpl-contract","object":"chat.completion","created":1,"model":%q,`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":%s}`, model, usage))
		return
	}
	chunk := `data: {"id":"chatcmpl-contract","object":"chat.completion.chunk","created":1,"model":%q,"choices":%s%s}` + "\n\n"
	stream(w, []string{
		fmt.Sprintf(chunk, model, `[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]`, ""),
		fmt.Sprintf(chunk, model, `[{"index":0,"delta":{},"finish_reason":"stop"}]`, ""),
		fmt.Sprintf(chunk, model, `[]`, `,"usage":`+usage),
		"data: [DONE]\n\n",
	})
}

func responses(w http.ResponseWriter, model string, streaming bool) {
	usage := fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d,`+
		`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`,
		UpstreamInputTokens, UpstreamOutputTokens, UpstreamInputTokens+UpstreamOutputTokens)
	output := `[{"type":"message","id":"msg_contract","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]`
	response := func(status, output, usage string) string {
		return fmt.Sprintf(`{"id":"resp_contract","object":"response","created_at":1,"status":%q,"model":%q,"output":%s%s}`, status, model, output, usage)
	}
	if !streaming {
		reply(w, response("completed", output, `,"usage":`+usage))
		return
	}
	event := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	stream(w, []string{
		event("response.created", `{"type":"response.created","response":`+response("in_progress", "[]", "")+`}`),
		event("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_contract","output_index":0,"content_index":0,"delta":"ok"}`),
		event("response.completed", `{"type":"response.completed","response":`+response("completed", output, `,"usage":`+usage)+`}`),
	})
}

func messages(w http.ResponseWriter, model string, streaming bool) {
	message := func(content, stop, usage string) string {
		return fmt.Sprintf(`{"id":"msg_contract","type":"message","role":"assistant","model":%q,"content":%s,"stop_reason":%s,"stop_sequence":null,"usage":%s}`,
			model, content, stop, usage)
	}
	if !streaming {
		reply(w, message(`[{"type":"text","text":"ok"}]`, `"end_turn"`,
			fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d}`, UpstreamInputTokens, UpstreamOutputTokens)))
		return
	}
	event := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	stream(w, []string{
		event("message_start", `{"type":"message_start","message":`+message(`[]`, `null`, fmt.Sprintf(`{"input_tokens":%d,"output_tokens":1}`, UpstreamInputTokens))+`}`),
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`),
		event("content_block_stop", `{"type":"content_block_stop","index":0}`),
		event("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":%d}}`, UpstreamOutputTokens)),
		event("message_stop", `{"type":"message_stop"}`),
	})
}
