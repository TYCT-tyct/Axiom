package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/nodetls"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/privfile"
)

// The MCP bridge of the ops node (docs/domains/operations.md「MCP 映射」): it offers the operations API to Hermes on this
// machine as MCP tools, one per operation of the contract this program embeds (mcp_tools.go), and forwards each call to
// a core's operations listener with the node's certificate and the operations credential. It holds no data. The
// credential is read from its file for every call, so neither the model nor a tool's result ever carries it, and a
// rotation needs only the file replaced. Every rule (permission, approval, cap, log) is the core's; the bridge only
// names the tools the credential may call.

const (
	// bridgeCallLimit bounds one forwarded call: the core gives a write 30 seconds (writeDeadline), and the margin lets
	// its answer arrive rather than the bridge giving up first and leaving the outcome unknown.
	bridgeCallLimit = 40 * time.Second
	// bridgeStartReason is what the bridge's own whoami call records in the operation log.
	bridgeStartReason = "the MCP bridge reads its credential to list the tools it may offer"
	// maxOperationsToken bounds the credential file's read; a token is far shorter.
	maxOperationsToken = 256
)

// mcpBridge forwards tool calls to the core.
type mcpBridge struct {
	core      *url.URL
	client    *http.Client
	tokenFile string
	logger    *slog.Logger
}

// startBridge prepares the bridge: the tools of the embedded contract, narrowed to what the credential may call by
// asking the core who it is, and the token Hermes must present, written to its file. A core that does not answer or
// refuses the credential stops the start: the bridge never offers tools it cannot call, and never falls back to another
// interface (docs/domains/operations.md「上线与兼容」).
func startBridge(ctx context.Context, cfg bridgeSettings, transport http.RoundTripper, logger *slog.Logger) (http.Handler, error) {
	doc, err := operationsapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("the operations contract: %w", err)
	}
	tools, err := bridgeTools(doc)
	if err != nil {
		return nil, fmt.Errorf("the operations contract: %w", err)
	}
	if err := guardLocalTokenFile(cfg); err != nil {
		return nil, err
	}
	// A redirect is never followed: it would carry the credential, and a write's body, past the mutual TLS to wherever
	// the answer points, a plain-HTTP port of the same host included. The core never redirects, so one is an error.
	client := &http.Client{
		Transport: transport, Timeout: bridgeCallLimit,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	b := &mcpBridge{core: cfg.operations, client: client, tokenFile: cfg.tokenFile, logger: logger}
	me, err := b.whoami(ctx, tools)
	if err != nil {
		return nil, err
	}
	local, err := writeLocalToken(cfg.localTokenFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", mcpTokenFileVar, err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "axiom-operations", Version: doc.Info.Version}, nil)
	offered := 0
	for _, t := range tools {
		if mayCall(t.rules, me) {
			server.AddTool(t.tool, b.handler(t))
			offered++
		}
	}
	logger.Info("MCP bridge ready", "credential", me.Id, "tools", offered, "contract", doc.Info.Version)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: logger})
	return requireLocalToken(local, http.NewCrossOriginProtection().Handler(handler)), nil
}

// guardLocalTokenFile refuses a token file for Hermes that is a file the bridge reads its identity from, under any name
// (a dot segment, a link, another mount of the directory): the rename that writes the token would replace the
// credential or the node's key, and the bridge would go on to report ready with neither. It runs before anything is
// written. A path that names no file yet cannot be one of them.
func guardLocalTokenFile(cfg bridgeSettings) error {
	target, err := os.Stat(cfg.localTokenFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", mcpTokenFileVar, err)
	}
	kept := map[string]string{operationsTokenFileVar: cfg.tokenFile}
	if cfg.tlsDir != "" {
		for _, name := range []string{nodetls.CertFile, nodetls.KeyFile, nodetls.CAFile} {
			kept[nodeTLSDirVar+"'s "+name] = filepath.Join(cfg.tlsDir, name)
		}
	}
	for what, path := range kept {
		if info, err := os.Stat(path); err == nil && os.SameFile(target, info) {
			return fmt.Errorf("%s: names the same file as %s, which the token would replace", mcpTokenFileVar, what)
		}
	}
	return nil
}

