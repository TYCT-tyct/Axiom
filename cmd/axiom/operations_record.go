package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/operations"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// runIDPattern is the contract's X-Axiom-Run-ID, which the operation log's check repeats: a run ID that breaks it is a
// 400, and is not recorded.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// maxReason is the longest reason the contract and the log accept, in characters.
const maxReason = 1000

// heldBody reads a write's body for its record and leaves it for the handler to read again; a read has none.
func heldBody(r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

// record writes the request's entry in the operation log, on its own deadline: a request that ran out of time still
// leaves its record. A read's reason is in its query, a write's in its body, whose other members are its input.
func (g *agentGuard) record(ctx context.Context, r *http.Request, pattern, tool string, agent accounts.Agent, body []byte, answer *bufferedResponse, started time.Time) error {
	entry := operations.LoggedCall{
		Credential: uuid.UUID(agent.ID()), Tool: tool, Reason: r.URL.Query().Get("reason"), Input: inputOf(pattern, r),
		Status: answer.statusCode(), Error: answer.errorCode(), RequestID: httpserver.RequestID(ctx), Started: started,
		Ended: time.Now(),
	}
	if r.Method != http.MethodGet {
		entry.Reason, entry.Operation = bodyInput(body, entry.Input), answer.operation()
	}
	entry.Reason = truncate(entry.Reason, maxReason)
	if run := r.Header.Get("X-Axiom-Run-Id"); runIDPattern.MatchString(run) {
		entry.Run = run
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordDeadline)
	defer cancel()
	return g.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := operations.RecordCall(ctx, tx, entry)
		return err
	})
}

// truncate is s cut to at most n characters, for a reason the contract refused as too long but the log still keeps.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// inputOf is what the request asked with: its path parameters by name, as the route pattern names them, and its query
// parameters but the reason, which the record keeps apart.
func inputOf(pattern string, r *http.Request) map[string]any {
	input := map[string]any{}
	if path := pathInput(pattern, r.URL.Path); len(path) > 0 {
		input["path"] = path
	}
	if query := queryInput(r); len(query) > 0 {
		input["query"] = query
	}
	return input
}

// bodyInput puts a write's JSON body but its reason into input, and returns the reason; a body that is not a JSON
// object, which the contract refused, is left out.
func bodyInput(body []byte, input map[string]any) string {
	var members map[string]any
	if jsonx.Unmarshal(body, &members) != nil || members == nil {
		return ""
	}
	reason, _ := members["reason"].(string)
	delete(members, "reason")
	if len(members) > 0 {
		input["body"] = members
	}
	return reason
}

// pathInput is the path's parameters by the names the route pattern gives them.
func pathInput(pattern, path string) map[string]any {
	_, route, _ := strings.Cut(pattern, " ")
	names, values := strings.Split(route, "/"), strings.Split(path, "/")
	params := map[string]any{}
	if len(names) != len(values) {
		return params
	}
	for i, name := range names {
		if strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") {
			params[name[1:len(name)-1]] = values[i]
		}
	}
	return params
}

// queryInput is the query's parameters but the reason: one value as itself, several as a list.
func queryInput(r *http.Request) map[string]any {
	query := map[string]any{}
	for name, vals := range r.URL.Query() {
		if name == "reason" {
			continue
		}
		if len(vals) == 1 {
			query[name] = vals[0]
		} else {
			query[name] = vals
		}
	}
	return query
}
