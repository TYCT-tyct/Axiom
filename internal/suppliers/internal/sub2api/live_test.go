//go:build contract

package sub2api

import (
	"context"
	"encoding/json/jsontext" //nolint:depguard // lays out the recorded samples like the files of testdata/; jsonx only reads
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// The images of sub2api 0.2.25 and what it runs on, pinned by digest so that a run today and one in a year talk to the
// same program. Moving one is a decision: run `just relays` with AXIOM_RECORD_TESTDATA=1, read what changed in testdata/
// and decide what the adapter does about each change.
const (
	liveImage    = "weishaw/sub2api@sha256:f4dcb7e9cc0f0d4006abb425391c9521331013764720f08b0f111059d6a78e18"    // 0.2.25
	livePostgres = "postgres:18-alpine@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71" // 18
	liveRedis    = "redis:8-alpine@sha256:5af80107c1eeecb484a2269b80a26d12bfa6521787da4663bd9f19f60a163483"     // 8
	liveAdmin    = "admin@example.test"
	liveUserMail = "alice@example.test"
	livePassword = "live-test-password-1"
)

// liveGroup is a group the test sets up: its name, its platform and rate multiplier, and whether it takes Claude Code only.
type liveGroup struct {
	name, platform string
	rate           float64
	claudeCodeOnly bool
	id             int64
}

// live is a running sub2api set up through its own API like a supplier's account: an administrator, an ordinary user with a
// balance, groups of two platforms at multipliers 1 and 1.5 (one of them for Claude Code only), an API-key account of each
// platform that relays to the fake upstream, and a channel that prices the models, shown by the model plaza.
type live struct {
	t        *testing.T
	base     string
	admin    string // the administrator's access token
	session  string // the user's access token, for what the adapter's own client does not hand back
	groups   []*liveGroup
	clock    *adaptertest.Clock
	scrub    *adaptertest.Scrubber
	recorded map[string]string
	requests int // request ids placeholders given so far
}

func startLive(t *testing.T) *live {
	t.Helper()
	reach := adaptertest.HostReach(true)
	up := adaptertest.StartUpstream(t, reach)
	l := &live{t: t, clock: &adaptertest.Clock{}, scrub: adaptertest.NewScrubber(), recorded: map[string]string{}}
	db := startBackend(t, livePostgres, "5432", map[string]string{"POSTGRES_USER": "sub2api", "POSTGRES_PASSWORD": livePassword, "POSTGRES_DB": "sub2api"})
	cache := startBackend(t, liveRedis, "6379", nil)
	l.base = adaptertest.StartSystem(t, reach, up, adaptertest.System{
		Name: "sub2api", Image: liveImage, Port: "8080", PortEnv: "SERVER_PORT", ReadyPath: "/health",
		Env: map[string]string{
			"AUTO_SETUP": "true", "SERVER_HOST": "0.0.0.0", "SERVER_PORT": "8080", "SERVER_MODE": "release", "TZ": "UTC",
			"DATABASE_HOST": db, "DATABASE_PORT": "5432", "DATABASE_USER": "sub2api", "DATABASE_PASSWORD": livePassword, "DATABASE_DBNAME": "sub2api", "DATABASE_SSLMODE": "disable",
			"REDIS_HOST": cache, "REDIS_PORT": "6379",
			"ADMIN_EMAIL": liveAdmin, "ADMIN_PASSWORD": livePassword, "JWT_SECRET": strings.Repeat("0123456789abcdef", 4),
			// The fake upstream is a private address on plain HTTP, which it refuses to relay to by default.
			"SECURITY_URL_ALLOWLIST_ENABLED": "false", "SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP": "true", "SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS": "true",
		},
	})
	l.admin = l.login(liveAdmin)
	l.acceptAdminCompliance()
	l.groups = []*liveGroup{
		{name: "claude-1", platform: "anthropic", rate: 1},
		{name: "claude-15", platform: "anthropic", rate: 1.5},
		{name: "gpt-1", platform: "openai", rate: 1},
		{name: "claude-code", platform: "anthropic", rate: 1, claudeCodeOnly: true},
		{name: "kimi-1", platform: "kimi", rate: 1},
		{name: "glm-1", platform: "zhipu", rate: 1},
		{name: "gemini-1", platform: "gemini", rate: 1},
	}
	for _, g := range l.groups {
		l.createGroup(g)
	}
	for _, acc := range []struct {
		name, platform string
		groups         []string
	}{
		{"upstream-anthropic", "anthropic", []string{"claude-1", "claude-15", "claude-code"}},
		{"upstream-openai", "openai", []string{"gpt-1"}},
		{"upstream-kimi", "kimi", []string{"kimi-1"}},
		{"upstream-zhipu", "zhipu", []string{"glm-1"}},
		{"upstream-gemini", "gemini", []string{"gemini-1"}},
	} {
		l.createAccount(acc.name, acc.platform, reach.UpstreamURL(up), acc.groups)
	}
	l.call(http.MethodPost, "/api/v1/admin/users", l.admin, map[string]any{"email": liveUserMail, "password": livePassword, "balance": 10, "concurrency": 5})
	l.session = l.login(liveUserMail)
	l.createPricingChannel()
	l.setModelPlaza(true)
	return l
}

// startBackend runs a database or cache next to sub2api on Docker's default network and returns the address of its
// container there, which is where sub2api reaches it from the container's own network or from the host's.
func startBackend(t *testing.T, image, port string, env map[string]string) string {
	t.Helper()
	ctx := t.Context()
	c, err := containertest.Start(ctx, func() (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(ctx, image, testcontainers.WithEnv(env), testcontainers.WithExposedPorts(port+"/tcp"),
			testcontainers.WithWaitStrategy(wait.ForListeningPort(port+"/tcp").WithStartupTimeout(2*time.Minute)), containertest.WithHostPolicy(ctx))
	})
	if err != nil {
		t.Fatalf("starting %s: %v", image, err)
	}
	containertest.Cleanup(t, c, image)
	ip, err := c.ContainerIP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ip
}

// liveEnvelope is sub2api's {code, message, data} wrapper; code 0 is success.
type liveEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call sends a request as token (none for an anonymous call) and returns the answer of a call that succeeded.
func (l *live) call(method, path, token string, body any) adaptertest.Answer {
	l.t.Helper()
	var header http.Header
	if token != "" {
		header = adaptertest.Bearer(token)
	}
	a := adaptertest.Send(l.t, method, l.base, path, header, body)
	var env liveEnvelope
	if err := jsonx.Unmarshal(a.Body, &env); err != nil || a.Status != http.StatusOK || env.Code != 0 {
		l.t.Fatalf("sub2api %s %s: %d %.300s", method, path, a.Status, a.Body)
	}
	return a
}

func (l *live) login(email string) string {
	l.t.Helper()
	var out struct {
		Data struct {
			Access string `json:"access_token"`
		} `json:"data"`
	}
	l.call(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": email, "password": livePassword}).Decode(l.t, &out)
	return out.Data.Access
}

// acceptAdminCompliance gives the confirmation phrase sub2api asks of an administrator before any administrative call.
// It is the phrase of a Docker container that lives for the length of this test, on a loopback address, relaying to a
// fake upstream for nobody; the commitment it confirms is not this test's to make for anyone else, so the phrase is kept
// here, in one place, for the person who owns this suite to confirm or to veto.
func (l *live) acceptAdminCompliance() {
	l.t.Helper()
	l.call(http.MethodPost, "/api/v1/admin/compliance/accept", l.admin, map[string]any{
		"phrase": "I have read, understood, and agree to the Sub2API Deployment and Operation Compliance Commitment", "language": "en",
	})
}

// createGroup makes a standard group (paid from the balance, not from a subscription).
func (l *live) createGroup(g *liveGroup) {
	l.t.Helper()
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	l.call(http.MethodPost, "/api/v1/admin/groups", l.admin, map[string]any{
		"name": g.name, "platform": g.platform, "rate_multiplier": g.rate, "subscription_type": "standard", "claude_code_only": g.claudeCodeOnly,
	}).Decode(l.t, &out)
	g.id = out.Data.ID
}

// createAccount makes an API-key account of the platform that relays to baseURL, in the named groups.
func (l *live) createAccount(name, platform, baseURL string, groups []string) {
	l.t.Helper()
	var ids []int64
	for _, n := range groups {
		ids = append(ids, l.group(n).id)
	}
	l.call(http.MethodPost, "/api/v1/admin/accounts", l.admin, map[string]any{
		"name": name, "platform": platform, "type": "apikey", "concurrency": 5, "priority": 1, "group_ids": ids,
		"credentials": map[string]any{"api_key": adaptertest.UpstreamKey, "base_url": baseURL},
	})
}

func (l *live) group(name string) *liveGroup {
	for _, g := range l.groups {
		if g.name == name {
			return g
		}
	}
	l.t.Fatalf("no group %s", name)
	return nil
}

// createPricingChannel prices the two fake models per token, in dollars, for every group: what the model plaza shows and
// what a request is billed at.
func (l *live) createPricingChannel() {
	l.t.Helper()
	var ids []int64
	for _, g := range l.groups {
		ids = append(ids, g.id)
	}
	price := func(platform, model string, in, out float64) map[string]any {
		return map[string]any{"platform": platform, "models": []string{model}, "billing_mode": "token", "input_price": in, "output_price": out}
	}
	l.call(http.MethodPost, "/api/v1/admin/channels", l.admin, map[string]any{"name": "prices", "group_ids": ids, "model_pricing": []any{
		price("anthropic", adaptertest.UpstreamAnthropicModel, 0.000005, 0.000025), price("openai", adaptertest.UpstreamOpenAIModel, 0.0000025, 0.00001),
		price("kimi", adaptertest.UpstreamOpenAIModel, 0.0000025, 0.00001), price("zhipu", adaptertest.UpstreamOpenAIModel, 0.0000025, 0.00001),
		price("gemini", adaptertest.UpstreamGeminiModel, 0.0000025, 0.00001),
	}})
}

// setModelPlaza turns the model plaza, the page that shows each group's models with prices, on or off. Off, its endpoint
// answers 404.
func (l *live) setModelPlaza(enabled bool) {
	l.t.Helper()
	l.call(http.MethodPut, "/api/v1/admin/settings", l.admin, map[string]any{"model_plaza_enabled": enabled})
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

// liveStore keeps the login in memory and hands the version back to Swap, as the module's store does.
type liveStore struct {
	mu      sync.Mutex
	raw     []byte
	version int64
}

func (s *liveStore) Load(context.Context) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw, s.version, nil
}

func (s *liveStore) Swap(_ context.Context, version int64, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if version != s.version {
		return adapter.ErrStaleLogin
	}
	s.raw, s.version = raw, s.version+1
	return nil
}

func (l *live) store(email string) *liveStore {
	l.t.Helper()
	raw, err := Login{Email: email, Password: secret.New(livePassword)}.Encode()
	if err != nil {
		l.t.Fatal(err)
	}
	return &liveStore{raw: raw, version: 1}
}

// record keeps the answer to a GET as a sample under name when the run records. token (or none) is what the request is
// sent with.
func (l *live) record(name, path, token string) adaptertest.Answer {
	l.t.Helper()
	var header http.Header
	if token != "" {
		header = adaptertest.Bearer(token)
	}
	a := adaptertest.Send(l.t, http.MethodGet, l.base, path, header, nil)
	l.keep(name, http.MethodGet, path, a)
	return a
}

// keep is a sample in the form of the other files of testdata/: the request, the status, the one header the call code reads
// and the body, with every secret and address of this run scrubbed.
func (l *live) keep(name, method, path string, a adaptertest.Answer) {
	l.t.Helper()
	header := map[string]string{}
	if id := a.Header.Get("X-Client-Request-Id"); id != "" {
		l.requests++
		placeholder := fmt.Sprintf("00000000-0000-4000-8000-%012d", l.requests)
		l.scrub.Secret(id, placeholder)
		header["X-Client-Request-Id"] = placeholder
	}
	head, err := jsonx.MarshalCanonical(header)
	if err != nil {
		l.t.Fatal(err)
	}
	body := strings.TrimSpace(string(a.Body))
	if body == "" {
		body = "null"
	}
	doc := jsontext.Value(fmt.Sprintf(`{"method":%q,"path":%q,"status":%d,"header":%s,"body":%s}`, method, path, a.Status, head, l.scrub.Text(body)))
	if err := doc.Indent(jsontext.WithIndent(" ")); err != nil {
		l.t.Fatalf("sample %s: %v: %.200s", name, err, a.Body)
	}
	if leaks := l.scrub.Leaks(string(doc)); len(leaks) > 0 {
		l.t.Fatalf("sample %s keeps %v", name, leaks)
	}
	l.recorded[name] = string(doc) + "\n"
}

// writeSamples rewrites testdata/ with the samples of this run when AXIOM_RECORD_TESTDATA=1.
func (l *live) writeSamples() {
	l.t.Helper()
	if !adaptertest.Recording() {
		return
	}
	for name, text := range l.recorded {
		if err := os.WriteFile(filepath.Join("testdata", name+".json"), []byte(text), 0o600); err != nil {
			l.t.Fatal(err)
		}
	}
	l.t.Logf("recorded %d samples in testdata/", len(l.recorded))
}

// usageRecord is one record of /api/v1/usage as the system states it.
type usageRecord struct {
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	KeyID     number `json:"api_key_id"`
	Cost      number `json:"actual_cost"`
	Input     int64  `json:"input_tokens"`
	Output    int64  `json:"output_tokens"`
}

// usage reads the user's records from sub2api itself, once it holds at least n of them (it writes them after the answer
// has gone out).
func (l *live) usage(n int) map[string]usageRecord {
	l.t.Helper()
	today := l.clock.Now().UTC()
	path := "/api/v1/usage?page=1&page_size=200&timezone=UTC&sort_by=created_at&sort_order=desc&start_date=" +
		today.Add(-24*time.Hour).Format(time.DateOnly) + "&end_date=" + today.Add(24*time.Hour).Format(time.DateOnly)
	var page struct {
		Data struct {
			Items []usageRecord `json:"items"`
		} `json:"data"`
	}
	for range 60 {
		l.call(http.MethodGet, path, l.session, nil).Decode(l.t, &page)
		if len(page.Data.Items) >= n {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(page.Data.Items) < n {
		l.t.Fatalf("the usage list holds %d records, want %d", len(page.Data.Items), n)
	}
	byID := map[string]usageRecord{}
	for _, r := range page.Data.Items {
		byID[r.RequestID] = r
	}
	return byID
}
