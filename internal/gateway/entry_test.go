package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/clientip"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// countingBody is a request body that counts how often it was read.
type countingBody struct {
	reads int
	r     io.Reader
}

func (b *countingBody) Read(p []byte) (int, error) { b.reads++; return b.r.Read(p) }
func (*countingBody) Close() error                 { return nil }

// iotestErr is a reader that fails.
type iotestErr struct{ err error }

func (e iotestErr) Read([]byte) (int, error) { return 0, e.err }

// serve runs one request through the handler without a network, as the public listener wraps it.
func (r *rig) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	httpserver.Public(r.handler, slog.New(slog.DiscardHandler), time.Minute).ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, host, path, key string, body io.Reader) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+host+path, body)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

// TestUnrecognisedKeyIsRefusedBeforeTheBodyIsRead: a request whose host or key is not recognised is answered 401, the
// same for every reason, and closes the connection; its body is never read.
func TestUnrecognisedKeyIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	body := messageBody("acme/beta")
	for name, tc := range map[string]struct{ host, key string }{
		"a key that is not known":       {apiHost, "sk-ax-v1-nope"},
		"no key":                        {apiHost, ""},
		"a host that is not registered": {"elsewhere.test", a.key},
		"a console host":                {consoleHost, a.key},
	} {
		t.Run(name, func(t *testing.T) {
			b := &countingBody{r: strings.NewReader(body)}
			req := post(t, tc.host, "/v1/messages", tc.key, b)
			req.ContentLength = int64(len(body))
			rec := r.serve(req)
			code, typ := errorOf(t, rec.Body.String())
			if rec.Code != http.StatusUnauthorized || b.reads != 0 || rec.Header().Get("Connection") != "close" ||
				code != "authentication" || typ != "authentication_error" {
				t.Errorf("%d, body read %d times, Connection %q, %s %s", rec.Code, b.reads, rec.Header().Get("Connection"), code, typ)
			}
		})
	}
	r.idle(1 << 30)
}

// TestSlowBodyOfAnUnknownKeyIsNeverWaitedFor: over a real connection, a client that announces a body and never sends it
// is told 401 at once, because the key is judged by the headers alone (the slow-body attack the order is for).
func TestSlowBodyOfAnUnknownKeyIsNeverWaitedFor(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { quietly(pw) })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.server.URL+"/v1/messages", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Host, req.ContentLength = apiHost, 1<<20
	req.Header.Set("Authorization", "Bearer sk-ax-v1-"+strings.Repeat("A", 49))
	done := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			close(done)
			return
		}
		resp.Body.Close() //nolint:errcheck,gosec // nothing was sent to read
		done <- resp.StatusCode
	}()
	select {
	case status, ok := <-done:
		if ok && status != http.StatusUnauthorized {
			t.Errorf("status %d", status)
		}
	case <-time.After(time.Minute): // the pipe is never written, so only a handler that waits for it takes longer
		t.Fatal("the 401 did not arrive while the body was still being awaited")
	}
}

// TestWaysToSendTheKey: the key may come as a Bearer token in any case or as x-api-key; a Bearer token wins, and an
// Authorization of another scheme does not hide x-api-key. A recognised request's body is read.
func TestWaysToSendTheKey(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	body := messageBody("acme/beta")
	for name, headers := range map[string][][2]string{
		"x-api-key":                      {{"X-Api-Key", a.key}},
		"Bearer, in any case":            {{"Authorization", "bearer " + a.key}},
		"Bearer over a wrong x-api-key":  {{"Authorization", "Bearer " + a.key}, {"X-Api-Key", "sk-ax-v1-nope"}},
		"x-api-key after another scheme": {{"Authorization", "Basic abc"}, {"X-Api-Key", a.key}},
	} {
		t.Run(name, func(t *testing.T) {
			b := &countingBody{r: strings.NewReader(body)}
			req := post(t, apiHost, "/v1/messages", "", b)
			req.ContentLength = int64(len(body))
			for _, h := range headers {
				req.Header.Set(h[0], h[1])
			}
			if rec := r.serve(req); rec.Code != http.StatusOK || b.reads == 0 {
				t.Errorf("%d %s, body read %d times", rec.Code, rec.Body, b.reads)
			}
		})
	}
	r.idle(1 << 30)
}

