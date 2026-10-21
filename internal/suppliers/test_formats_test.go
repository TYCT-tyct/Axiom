package suppliers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

func TestEndpointGenerationFormats(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		format    protocol.Format
		name, cap string
	}{
		{protocol.Messages, "messages", "max_tokens"},
		{protocol.Responses, "responses", "max_output_tokens"},
		{protocol.Chat, "chat", "max_tokens"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			stream, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", c.name, "text", "upstream.sse"))
			if err != nil {
				t.Fatal(err)
			}
			w := newWorld(t, 1)
			bodies := make(chan string, 1)
			up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				bodies <- string(b)
				rw.Header().Set("Content-Type", "text/event-stream")
				_, _ = rw.Write(stream) //nolint:errcheck // a test server
			}))
			t.Cleanup(up.Close)
			w.pointAt(up.URL)
			w.svc.snap.Load().endpoints[w.endpoint].formats = []protocol.Format{c.format}
			r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: c.format})
			up.Close()
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome() != TestPassed || !r.Streamed || r.FirstToken == nil || len(r.Usage) == 0 {
				t.Fatalf("test result: %+v", r)
			}
			body := <-bodies
			if !strings.Contains(body, `"`+c.cap+`":16`) || !strings.Contains(body, `"stream":true`) {
				t.Errorf("uncapped or unstreamed request: %s", body)
			}
		})
	}
}

func TestEndpointRefusalsKeepTheirStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, 1)
			up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(status) }))
			t.Cleanup(up.Close)
			w.pointAt(up.URL)
			r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != status || r.Outcome() != TestInconclusive || r.Failure == nil {
				t.Errorf("status %d: %+v", status, r)
			}
		})
	}
}

func TestEndpointCostAndAuditRollBackTogether(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt(newTestUpstream(t, answersWhole).URL)
	r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("roll back the test result")
	err = w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := RecordTest(t.Context(), tx, audit.User(uuid.NewV7()), "r", r); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if w.count(`SELECT count(*) FROM suppliers.attempt_costs`) != 0 || w.count(`SELECT count(*) FROM platform.audit_log WHERE action = 'endpoint.test'`) != 0 {
		t.Fatal("the rolled-back test left a cost or audit row")
	}
}

func TestEndpointOwnLimitsAreInconclusive(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{"timeout", io.ErrUnexpectedEOF, true},
		{"memory", protocol.ErrMemory, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if c.cancel {
				cancel()
			}
			r := TestResult{}
			r.endOf(ctx, c.err)
			cause, scope, ok := r.Problem()
			if r.Outcome() != TestInconclusive || cause != c.name || scope != "" || !ok {
				t.Errorf("own limit judged the endpoint: %+v", r)
			}
		})
	}
}

func TestEndpointWithoutAPriceDoesNotGenerate(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	up := newTestUpstream(t, answersWhole)
	w.pointAt(up.URL)
	w.svc.schedules.Store(&map[uuid.UUID]*catalog.Schedule{})
	_, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if !errors.Is(err, ErrTestCost) || up.sent() != "" {
		t.Errorf("unpriced test: error %v, upstream body %s", err, up.sent())
	}
}

func TestEndpointEmptyAnswerFails(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(rw, chatEvent(`"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}`)+"data: [DONE]\n\n") //nolint:errcheck // a test server
	}))
	t.Cleanup(up.Close)
	w.pointAt(up.URL)
	r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome() != TestFailed || r.FirstToken != nil {
		t.Errorf("an empty answer passed: %+v", r)
	}
}
