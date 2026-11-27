//go:build contract

package newapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// liveImage is new-api v1.0.0-rc.51, the image the adapter was last checked against by hand, pinned by digest so that a
// run today and one in a year talk to the same program. Moving it is a decision: run `just relays` with
// AXIOM_RECORD_TESTDATA=1, read what changed in testdata/ and decide what the adapter does about each change.
const liveImage = "calciumion/new-api@sha256:d6effcdbc58175494765c0cc5954bdacb2300516eec79ba1a9c61f9b9fae7b49"

const (
	liveRoot     = "root"
	liveUser     = "alice"
	livePassword = "live-test-password-1"
)

// liveScopes are the permissions Axiom reads with (the adapter's every request is one of them); liveNarrow lacks the one
// that reads the log.
var (
	liveScopes = []string{"api_key:read", "api_key:write", "api_key:reveal", "profile:read", "wallet:read", "usage:read"}
	liveNarrow = []string{"api_key:read", "api_key:write", "api_key:reveal", "profile:read", "wallet:read"}
)

// live is a running new-api set up through its own API like a supplier's account: a root administrator, an ordinary user
// with quota and two personal access tokens, two groups of ratios 1 and 1.5, and a channel for each format to the fake
// upstream, whose models are priced.
type live struct {
	t              *testing.T
	base           string
	root, session  string // dashboard sessions of the administrator and of the user
	userID         int
	pat, narrowPAT string
	clock          *adaptertest.Clock
	scrub          *adaptertest.Scrubber
	recorded       map[string]string
}

func startLive(t *testing.T) *live {
	t.Helper()
	reach := adaptertest.HostReach(false)
	up := adaptertest.StartUpstream(t, reach)
	l := &live{t: t, clock: &adaptertest.Clock{}, scrub: adaptertest.NewScrubber(), recorded: map[string]string{}}
	l.base = adaptertest.StartSystem(t, reach, up, adaptertest.System{
		Name: "new-api", Image: liveImage, Port: "3000", PortEnv: "PORT", ReadyPath: "/api/status",
		Env: map[string]string{"SESSION_SECRET": strings.Repeat("live-test-session-", 2), "TZ": "UTC"}, // a throwaway container's, not a credential
	})
	l.call(http.MethodPost, "/api/setup", "", map[string]any{
		"username": liveRoot, "password": livePassword, "confirmPassword": livePassword, "SelfUseModeEnabled": false, "DemoSiteEnabled": false,
	})
	l.root, _ = l.login(liveRoot)
	// It refuses requests with 503 while the machine's CPU is above 90%, which a CI server building other branches is.
	l.setOption("performance_setting.monitor_enabled", "false")
	l.setOption("GroupRatio", `{"default":1,"premium":1.5}`)
	l.setOption("UserUsableGroups", `{"default":"Default group","premium":"Premium group"}`)
	l.setOption("ModelRatio", fmt.Sprintf(`{%q:2.5,%q:5}`, adaptertest.UpstreamOpenAIModel, adaptertest.UpstreamAnthropicModel))
	l.setOption("CompletionRatio", fmt.Sprintf(`{%q:4,%q:5}`, adaptertest.UpstreamOpenAIModel, adaptertest.UpstreamAnthropicModel))
	for _, ch := range []struct {
		typ   int // OpenAI's and Anthropic's channel types (constant/channel.go)
		model string
	}{{1, adaptertest.UpstreamOpenAIModel}, {14, adaptertest.UpstreamAnthropicModel}} {
		l.call(http.MethodPost, "/api/channel/", l.root, map[string]any{"mode": "single", "channel": map[string]any{
			"type": ch.typ, "name": "upstream-" + ch.model, "key": adaptertest.UpstreamKey, "base_url": reach.UpstreamURL(up),
			"models": ch.model, "group": "default,premium", "status": 1,
		}})
	}
	l.call(http.MethodPost, "/api/user/register", "", map[string]any{"username": liveUser, "password": livePassword})
	l.session, l.userID = l.login(liveUser)
	l.call(http.MethodPost, "/api/user/manage", l.root, map[string]any{"id": l.userID, "action": "add_quota", "mode": "add", "value": 5000000})
	l.pat, l.narrowPAT = l.createPAT("axiom", liveScopes), l.createPAT("axiom-narrow", liveNarrow)
	l.scrub.Secret(l.pat, "<ALICE_PAT>")
	l.scrub.Secret(l.narrowPAT, "<ALICE_PAT_NARROW>")
	return l
}