// TestSlots: an account's slots, then the node's, are taken before the body is read, and are given back when a call
// ends or is refused. A tenth of the node's slots is an account's by default.
func TestSlots(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.NodeInFlight = 10 } })
	release := r.holdCalls()
	accounts := make([]account, 10)
	for i := range accounts {
		accounts[i] = r.newAccount(1e9)
	}
	requests := make([]func() (reply, string), 10)
	for i := range requests {
		requests[i] = func() (reply, string) { return r.message(accounts[i], "acme/beta") }
	}
	together(func() {
		r.asked(10)
		if resp, body := r.message(accounts[0], "acme/beta"); resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("a second call of an account with one slot: %d %s", resp.StatusCode, body)
		}
		resp, body := r.message(r.newAccount(1e9), "acme/beta")
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusServiceUnavailable || code != "overloaded" || resp.Header.Get("Retry-After") != "1" {
			t.Errorf("a call when the node's slots are taken: %d %s", resp.StatusCode, body)
		}
		release()
	}, requests...)
	r.idle(1 << 30)
	if resp, body := r.message(accounts[0], "acme/beta"); resp.StatusCode != http.StatusOK {
		t.Errorf("after the calls ended: %d %s", resp.StatusCode, body)
	}
	wantRefused(t, r.refused(), map[string]int64{"account_in_flight": 1})
}

// TestIPLimits: a client address may have only so many requests in flight, however it spells itself with proxy
// headers; another address is not affected.
func TestIPLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.IPInFlight = 2 } })
	a, b, c := r.newAccount(1e9), r.newAccount(1e9), r.newAccount(1e9) // one account each: one account's gate would turn one away
	release := r.holdCalls()
	together(func() {
		r.asked(2)
		resp, body := r.do(http.MethodPost, "/v1/messages", b.key, strings.NewReader(messageBody("acme/beta")), "X-Forwarded-For", "203.0.113.9")
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusTooManyRequests || code != "rate_limited" {
			t.Errorf("a third request from the address: %d %s", resp.StatusCode, body)
		}
		other := post(t, apiHost, "/v1/messages", b.key, strings.NewReader(messageBody("acme/beta")))
		other.RemoteAddr = "198.51.100.4:5000"
		done := make(chan *httptest.ResponseRecorder)
		go func() { done <- r.serve(other) }()
		r.asked(3) // admitted while the first address is at its limit
		release()
		if rec := <-done; rec.Code != http.StatusOK {
			t.Errorf("another address: %d %s", rec.Code, rec.Body)
		}
	}, func() (reply, string) { return r.message(a, "acme/beta") }, func() (reply, string) { return r.message(c, "acme/beta") })
	r.idle(1 << 30)
	wantRefused(t, r.refused(), map[string]int64{"ip_in_flight": 1})
}

