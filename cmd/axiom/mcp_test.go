package main

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/nodetls"
	"axiom/internal/platform/operationsapi"
)

// contractTools are the bridge's tools of the embedded contract, by name.
func contractTools(t *testing.T) map[string]bridgeTool {
	t.Helper()
	doc, err := operationsapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := bridgeTools(doc)
	if err != nil {
		t.Fatalf("the bridge refused the contract: %v", err)
	}
	byName := map[string]bridgeTool{}
	for _, tool := range tools {
		byName[tool.tool.Name] = tool
	}
	return byName
}

// TestBridgeToolsFollowTheContract: every operation of the contract is one tool under its x-mcp-name (the runbooks call
// them by name, so a rename must fail here), its input is the operation's parameters and body with every reference
// inlined, and the hints say which tools read and which change something under a key.
func TestBridgeToolsFollowTheContract(t *testing.T) {
	t.Parallel()
	tools := contractTools(t)
	want := []string{
		"alert_raise", "alert_resolve", "alerts_list", "approval_get", "approval_wait", "approval_withdraw", "approvals_list", "audit_get",
		"audit_list", "call_get", "calls_list", "caps_get", "credit_grant", "endpoint_set_enabled", "model_get", "model_set_status",
		"models_list", "operation_get", "operation_log_list",
		"operations_list", "pending_mappings_list", "price_publish", "prices_list", "pricing_rule_create", "pricing_rule_update",
		"pricing_rules_list", "supplier_group_select", "supplier_model_map",
		"supplier_top_up_confirm", "supplier_top_up_propose", "suppliers_get", "suppliers_list", "whoami",
	}
	if names := slices.Sorted(maps.Keys(tools)); !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
	cases := []struct {
		tool     string
		required []string
		read     bool
	}{
		{"whoami", []string{"reason"}, true},
		{"suppliers_list", []string{"reason"}, true},
		{"model_set_status", []string{"idempotency_key", "model_id", "reason", "rev", "status"}, false},
		{"credit_grant", []string{"account_id", "amount", "idempotency_key", "reason"}, false},
		{"pricing_rule_update", []string{"idempotency_key", "reason", "rev", "rule", "rule_id"}, false},
	}
	for _, c := range cases {
		tool := tools[c.tool].tool
		input := tool.InputSchema.(map[string]any)
		if got, _ := input["required"].([]string); !slices.Equal(got, c.required) || input["additionalProperties"] != false {
			t.Errorf("%s: required %v, additionalProperties %v; want %v and false", c.tool, got, input["additionalProperties"], c.required)
		}
		if a := tool.Annotations; a.ReadOnlyHint != c.read || a.IdempotentHint == c.read || (a.DestructiveHint == nil) != c.read {
			t.Errorf("%s: annotations %+v", c.tool, a)
		}
	}
	encoded, err := jsonx.MarshalCanonical(tools["credit_grant"].tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(encoded); strings.Contains(text, "$ref") || !strings.Contains(text, `"currency"`) || !strings.Contains(text, `"^-?(0|[1-9][0-9]*)$"`) {
		t.Errorf("credit_grant's input does not inline the money schema: %s", text)
	}
}

// describeRequest is r as one line: method and URL, headers sorted, body.
func describeRequest(t *testing.T, r *http.Request) string {
	t.Helper()
	headers := make([]string, 0, len(r.Header))
	for name := range r.Header {
		headers = append(headers, name+"="+r.Header.Get(name))
	}
	slices.Sort(headers)
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			t.Fatal(err)
		}
	}
	return r.Method + " " + r.URL.String() + " | " + strings.Join(headers, " ") + " | " + string(body)
}