// call sends a request as token (a session or personal access token; none for an anonymous call) and returns the data
// of its answer, which new-api reports as a 200 with success false when it fails.
func (l *live) call(method, path, token string, body any) adaptertest.Answer {
	l.t.Helper()
	var header http.Header
	if token != "" {
		header = adaptertest.Bearer(token)
	}
	return l.callWith(method, path, header, body)
}

// callWith is call with the headers a login of another form is sent in.
func (l *live) callWith(method, path string, header http.Header, body any) adaptertest.Answer {
	l.t.Helper()
	a := adaptertest.Send(l.t, method, l.base, path, header, body)
	var env struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := jsonx.Unmarshal(a.Body, &env); err != nil || !env.Success {
		l.t.Fatalf("new-api %s %s: %d %.300s", method, path, a.Status, a.Body)
	}
	return a
}

func (l *live) login(user string) (session string, id int) {
	l.t.Helper()
	var out struct {
		Data struct {
			Access string `json:"access_token"`
			User   struct {
				ID int `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	l.call(http.MethodPost, "/api/user/login", "", map[string]any{"username": user, "password": livePassword}).Decode(l.t, &out)
	return out.Data.Access, out.Data.User.ID
}

// setOption sets one of the system options the administrator edits.
func (l *live) setOption(key, value string) {
	l.t.Helper()
	l.call(http.MethodPut, "/api/option/", l.root, map[string]any{"key": key, "value": value})
}

// createPAT makes a personal access token with exactly scopes. new-api lets only a dashboard session make one, and only
// with a proof of the password bound to the grant.
func (l *live) createPAT(name string, scopes []string) string {
	l.t.Helper()
	grant := map[string]any{"scopes": scopes, "expires_at": 0}
	var proof struct {
		Data struct {
			Token string `json:"proof_token"`
		} `json:"data"`
	}
	l.call(http.MethodPost, "/api/verify", l.session, map[string]any{
		"method": "password", "scope": "access_token.generate", "context": grant, "password": livePassword,
	}).Decode(l.t, &proof)
	var made struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	a := adaptertest.Send(l.t, http.MethodPost, l.base, "/api/user/access_tokens",
		http.Header{"Authorization": {"Bearer " + l.session}, "X-Security-Proof": {proof.Data.Token}},
		map[string]any{"name": name, "scopes": scopes, "expires_at": 0})
	a.Decode(l.t, &made)
	if !strings.HasPrefix(made.Data.Token, "nap_") {
		l.t.Fatalf("new-api did not make the access token %s: %d %.300s", name, a.Status, a.Body)
	}
	return made.Data.Token
}

// setPricingModule turns the console's pricing module on or off. Off, new-api answers /api/pricing with 403 to everyone
// but its root administrator, which is every call an ordinary account's token makes.
func (l *live) setPricingModule(enabled bool) {
	l.t.Helper()
	l.setOption("HeaderNavModules", fmt.Sprintf(`{"pricing":{"enabled":%t,"requireAuth":false}}`, enabled))
}

// limitLogRange sets the longest time range a user may read the log over; a longer range is answered with success false.
func (l *live) limitLogRange(days int) {
	l.t.Helper()
	l.setOption("log_setting.max_range_days", strconv.Itoa(days))
}

// adapterClient is the client the module builds for a supplier: through platform/egress to the system's origin, with the
// adapter's buckets and the live clock.
func (l *live) adapterClient(a *Adapter) *client.Client {
	l.t.Helper()
	c, err := client.New(l.base, egress.NewForTesting(egresstest.Loopback(l.t)), client.NewLimiter(a.Buckets(), l.clock.Now), l.clock.Now)
	if err != nil {
		l.t.Fatal(err)
	}
	return c
}

type liveLogin struct{ raw []byte }

func (s liveLogin) Load(context.Context) ([]byte, int64, error) { return s.raw, 1, nil }
func (liveLogin) Swap(context.Context, int64, []byte) error     { return nil }

func (l *live) open(a *Adapter, c adapter.Client, pat string) (adapter.Manager, error) {
	l.t.Helper()
	raw, err := EncodeLogin(Login{PAT: secret.New(pat)})
	if err != nil {
		l.t.Fatal(err)
	}
	return a.Open(l.t.Context(), c, liveLogin{raw})
}

// record asks the system what the adapter's request would ask, and keeps the answer as a sample under name when the run
// records. label stands for the token in the sample.
func (l *live) record(name, method, path, token, label string) adaptertest.Answer {
	l.t.Helper()
	var header http.Header
	if token != "" {
		header, label = adaptertest.Bearer(token), "Bearer "+label
	}
	return l.recordWith(name, method, path, header, label)
}

// recordWith is record with the headers a login of another form is sent in; auth is what the sample shows as its
// Authorization header.
func (l *live) recordWith(name, method, path string, header http.Header, auth string) adaptertest.Answer {
	l.t.Helper()
	a := adaptertest.Send(l.t, method, l.base, path, header, nil)
	l.recorded[name] = l.sample(name, method, path, auth, a)
	return a
}

// sample is the text of a recorded exchange in the form of the other files of testdata/, with every secret and address
// of this run scrubbed.
func (l *live) sample(name, method, path, auth string, a adaptertest.Answer) string {
	l.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n%s %s\n", name, method, l.scrub.Text(path))
	if auth != "" {
		fmt.Fprintf(&b, "> Authorization: %s\n", auth)
	}
	fmt.Fprintf(&b, "< status: %d  (elapsed %.2fs)\n", a.Status, a.Elapsed.Seconds())
	for _, h := range []string{"Content-Type", "Retry-After"} {
		if v := a.Header.Get(h); v != "" {
			fmt.Fprintf(&b, "< %s: %s\n", h, v)
		}
	}
	fmt.Fprintf(&b, "< %s: sample-request-id\n< body: %s\n", requestIDHeader, l.scrub.Text(strings.TrimSpace(string(a.Body))))
	if leaks := l.scrub.Leaks(b.String()); len(leaks) > 0 {
		l.t.Fatalf("sample %s keeps %v", name, leaks)
	}
	return b.String()
}

// writeSamples rewrites testdata/ with the samples of this run when AXIOM_RECORD_TESTDATA=1.
func (l *live) writeSamples() {
	l.t.Helper()
	if !adaptertest.Recording() {
		return
	}
	for name, text := range l.recorded {
		if err := os.WriteFile(filepath.Join("testdata", name+".txt"), []byte(text), 0o600); err != nil {
			l.t.Fatal(err)
		}
	}
	l.t.Logf("recorded %d samples in testdata/", len(l.recorded))
}

// logRecord is one record of /api/log/self as the system states it.
type logRecord struct {
	RequestID string `json:"request_id"`
	Model     string `json:"model_name"`
	Quota     int64  `json:"quota"`
	TokenID   int64  `json:"token_id"`
	Prompt    int64  `json:"prompt_tokens"`
	Output    int64  `json:"completion_tokens"`
}

// logs reads the user's consumption log from new-api itself, once it holds at least n records (it writes them after the
// answer has gone out).
func (l *live) logs(n int) map[string]logRecord {
	l.t.Helper()
	var page struct {
		Data struct {
			Items []logRecord `json:"items"`
		} `json:"data"`
	}
	for range 60 {
		l.call(http.MethodGet, "/api/log/self?type=2&page_size=100&p=1", l.pat, nil).Decode(l.t, &page)
		if len(page.Data.Items) >= n {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(page.Data.Items) < n {
		l.t.Fatalf("the log holds %d consumption records, want %d", len(page.Data.Items), n)
	}
	byID := map[string]logRecord{}
	for _, r := range page.Data.Items {
		byID[r.RequestID] = r
	}
	return byID
}

// relay is one call of the made key and what a charge for it must be.
type relay struct {
	key, path, model string
	stream           bool
	wantQuota        int64
	ref              adapter.ChargeRef
}

// send makes the call and keeps the request id the relay answered with, as the module's call code reads it.
func (l *live) send(a *Adapter, r *relay) {
	l.t.Helper()
	header := adaptertest.Bearer(r.key)
	body := map[string]any{"model": r.model, "stream": r.stream}
	if strings.HasSuffix(r.path, "/messages") {
		header.Set("Anthropic-Version", "2023-06-01")
		body["max_tokens"] = 100
		body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
	} else if strings.HasSuffix(r.path, "/responses") {
		body["input"] = "hi"
	} else {
		body["messages"] = []map[string]string{{"role": "user", "content": "hi"}}
	}
	ans := adaptertest.Send(l.t, http.MethodPost, l.base, r.path, header, body)
	ref, ok := a.Calls().ChargeRef(ans.Header)
	if ans.Status != http.StatusOK || !ok {
		l.t.Fatalf("%s (stream %t): %d, charge reference %q found %t: %.300s", r.path, r.stream, ans.Status, ref, ok, ans.Body)
	}
	r.ref = ref
}