// TestIPLimitsGroupIPv6By64: the in-flight limit counts an IPv6 customer's whole /64, so changing the address inside
// it buys no slot; another /64 is another address. The group is clientip.Group's, the one the login attempts count too.
func TestIPLimitsGroupIPv6By64(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.IPInFlight = 2 } })
	a, b := r.newAccount(1e9), r.newAccount(1e9) // one account each: one account's gate would turn one away
	group := clientip.Group(netip.MustParseAddr("2001:db8:1:2::1")).String()
	first, err := r.limits.IPCalls.Acquire(group, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := r.limits.IPCalls.Acquire(group, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	request := func(key, remote string) *http.Request {
		req := post(t, apiHost, "/v1/messages", key, strings.NewReader(messageBody("acme/beta")))
		req.RemoteAddr = remote
		return req
	}
	if rec := r.serve(request(b.key, "[2001:db8:1:2::3]:4000")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("another address of the same /64: %d %s", rec.Code, rec.Body)
	}
	if rec := r.serve(request(a.key, "[2001:db8:1:3::1]:4000")); rec.Code != http.StatusOK {
		t.Errorf("another /64: %d %s", rec.Code, rec.Body)
	}
	first()
	second()
	r.idle(1 << 30)
	wantRefused(t, r.refused(), map[string]int64{"ip_in_flight": 1})
}

// quietly closes what a test is done with; there is nobody to tell about a failure.
func quietly(c io.Closer) { c.Close() } //nolint:errcheck,gosec // the test is over with it

// TestBodyLimits: a body over the limit is 413 by its announced length or as it comes, one that is cut short is 400
// and one too slow 408, a body the node has no memory for is 503, and every byte charged is given back.
func TestBodyLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) { c.MaxBody = 1 << 10 }
		o.memory = 4 << 10
	})
	a := r.newAccount(1e9)
	big := strings.Repeat("x", 2<<10)
	small := messageBody("acme/beta")
	for name, tc := range map[string]struct {
		body   io.Reader
		length int64
		status int
		code   string
	}{
		"announced over the limit": {strings.NewReader(big), int64(len(big)), http.StatusRequestEntityTooLarge, "payload_too_large"},
		"unannounced and over":     {io.NopCloser(strings.NewReader(big)), -1, http.StatusRequestEntityTooLarge, "payload_too_large"},
		"too slow":                 {&countingBody{r: iotestErr{os.ErrDeadlineExceeded}}, -1, http.StatusRequestTimeout, "timeout"},
		"cut short": {
			&countingBody{r: io.MultiReader(strings.NewReader(small[:10]), iotestErr{io.ErrUnexpectedEOF})}, int64(len(small)),
			http.StatusBadRequest, "invalid_request",
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := post(t, apiHost, "/v1/messages", a.key, tc.body)
			req.ContentLength = tc.length
			rec := r.serve(req)
			if code, _ := errorOf(t, rec.Body.String()); rec.Code != tc.status || code != tc.code {
				t.Errorf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	r.idle(4 << 10)
	r.memory.Charge(4<<10 - 10) // what is left is smaller than a body that fits the limit
	rec := r.serve(post(t, apiHost, "/v1/messages", a.key, strings.NewReader(small)))
	if code, _ := errorOf(t, rec.Body.String()); rec.Code != http.StatusServiceUnavailable || code != "overloaded" {
		t.Errorf("a body the node's memory cannot hold: %d %s", rec.Code, rec.Body)
	}
}

// TestRequestsThatAreNotServed: what M1 does not take, and what is not a request, is 400 and reserves nothing.
func TestRequestsThatAreNotServed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for name, tc := range map[string]struct {
		path, body string
		header     [][2]string
		named      string
	}{
		"a continued response": {"/v1/responses", `{"model":"acme/beta","input":"x","previous_response_id":"resp_1"}`, nil, "previous_response_id"},
		"a protocol upgrade":   {"/v1/messages", messageBody("acme/beta"), [][2]string{{"Upgrade", "websocket"}}, "upgrade"},
		"not JSON":             {"/v1/chat/completions", `{"model":`, nil, ""},
		"no model":             {"/v1/messages", `{"max_tokens":1}`, nil, "model"},
		"too many alternates":  {"/v1/chat/completions", `{"model":"acme/beta","models":["1","2","3","4","5","6","7","8","9"]}`, nil, "models"},
		"a file reference without a pin": {"/v1/responses", `{"model":"acme/beta","input":[{"type":"message","role":"user",` +
			`"content":[{"type":"input_file","file_id":"file_1"}]}]}`, nil, "file_id"},
	} {
		t.Run(name, func(t *testing.T) {
			req := post(t, apiHost, tc.path, a.key, strings.NewReader(tc.body))
			for _, h := range tc.header {
				req.Header.Set(h[0], h[1])
			}
			rec := r.serve(req)
			if code, _ := errorOf(t, rec.Body.String()); rec.Code != http.StatusBadRequest || code != "invalid_request" ||
				!strings.Contains(strings.ToLower(rec.Body.String()), tc.named) {
				t.Errorf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d reservation rows, %d calls", r.count(allRows), len(r.calls()))
	}
	// A client that tries the WebSocket handshake of Codex on a call path is refused, and a plain GET is not allowed.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+apiHost+"/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	if rec := r.serve(req); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Errorf("the handshake: %d %s", rec.Code, rec.Body)
	}
	plain := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+apiHost+"/v1/responses", nil)
	if rec := r.serve(plain); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("a GET: %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	r.idle(1 << 30)
}

// TestPathsAndFormats: each call path answers under both prefixes, and the call knows the format of its path.
func TestPathsAndFormats(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	cases := []struct {
		path, body string
		format     protocol.Format
	}{
		{"/chat/completions", `{"model":"acme/beta","max_tokens":10,"service_tier":"default","messages":[{"role":"user","content":"hi"}]}`, protocol.Chat},
		{"/responses", `{"model":"acme/beta","max_output_tokens":10,"service_tier":"default","input":"hi"}`, protocol.Responses},
		{"/messages", messageBody("acme/beta"), protocol.Messages},
	}
	for _, prefix := range []string{"/v1", "/api/v1"} {
		for _, c := range cases {
			resp, body := r.do(http.MethodPost, prefix+c.path, a.key, strings.NewReader(c.body))
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s%s: %d %s", prefix, c.path, resp.StatusCode, body)
				continue
			}
			calls := r.calls()
			if got := calls[len(calls)-1].Format; got != c.format {
				t.Errorf("%s%s: format %v, want %v", prefix, c.path, got, c.format)
			}
		}
	}
}

// TestTheCompactPath: the Responses compact endpoint is a call path of its own under both prefixes, and the call knows
// it is passed through; a stream asked for there is refused before the route is planned.
func TestTheCompactPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, prefix := range []string{"/v1", "/api/v1"} {
		resp, body := r.do(http.MethodPost, prefix+"/responses/compact", a.key,
			strings.NewReader(`{"model":"acme/beta","service_tier":"default","input":"hi"}`))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", prefix, resp.StatusCode, body)
		}
		calls := r.calls()
		if c := calls[len(calls)-1]; !c.Compact || c.Format != protocol.Responses {
			t.Errorf("%s: compact %t, format %v", prefix, c.Compact, c.Format)
		}
	}
	resp, body := r.do(http.MethodPost, "/v1/responses/compact", a.key,
		strings.NewReader(`{"model":"acme/beta","service_tier":"default","input":"hi","stream":true}`))
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Errorf("a stream asked for there: %d %s", resp.StatusCode, body)
	}
}