// whoami asks the core for the credential: it is live, and what it holds decides the tools.
func (b *mcpBridge) whoami(ctx context.Context, tools []bridgeTool) (operationsapi.AgentSelf, error) {
	var me operationsapi.AgentSelf
	i := slices.IndexFunc(tools, func(t bridgeTool) bool { return t.tool.Name == "whoami" })
	if i < 0 {
		return me, errors.New("the operations contract has no whoami")
	}
	args, err := jsonx.MarshalCanonical(map[string]string{"reason": bridgeStartReason})
	if err != nil {
		return me, err
	}
	status, body, err := b.forward(ctx, tools[i], args)
	switch {
	case err != nil:
		return me, fmt.Errorf("%s: %w", operationsURLVar, err)
	case status != http.StatusOK:
		return me, fmt.Errorf("%s: the core refused the operations credential with %d: %s", operationsURLVar, status, body)
	}
	if err := jsonx.Unmarshal(body, &me); err != nil {
		return me, fmt.Errorf("%s: the core's whoami: %w", operationsURLVar, err)
	}
	return me, nil
}

// mayCall reports whether the credential holds what an operation needs: its permission, operations.money besides when
// it moves money, and the platform grant when it acts on the platform's own resources. The core checks the same on
// every call; the bridge only leaves out what would be refused.
func mayCall(rules *agentRules, me operationsapi.AgentSelf) bool {
	return slices.Contains(me.Permissions, rules.permission.Name()) &&
		(!rules.money || slices.Contains(me.Permissions, "operations.money")) &&
		(rules.scope != agentPlatform || me.Platform)
}

// handler answers a call of t with the core's answer as JSON text. What the core refuses, and a redirect it should never
// give, is a tool error the model can
// read and act on; so is a call the bridge could not make, and one whose answer it lost, which for a write says to
// repeat it under the same key.
func (b *mcpBridge) handler(t bridgeTool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status, body, err := b.forward(ctx, t, req.Params.Arguments)
		if err != nil {
			text := err.Error()
			if lost := (*lostAnswer)(nil); errors.As(err, &lost) && t.method != http.MethodGet {
				text += "; whether it took effect is unknown: call again with the same idempotency_key"
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}, nil
		}
		if len(body) == 0 {
			body = []byte(http.StatusText(status))
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}, IsError: status >= http.StatusMultipleChoices}, nil
	}
}

// lostAnswer is a call that may have reached the core but whose answer did not come back.
type lostAnswer struct{ err error }

func (e *lostAnswer) Error() string { return e.err.Error() }
func (e *lostAnswer) Unwrap() error { return e.err }

// forward makes the call of t with args on the core with the credential and returns the core's status and body.
func (b *mcpBridge) forward(ctx context.Context, t bridgeTool, args []byte) (int, []byte, error) {
	r, err := t.request(ctx, b.core, args)
	if err != nil {
		return 0, nil, err
	}
	token, err := readOperationsToken(b.tokenFile)
	if err != nil {
		b.logger.Error("the MCP bridge cannot read the operations credential", "variable", operationsTokenFileVar, "error", err)
		return 0, nil, errors.New("the bridge cannot read its operations credential")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.client.Do(r)
	if err != nil {
		return 0, nil, &lostAnswer{fmt.Errorf("the core did not answer: %w", err)}
	}
	body, err := io.ReadAll(resp.Body)
	if err = errors.Join(err, resp.Body.Close()); err != nil {
		return 0, nil, &lostAnswer{fmt.Errorf("the core's answer broke off: %w", err)}
	}
	return resp.StatusCode, body, nil
}

// readOperationsToken reads the operations credential from its file, which only its owner may read, like the node's
// keys. The file is read again for every call, so replacing it is all a rotation needs.
func readOperationsToken(path string) (string, error) {
	file, err := privfile.Open(path, os.Open)
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxOperationsToken+1))
	if err = errors.Join(err, file.Close()); err != nil {
		return "", err
	}
	token := strings.TrimRight(string(raw), "\r\n")
	if len(raw) > maxOperationsToken || !strings.HasPrefix(token, "op-ax-v1-") ||
		strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", errors.New("want one operations credential token, op-ax-v1-…, on one line")
	}
	return token, nil
}

// writeLocalToken makes the token of this start and puts it at path for Hermes, readable by the owner only. It replaces
// the previous start's in one rename, so the file never holds half a token, and a token of an earlier start no longer
// opens the bridge.
func writeLocalToken(path string) (string, error) {
	token := rand.Text()
	file, err := os.CreateTemp(filepath.Dir(path), ".mcp-token-*") // created 0600
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(token + "\n")
	if err = errors.Join(err, file.Close()); err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return "", errors.Join(err, os.Remove(file.Name()))
	}
	return token, nil
}

// requireLocalToken lets through only a request that presents the token of this start, compared in constant time, with
// a body no larger than the operations API's own limit.
func requireLocalToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}
