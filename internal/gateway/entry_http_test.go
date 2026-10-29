package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestEveryResponseCarriesTheCallNumber: the call number is in X-Generation-Id beside X-Request-ID on every response
// the handler gives, the successful and the refused, the model list and the paths that are not calls included.
func TestEveryResponseCarriesTheCallNumber(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for name, tc := range map[string]struct {
		method, path, key string
		status            int
		headers           []string
	}{
		"a model list":                 {http.MethodGet, "/v1/models", a.key, http.StatusOK, nil},
		"a model list without a key":   {http.MethodGet, "/v1/models", "", http.StatusUnauthorized, nil},
		"a call without a key":         {http.MethodPost, "/v1/messages", "", http.StatusUnauthorized, nil},
		"a call refused":               {http.MethodPost, "/v1/messages", a.key, http.StatusBadRequest, nil},
		"a handshake":                  {http.MethodGet, "/v1/responses", "", http.StatusBadRequest, []string{"Upgrade", "websocket"}},
		"a method the path lacks":      {http.MethodGet, "/v1/messages", "", http.StatusMethodNotAllowed, nil},
		"a path nothing serves":        {http.MethodGet, "/v1/nothing", "", http.StatusNotFound, nil},
		"a call admitted and answered": {http.MethodPost, "/v1/messages", a.key, http.StatusOK, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(`{"max_tokens":1}`) // no model: refused
				if tc.status == http.StatusOK || tc.key == "" {
					body = strings.NewReader(messageBody("acme/beta"))
				}
			}
			resp, out := r.do(tc.method, tc.path, tc.key, body, tc.headers...)
			id := resp.Header.Get("X-Request-ID")
			if resp.StatusCode != tc.status || id == "" || resp.Header.Get("X-Generation-Id") != id {
				t.Errorf("%d (want %d), X-Request-ID %q, X-Generation-Id %q: %s",
					resp.StatusCode, tc.status, id, resp.Header.Get("X-Generation-Id"), out)
			}
		})
	}
}

// readReply reads the response the server writes to conn, the body in full.
func readReply(t *testing.T, conn net.Conn) (rep reply, body string, closes bool) {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header}, string(out), resp.Close
}

// TestSlowBodyIsAnswered408OverARealConnection: net/http cancels the request's context when the read deadline passes in
// the middle of the body, so the answer to a body that was too slow has to be written though the context has ended.
// The unit test with a reader that fails does not cancel anything and cannot tell.
func TestSlowBodyIsAnswered408OverARealConnection(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.body = 200 * time.Millisecond })
	a := r.newAccount(1e9)
	conn, err := new(net.Dialer).DialContext(t.Context(), "tcp", r.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { quietly(conn) })
	if err := conn.SetDeadline(time.Now().Add(time.Minute)); err != nil { // a hang guard: the answer comes when the server's deadline passes
		t.Fatal(err)
	}
	const head = "POST /v1/messages HTTP/1.1\r\nHost: " + apiHost + "\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n"
	if _, err := io.WriteString(conn, head+"Authorization: Bearer "+a.key+"\r\n\r\n{\"model\":"); err != nil {
		t.Fatal(err)
	}
	resp, out, closes := readReply(t, conn)
	if code, typ := errorOf(t, out); resp.StatusCode != http.StatusRequestTimeout || code != "timeout" || typ != "timeout_error" || !closes {
		t.Errorf("%d %s, connection closes: %v", resp.StatusCode, out, closes)
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d reservation rows, %d calls", r.count(allRows), len(r.calls()))
	}
	r.idle(1 << 30)
}

// TestModelListJudgesTheKeyAsACallDoes: a key the gateway already knows is not thereby allowed; the list asks the
// database as an admission does, so a key disabled, expired, deleted, of another kind or limited to other addresses
// since is refused 401, whatever the cache holds.
func TestModelListJudgesTheKeyAsACallDoes(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]string{
		"disabled":                   "SET status = 'disabled'",
		"expired":                    "SET expires_at = now() - interval '1 minute'",
		"deleted":                    "SET deleted_at = now()",
		"a management key":           "SET kind = 'management', expires_at = now() + interval '1 day'",
		"limited to other addresses": "SET ip_allow = '{198.51.100.0/24}'",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.newAccount(1e9) // recognised once: the key is in the cache
			if resp, body := r.do(http.MethodGet, "/v1/models", a.key, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("before the change: %d %s", resp.StatusCode, body)
			}
			r.sql("UPDATE accounts.api_keys " + change)
			for _, path := range []string{"/v1/models", "/api/v1/models", "/api/v1/models/user", "/api/v1/models/count", "/api/v1/models/acme/alpha/endpoints", "/api/v1/providers"} {
				resp, body := r.do(http.MethodGet, path, a.key, nil)
				if code, _ := errorOf(t, body); resp.StatusCode != http.StatusUnauthorized || code != "authentication" {
					t.Errorf("after the change, %s: %d %s", path, resp.StatusCode, body)
				}
			}
		})
	}
}