// TestAlternateModels: the models a request lists follow its own in the route, and one that does not exist is left
// out.
func TestAlternateModels(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	body := `{"model":"acme/alpha","models":["acme/none","acme/beta"],"max_tokens":` + itoa(maxOutput) + `,"service_tier":"standard_only","messages":[]}`
	resp, out := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	var got []string
	for _, e := range r.calls()[0].Plan.Entries {
		got = append(got, e.Model.String())
	}
	want := []string{r.alpha.String(), r.alpha.String(), r.beta.String()}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("models of the route (-want +got):\n%s", diff)
	}
}

// TestForwardedHeaders: the client's headers that a protocol may forward are kept as it wrote them, to be derived per
// attempt (headers.go); its credentials and the headers that name its tooling are dropped.
func TestForwardedHeaders(t *testing.T) {
	t.Parallel()
	in := http.Header{
		"Authorization": {"Bearer secret"}, "X-Api-Key": {"secret"}, "X-Axiom-Preset": {"x"}, "X-Claude-Code-Session-Id": {"s"},
		"Http-Referer": {"https://app.example"}, "X-Title": {"app"}, "Cookie": {"a=b"}, "Content-Type": {"application/json"},
		"Anthropic-Beta":    {"context-1m-2026-01-01,something-new-2027-02-02", "another"},
		"Anthropic-Version": {"2023-06-01"},
		"session_id":        {"s"}, "conversation_id": {"c"}, "x-codex-turn-state": {"t"},
	}
	want := http.Header{
		"Anthropic-Beta":     {"context-1m-2026-01-01,something-new-2027-02-02", "another"},
		"Anthropic-Version":  {"2023-06-01"},
		"Session_id":         {"s"},
		"Conversation_id":    {"c"},
		"X-Codex-Turn-State": {"t"},
	}
	if diff := cmp.Diff(want, forwardable(in)); diff != "" {
		t.Errorf("forwarded headers (-want +got):\n%s", diff)
	}
}