// TestBridgeRequestMapsArguments: a call's arguments become the operation's path, query, headers and body; what the
// tool does not take, or what would change the path, is refused before anything is sent.
func TestBridgeRequestMapsArguments(t *testing.T) {
	t.Parallel()
	tools := contractTools(t)
	core, err := url.Parse("https://core.internal:8084")
	if err != nil {
		t.Fatal(err)
	}
	const base = "https://core.internal:8084/api/operations/v1"
	cases := []struct {
		name, tool, args string
		want             string // the request as describeRequest writes it, or the refusal
	}{
		{
			"a read", "models_list", `{"reason":"patrol here","status":"listed","limit":20}`,
			"GET " + base + "/models?limit=20&reason=patrol+here&status=listed |  | ",
		},
		{
			"a write", "model_set_status", `{"model_id":"0198c0a8-0000-7000-8000-000000000002","status":"retired","rev":3,` +
				`"reason":"stale","idempotency_key":"0198c0a8-0000-7000-8000-00000000000a","run_id":"run-1","dry_run":true}`,
			"PUT " + base + "/models/0198c0a8-0000-7000-8000-000000000002/status | " +
				`Content-Type=application/json Idempotency-Key=0198c0a8-0000-7000-8000-00000000000a X-Axiom-Run-Id=run-1 | ` +
				`{"dry_run":true,"reason":"stale","rev":3,"status":"retired"}`,
		},
		{"no arguments", "whoami", ``, "GET " + base + "/me |  | "},
		{"an escaped path argument", "call_get", `{"reason":"r","call_id":"a/b?c"}`, "GET " + base + "/calls/a%2Fb%3Fc?reason=r |  | "},
		{"an argument the tool does not take", "whoami", `{"reason":"r","token":"x"}`, `whoami takes no argument "token"`},
		{"a missing path argument", "model_get", `{"reason":"r"}`, "model_get needs the argument model_id"},
		{"a path argument that climbs", "model_get", `{"reason":"r","model_id":".."}`, "the argument model_id: must name an object"},
		{"an object for a parameter", "models_list", `{"reason":{"x":1}}`, "the argument reason: want a string, a number or a boolean"},
		{"arguments that are not an object", "whoami", `["reason"]`, "the arguments must be a JSON object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := tools[c.tool].request(t.Context(), core, []byte(c.args))
			got := ""
			if err != nil {
				got = err.Error()
			} else {
				got = describeRequest(t, r)
			}
			if got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// bearer adds the bridge's token of this start to every request, as Hermes does.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// writeFile writes data to a new file in dir, readable by its owner only, and returns its path.
func writeFile(t *testing.T, dir, name, data string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readLocalToken reads the token of this start the bridge wrote for Hermes, which only its owner may read.
func readLocalToken(t *testing.T, path string) string {
	t.Helper()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the token file for Hermes: %v %v", info, err)
	}
	written, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(written))
}

// startTestBridge starts the bridge in front of the operations API at coreURL with token as the credential, serves it,
// and returns where, and the token of this start that Hermes reads from its file.
func startTestBridge(t *testing.T, coreURL, token string) (bridgeURL, local string) {
	t.Helper()
	dir := t.TempDir()
	core, err := url.Parse(coreURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := bridgeSettings{operations: core, tokenFile: writeFile(t, dir, "operations-token", token+"\n"), localTokenFile: filepath.Join(dir, "mcp-token")}
	handler, err := startBridge(t.Context(), cfg, http.DefaultTransport, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("startBridge: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL, readLocalToken(t, cfg.localTokenFile)
}

// connectBridge is Hermes: an MCP client of the bridge at bridgeURL, presenting local.
func connectBridge(t *testing.T, bridgeURL, local string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-test", Version: "v0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: bridgeURL, HTTPClient: &http.Client{Transport: bearer{local}}}
	session, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("connect to the bridge: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close the MCP session: %v", err)
		}
	})
	return session
}

// callTool calls name with args and returns the result's text and whether it is a tool error.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s answered %d contents", name, len(result.Content))
	}
	return result.Content[0].(*mcp.TextContent).Text, result.IsError
}

// expectOffered checks the tools the bridge lists: every one of offered, none of withheld.
func expectOffered(t *testing.T, session *mcp.ClientSession, offered, withheld []string) {
	t.Helper()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(listed.Tools))
	for i, tool := range listed.Tools {
		names[i] = tool.Name
	}
	for _, name := range offered {
		if !slices.Contains(names, name) {
			t.Errorf("the bridge does not offer %s: %v", name, names)
		}
	}
	for _, name := range withheld {
		if slices.Contains(names, name) {
			t.Errorf("the bridge offers %s, which the credential may not call", name)
		}
	}
}

