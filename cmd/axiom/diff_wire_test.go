//go:build diff

package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestAxiomMatchesNewAPIOnTheWire sends the same requests to Axiom and to the official new-api image and diffs what
// comes back: status, headers, JSON bodies, server-sent events. Both are served by one fake upstream that replays the
// protocol samples, each format by a channel or an endpoint of the same format, so what is compared is how each relays
// a reply and an error, not how it converts a format. A difference must be explained by a rule (diff_rules_test.go) or
// the case fails, and so must a rule that explains nothing. `just diff` runs it; it is not part of `just check`, which
// would pull an image and start a container for every run.
func TestAxiomMatchesNewAPIOnTheWire(t *testing.T) {
	up := newDiffUpstream(t)
	api := startNewAPI(t, up)
	w, n := newDiffNode(t, up)
	rules := diffRules()
	for _, c := range diffCases() {
		t.Run(c.name, func(t *testing.T) {
			theirs := send(t, api.base, "", orKey(c.key, api.key), c)
			theirs.body = newAPIRequestID.ReplaceAll(theirs.body, nil)
			ours := send(t, "http://"+n.Public, "api.main.example", orKey(c.key, w.richKey), c)
			for _, d := range compareReplies(theirs, ours) {
				explainOrFail(t, rules, c.name, d)
			}
		})
	}
	for _, r := range rules {
		if r.hits == 0 {
			t.Errorf("the rule for %q explains no difference: remove it", r.path)
		}
	}
}

// newAPIRequestID is the " (request id: ...)" new-api ends every message of its errors with. It is its own request id,
// which Axiom does not copy (the request id header is a difference of its own), so it is taken out of the words.
var newAPIRequestID = regexp.MustCompile(`\s*\(request id: [^)]*\)`)

func orKey(override, key string) string {
	if override != "" {
		return override
	}
	return key
}

// explainOrFail logs a difference with its rule, or fails the case when no rule explains it.
func explainOrFail(t *testing.T, rules []*diffRule, name string, d difference) {
	t.Helper()
	line := fmt.Sprintf("%s: new-api %s, Axiom %s", d.at, d.newAPI, d.axiom)
	for _, r := range rules {
		if r.matches(name, d) {
			r.hits++
			t.Logf("%s [allowed] %s", line, r.reason)
			return
		}
	}
	t.Errorf("%s [unexplained]", line)
}

// send posts a case to a gateway; host, when set, is the Host header a gateway that serves by domain needs.
func send(t *testing.T, base, host, key string, c diffCase) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+c.path, strings.NewReader(c.body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	for i := 0; i+1 < len(c.headers); i += 2 {
		req.Header.Set(c.headers[i], c.headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test client
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, body}
}

// newDiffNode starts a node that serves the callpath world's Claude model by Messages and its GPT model by Responses
// from the fake upstream, each sold at one and a half times its reference price.
func newDiffNode(t *testing.T, up *diffUpstream) (*callWorld, runningNode) {
	t.Helper()
	w := newCallWorld(t)
	w.declareAbilities()
	// The endpoints call the upstream by the names their clients use, the way a gateway in front of one upstream does.
	if err := pgtest.Run(t.Context(), w.database.Superuser,
		"UPDATE suppliers.suppliers SET origin = '"+up.server.URL+"'",
		"UPDATE suppliers.endpoints SET upstream_name = 'claude-opus-5-5' WHERE upstream_name = 'up-claude'",
		"UPDATE suppliers.endpoints SET upstream_name = 'gpt-6-astra' WHERE upstream_name = 'up-gpt'"); err != nil {
		t.Fatal(err)
	}
	n := w.start()
	w.price()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
	return w, n
}

// declareAbilities makes the two models able to take a picture and to reason, which the callpath world's do not say:
// a request that needs an ability its model lacks is not routed.
func (w *callWorld) declareAbilities() {
	w.t.Helper()
	cat, err := catalog.Open(w.db, pg.PlatformScope(), catalog.Config{MaxStale: time.Hour})
	if err != nil {
		w.t.Fatal(err)
	}
	spec := strings.Replace(servedSpec, `"abilities":["tools","web_search"]`, `"abilities":["tools","images","documents","reasoning","web_search"]`, 1)
	for _, slug := range []string{"anthropic/claude-opus-5-5", "openai/gpt-6-astra"} {
		id := w.models[slug]
		rev, err := strconv.ParseInt(w.query("SELECT rev::text FROM catalog.model WHERE id = $1", id), 10, 64)
		if err != nil {
			w.t.Fatal(err)
		}
		op := catalog.Op{Actor: audit.System("diff.test"), Key: uuid.NewV7(), Reason: "abilities of the differential test"}
		if _, err := cat.UpdateSpec(w.t.Context(), catalog.UpdateSpecRequest{
			Op:    op,
			Model: catalog.ModelID(id), Rev: rev, Spec: jsonx.Raw(spec),
		}); err != nil {
			w.t.Fatal(err)
		}
	}
}