// listedModel is a model of the list with the members of OpenAI's and of Anthropic's.
type listedModel struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Type        string `json:"type"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	Created     *int64 `json:"created"`
}

func (m listedModel) complete() bool {
	return m.Object == "model" && m.Type == "model" && m.OwnedBy != "" && m.DisplayName != "" && m.CreatedAt != "" && m.Created != nil
}

// listIDs reads GET /v1/models and returns the IDs, checking every model carries the members OpenAI's and Anthropic's
// clients read.
func (r *rig) listIDs(a account) []string {
	r.t.Helper()
	resp, body := r.do(http.MethodGet, "/v1/models", a.key, nil)
	var list struct {
		Object  string        `json:"object"`
		HasMore bool          `json:"has_more"`
		FirstID string        `json:"first_id"`
		Data    []listedModel `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &list) != nil || list.Object != "list" || len(list.Data) == 0 {
		r.t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var ids []string
	for _, m := range list.Data {
		if !m.complete() {
			r.t.Errorf("model %+v lacks a member OpenAI or Anthropic clients read", m)
		}
		ids = append(ids, m.ID)
	}
	if list.FirstID != ids[0] || list.HasMore {
		r.t.Errorf("first_id %q, has_more %v", list.FirstID, list.HasMore)
	}
	return ids
}

// TestModelList: the list names every model that has an endpoint by the name the catalog gives it and by its maker's own
// where that resolves, in the members of OpenAI's and of Anthropic's, and follows the catalog. Under /api/v1 the same
// models are OpenRouter's objects, one entry each under the catalog's name.
func TestModelList(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	if diff := cmp.Diff([]string{"acme/alpha", "acme/beta", "anthropic/claude-live", "claude-live"}, r.listIDs(a)); diff != "" {
		t.Errorf("/v1/models (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"acme/alpha", "acme/beta", "anthropic/claude-live"}, r.orIDs(a)); diff != "" {
		t.Errorf("/api/v1/models (-want +got):\n%s", diff)
	}
	gamma := r.model("acme/gamma")
	if err := r.catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.attachEndpoint(gamma)
	if got := r.listIDs(a); len(got) != 5 {
		t.Errorf("after a model was listed and given an endpoint: %v", got)
	}
	if got := r.orIDs(a); len(got) != 4 {
		t.Errorf("after a model was listed and given an endpoint: %v", got)
	}
	resp, body := r.do(http.MethodGet, "/v1/models", "", nil, "Anthropic-Version", "2023-06-01")
	if code, typ := errorOf(t, body); resp.StatusCode != http.StatusUnauthorized || code != "authentication" || typ != "authentication_error" {
		t.Errorf("without a key: %d %s", resp.StatusCode, body)
	}
}