// expectToolAnswer calls name and checks whether it failed and that its text holds want.
func expectToolAnswer(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, fail bool, want string) {
	t.Helper()
	if text, failed := callTool(t, session, name, args); failed != fail || !strings.Contains(text, want) {
		t.Errorf("%s %v: %s (error %v), want error %v with %s", name, args, text, failed, fail, want)
	}
}

// expectBridgeLog reads the credential's operation log through the bridge: the patrol is there as the agent's call
// with its run, and so is the bridge's own start.
func expectBridgeLog(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	text, _ := callTool(t, session, "operation_log_list", map[string]any{"reason": "replay", "limit": 10})
	var log operationsapi.OperationLogList
	if err := jsonx.Unmarshal([]byte(text), &log); err != nil {
		t.Fatalf("operation_log_list: %v: %s", err, text)
	}
	patrol := slices.IndexFunc(log.Data, func(e operationsapi.OperationLogEntry) bool { return e.Reason == "morning patrol" })
	start := slices.IndexFunc(log.Data, func(e operationsapi.OperationLogEntry) bool { return e.Reason == bridgeStartReason })
	if patrol < 0 || start < 0 {
		t.Fatalf("the operation log does not hold the patrol and the bridge's start: %s", text)
	}
	if p := log.Data[patrol]; p.Tool != "suppliers_list" || p.RunId == nil || *p.RunId != "run-7" || log.Data[start].Tool != "whoami" {
		t.Errorf("the patrol and the start are not logged as the agent's calls: %s", text)
	}
}

// expectStrangersRefused: without the token of this start, the bridge answers nothing.
func expectStrangersRefused(t *testing.T, bridgeURL, local string) {
	t.Helper()
	for name, header := range map[string]string{"no token": "", "another start's token": "Bearer " + strings.Repeat("A", len(local))} {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, bridgeURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, resp.StatusCode)
		}
	}
}

// TestMCPBridgeCallsTheOperationsAPI: through the bridge, an MCP client sees only the tools the credential may call
// (no money tool without operations.money, nothing it lacks the permission for), and its calls reach the real
// operations API as the agent's, with the reason and run in the operation log; the core's refusals come back as tool
// errors the model can read. Without the token of this start the bridge answers nothing.
func TestMCPBridgeCallsTheOperationsAPI(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	_, token := f.makeCredential(f.owner(), `{"name":"hermes","permissions":["suppliers.view","catalog.view","catalog.manage","pricing.view"],
		"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"start the agent"}`)
	core := httptest.NewServer(httpserver.Public(f.ops, slog.New(slog.DiscardHandler), bodyTimeout))
	t.Cleanup(core.Close)
	bridgeURL, local := startTestBridge(t, core.URL, token)
	session := connectBridge(t, bridgeURL, local)

	expectOffered(t, session, []string{"whoami", "suppliers_list", "model_set_status", "prices_list"},
		[]string{"price_publish", "supplier_top_up_propose", "credit_grant", "calls_list"})
	expectToolAnswer(t, session, "suppliers_list", map[string]any{"reason": "morning patrol", "run_id": "run-7"}, false, `"data"`)
	expectToolAnswer(t, session, "model_set_status", map[string]any{
		"model_id": "0198c0a8-0000-7000-8000-000000000002", "status": "retired",
		"rev": 1, "reason": "gone upstream", "idempotency_key": "0198c0a8-0000-7000-8000-00000000000b", "dry_run": true,
	}, true, `"not_found"`)
	expectToolAnswer(t, session, "suppliers_list", nil, true, `"invalid_request"`)
	expectToolAnswer(t, session, "suppliers_list", map[string]any{"reason": "r", "url": "https://elsewhere"}, true, `takes no argument "url"`)
	expectBridgeLog(t, session)
	expectStrangersRefused(t, bridgeURL, local)
}

