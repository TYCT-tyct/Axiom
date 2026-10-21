//go:build diff

package main

import (
	"bytes"
	"encoding/json/v2" //nolint:depguard // talks to new-api's admin API; jsonx guards input from outside
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
)

// newAPIImage is new-api's official image, pinned by digest so that a run today and one in a year compare Axiom with
// the same program: v1.0.0-rc.45, published 2026-10-17. Moving it is a decision (docs/domains/gateway.md「与 new-api
// 的有意差异」): read the new release's notes, run `just diff`, and decide each difference that appears.
const newAPIImage = "calciumion/new-api@sha256:6c63c7ee9cd5bf7880df3984e99dd3ef07992d7a97538cfb2719adf9d87bc3f9"

const (
	newAPIRoot     = "root"
	newAPIPassword = "diff-test-password-1"
)

// newAPI is a running new-api with a user's token and the channels that reach the fake upstream.
type newAPI struct {
	base string // http://host:port
	key  string // the token, the way a client sends it
}

// startNewAPI runs the image on SQLite, with the upstream's port reachable from inside it, and sets up what a deployment
// is set up with: an administrator, a token that spends without limit, and a channel for each model, of the type that
// speaks the format Axiom's endpoint of that model speaks (Anthropic's channel for Messages, OpenAI's for Responses,
// and explicit advanced-custom routes for Chat), so that the upstream is asked the same way by both.
func startNewAPI(t *testing.T, up *diffUpstream) newAPI {
	t.Helper()
	ctx := t.Context()
	container, err := testcontainers.Run(ctx, newAPIImage,
		containertest.WithCgroupParent(),
		testcontainers.WithExposedPorts("3000/tcp"),
		testcontainers.WithHostPortAccess(up.port()),
		testcontainers.WithEnv(map[string]string{"SESSION_SECRET": "diff-test-session-secret", "TZ": "UTC"}), //nolint:gosec // a throwaway container's, not a credential
		testcontainers.WithWaitStrategy(wait.ForHTTP("/api/status").WithPort("3000/tcp").WithStartupTimeout(2*time.Minute)),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("starting new-api: %v", err)
	}
	base, err := container.PortEndpoint(ctx, "3000/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	admin := &newAPIAdmin{t: t, base: base}
	admin.post("/api/setup", map[string]any{
		"username": newAPIRoot, "password": newAPIPassword, "confirmPassword": newAPIPassword,
		// Self-use mode serves models nobody has set a price for, which is every model of a fake upstream.
		"SelfUseModeEnabled": true, "DemoSiteEnabled": false,
	})
	admin.token = admin.post("/api/user/login", map[string]any{"username": newAPIRoot, "password": newAPIPassword})["access_token"].(string)
	admin.post("/api/token/", map[string]any{"name": "diff", "unlimited_quota": true, "expired_time": -1})
	id := admin.get("/api/token/")["items"].([]any)[0].(map[string]any)["id"].(float64)
	key := admin.post("/api/token/"+strconv.Itoa(int(id))+"/key", nil)["key"].(string)
	upstream := fmt.Sprintf("http://%s:%d", testcontainers.HostInternal, up.port())
	for _, channel := range []struct {
		typ    int
		models string
	}{{1, "gpt-6-astra"}, {14, "claude-opus-5-5"}} { // OpenAI's and Anthropic's channel types (constant/channel.go)
		admin.post("/api/channel/", map[string]any{"mode": "single", "channel": map[string]any{
			"type": channel.typ, "name": "upstream-" + channel.models, "key": newAPIChannelKey, "base_url": upstream,
			"models": channel.models, "group": "default", "status": 1,
		}})
	}
	// The OpenAI channel passes Responses through; explicit routes exercise new-api's Chat-only upstream converters.
	admin.post("/api/channel/", map[string]any{"mode": "single", "channel": map[string]any{
		"type": 58, "name": "upstream-deepseek", "key": newAPIChannelKey, "base_url": upstream,
		"models": "deepseek-v4-chat", "group": "default", "status": 1,
		"settings": `{"advanced_custom":{"advanced_routes":[` +
			`{"incoming_path":"/v1/chat/completions","upstream_path":"/v1/chat/completions","converter":"none"},` +
			`{"incoming_path":"/v1/messages","upstream_path":"/v1/chat/completions","converter":"anthropic_messages_to_openai_chat_completions"},` +
			`{"incoming_path":"/v1/responses","upstream_path":"/v1/chat/completions","converter":"openai_responses_to_openai_chat_completions"}]}}`,
	}})
	// Only GPT's upstream speaks Responses; DeepSeek must keep receiving Chat requests.
	admin.option("global.chat_completions_to_responses_policy", `{"enabled":true,"all_channels":true,"model_patterns":["^gpt-6-astra$"]}`)
	// It refuses requests with 503 while the machine's CPU is above 90%, which a CI server building other branches is.
	admin.option("performance_setting.monitor_enabled", "false")
	return newAPI{base: base, key: "sk-" + key}
}

// newAPIAdmin calls new-api's management API as its administrator.
type newAPIAdmin struct {
	t     *testing.T
	base  string
	token string
}

func (a *newAPIAdmin) option(key, value string) {
	a.t.Helper()
	a.call(http.MethodPut, "/api/option/", map[string]any{"key": key, "value": value})
}

func (a *newAPIAdmin) get(path string) map[string]any { return a.call(http.MethodGet, path, nil) }
func (a *newAPIAdmin) post(path string, body map[string]any) map[string]any {
	return a.call(http.MethodPost, path, body)
}

// call returns the "data" of a successful answer; new-api reports a failure as a 200 with success false.
func (a *newAPIAdmin) call(method, path string, body map[string]any) map[string]any {
	a.t.Helper()
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.base+path, in)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test client
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	var answer struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || !answer.Success {
		a.t.Fatalf("new-api %s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	return answer.Data
}