// TestMCPBridgeRefusesToStartWithoutTheCore: a credential the core refuses, or a core that does not answer, stops the
// bridge's start instead of offering tools that cannot be called, and leaves Hermes no token.
func TestMCPBridgeRefusesToStartWithoutTheCore(t *testing.T) {
	t.Parallel()
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"unauthenticated","message":"no live credential"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(refusing.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	for name, coreURL := range map[string]string{"a refused credential": refusing.URL, "a core that is not there": gone.URL} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			core, err := url.Parse(coreURL)
			if err != nil {
				t.Fatal(err)
			}
			cfg := bridgeSettings{operations: core, tokenFile: writeFile(t, dir, "operations-token", "op-ax-v1-test\n"), localTokenFile: filepath.Join(dir, "mcp-token")}
			if _, err := startBridge(t.Context(), cfg, http.DefaultTransport, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), operationsURLVar) {
				t.Fatalf("startBridge = %v, want a refusal naming %s", err, operationsURLVar)
			}
			if _, err := os.Stat(cfg.localTokenFile); !os.IsNotExist(err) {
				t.Fatalf("a refused start wrote a token for Hermes: %v", err)
			}
		})
	}
}

// fakeOperationsCore answers the two operations an ops node's test calls, over mutual TLS with the certificates in
// coreDir, and only to the credential op-ax-v1-test.
func fakeOperationsCore(t *testing.T, coreDir string) *httptest.Server {
	t.Helper()
	coreTLS, err := nodetls.Server(coreDir)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{
		"/api/operations/v1/me": `{"id":"opcred_test","name":"hermes","permissions":["operations.read","suppliers.view"],` +
			`"platform":true,"proposer_vendors":[],"expires_at":"2099-01-01T00:00:00Z"}`,
		"/api/operations/v1/suppliers": `{"data":[]}`,
	}
	core := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := answers[r.URL.Path]
		switch {
		case len(r.TLS.PeerCertificates) == 0 || r.Header.Get("Authorization") != "Bearer op-ax-v1-test":
			w.WriteHeader(http.StatusUnauthorized)
		case !ok:
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, answer); err != nil {
				t.Error(err)
			}
		}
	}))
	core.TLS = coreTLS
	core.StartTLS()
	t.Cleanup(core.Close)
	return core
}

// awaitStop cancels a node started for a test and waits for it to shut down cleanly.
func awaitStop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not shut down")
	}
}

// TestServeOpsNodeBridgesToTheCore: an ops node configured with the bridge asks the core over mutual TLS who its
// credential is before it serves, writes the token of this start for Hermes, and serves the tools on its loopback
// listener, forwarding each call with the node's certificate and the credential.
func TestServeOpsNodeBridgesToTheCore(t *testing.T) {
	t.Parallel()
	coreDir, opsDir := testNodeDirs(t)
	core := fakeOperationsCore(t, coreDir)
	dir := t.TempDir()
	localFile := filepath.Join(dir, "mcp-token")
	environ := []string{
		"AXIOM_NODE_ROLES=ops", "AXIOM_PROBES_ADDR=127.0.0.1:0", "AXIOM_SHUTDOWN_TIMEOUT=5s", "AXIOM_MCP_ADDR=127.0.0.1:0",
		"AXIOM_OPERATIONS_URL=" + core.URL, "AXIOM_NODE_TLS_DIR=" + opsDir,
		"AXIOM_OPERATIONS_TOKEN_FILE=" + writeFile(t, dir, "operations-token", "op-ax-v1-test\n"), "AXIOM_MCP_TOKEN_FILE=" + localFile,
	}
	logs := make(records, 16)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveForTest(ctx, environ, slog.New(slog.NewJSONHandler(logs, nil))) }()
	addrs := servingAddrs(t, logs, done)
	if addrs.MCP == "" || addrs.Public != "" {
		t.Fatalf("an ops node with the bridge opened mcp=%q public=%q", addrs.MCP, addrs.Public)
	}
	session := connectBridge(t, "http://"+addrs.MCP, readLocalToken(t, localFile))
	expectToolAnswer(t, session, "suppliers_list", map[string]any{"reason": "patrol"}, false, `{"data":[]}`)
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "model_get", Arguments: map[string]any{"reason": "r", "model_id": "x"}}); err == nil {
		t.Error("the bridge called model_get, which a credential without catalog.view is not offered")
	}
	awaitStop(t, cancel, done)
}

// redirectingCore answers whoami for the credential op-ax-v1-test and redirects every other operation to location;
// a whoami at /me redirects too when redirectWhoami is set.
func redirectingCore(t *testing.T, location string, redirectWhoami bool) *httptest.Server {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/operations/v1/me" || redirectWhoami {
			http.Redirect(w, r, location+"/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"opcred_test","name":"hermes","permissions":["operations.read","suppliers.view"],`+
			`"platform":true,"proposer_vendors":[],"expires_at":"2099-01-01T00:00:00Z"}`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(core.Close)
	return core
}

// TestMCPBridgeNeverFollowsARedirect: a redirect from the core, to a plain-HTTP port of the same host or to another
// host, is never followed, so neither the credential nor a write's body leaves the mutual-TLS connection: at start the
// bridge refuses, and on a call the model gets a tool error. The place the redirect names receives nothing.
func TestMCPBridgeNeverFollowsARedirect(t *testing.T) {
	t.Parallel()
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(elsewhere.Close)
	sameHost := elsewhere.URL // 127.0.0.1 on another port, without TLS
	otherHost := strings.Replace(elsewhere.URL, "127.0.0.1", "localhost", 1)
	for name, location := range map[string]string{"the same host in plain HTTP": sameHost, "another host": otherHost} {
		t.Run(name, func(t *testing.T) {
			bridgeURL, local := startTestBridge(t, redirectingCore(t, location, false).URL, "op-ax-v1-test")
			session := connectBridge(t, bridgeURL, local)
			expectToolAnswer(t, session, "suppliers_list", map[string]any{"reason": "patrol"}, true, "Temporary Redirect")

			dir := t.TempDir()
			core, err := url.Parse(redirectingCore(t, location, true).URL)
			if err != nil {
				t.Fatal(err)
			}
			cfg := bridgeSettings{operations: core, tokenFile: writeFile(t, dir, "operations-token", "op-ax-v1-test\n"), localTokenFile: filepath.Join(dir, "mcp-token")}
			if _, err := startBridge(t.Context(), cfg, http.DefaultTransport, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "307") {
				t.Fatalf("a redirected whoami: startBridge = %v, want a refusal with 307", err)
			}
		})
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the redirect's target received %d requests", n)
	}
}

// TestMCPBridgeKeepsItsIdentityFiles: a token file for Hermes that is the credential or one of the node's certificate
// files, under any name, stops the start before anything is written, and the file keeps its bytes.
func TestMCPBridgeKeepsItsIdentityFiles(t *testing.T) {
	t.Parallel()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	core, err := url.Parse(gone.URL)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(dir, credential, key string) string{
		"the credential's path": func(_, credential, _ string) string { return credential },
		"the credential, spelled again": func(dir, _, _ string) string {
			return filepath.Join(dir, ".", "keys", "..", "keys", "operations-token")
		},
		"a link to the credential": func(dir, credential, _ string) string {
			link := filepath.Join(dir, "mcp-token")
			if err := os.Symlink(credential, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"the node's key": func(_, _, key string) string { return key },
	}
	for name, localFile := range cases {
		t.Run(name, func(t *testing.T) { expectIdentityKept(t, core, localFile) })
	}
}

// expectIdentityKept starts the bridge with a credential and a node key in dir/keys and the token file localFile names:
// the start is refused for the token file, and both files keep their bytes.
func expectIdentityKept(t *testing.T, core *url.URL, localFile func(dir, credential, key string) string) {
	t.Helper()
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.Mkdir(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := writeFile(t, keys, "operations-token", "op-ax-v1-test\n")
	key := writeFile(t, keys, nodetls.KeyFile, "the node's key\n")
	cfg := bridgeSettings{operations: core, tlsDir: keys, tokenFile: credential, localTokenFile: localFile(dir, credential, key)}
	_, err := startBridge(t.Context(), cfg, http.DefaultTransport, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), mcpTokenFileVar+": names the same file") {
		t.Fatalf("startBridge = %v, want a refusal of %s", err, mcpTokenFileVar)
	}
	for path, want := range map[string]string{credential: "op-ax-v1-test\n", key: "the node's key\n"} { // #nosec G101 -- a fake token this test wrote
		if got, err := os.ReadFile(filepath.Clean(path)); err != nil || string(got) != want {
			t.Errorf("%s now holds %q (%v)", path, got, err)
		}
	}
}
