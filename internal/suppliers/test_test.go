package suppliers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// What the made-up supplier does with a test's call: answer the whole stream, refuse it, break the stream, or hold it
// open so that the caller can go away while it is read.
const (
	answersWhole = iota
	answersFailed
	answersBroken
	answersHeld
)

// testUpstream is a supplier that answers a test's call in Chat's streaming form and gives its own request id.
type testUpstream struct {
	*httptest.Server
	mode int
	// started is closed once the first event is written; goOn lets the held answer go on.
	started, goOn chan struct{}
	finished      chan struct{}

	mu    sync.Mutex
	body  string
	wrote error // what writing the answer's last event returned
	ended error // the request's context error when the last event was written
}

func newTestUpstream(t *testing.T, mode int) *testUpstream {
	t.Helper()
	u := &testUpstream{mode: mode, started: make(chan struct{}), goOn: make(chan struct{}), finished: make(chan struct{})}
	u.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer close(u.finished)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server
		u.mu.Lock()
		u.body = string(body)
		u.mu.Unlock()
		if u.mode == answersFailed {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("X-Ref", "ref-1")
		rw.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(rw, chatEvent(`"choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]`)) //nolint:errcheck // a test server
		flush(rw)
		if u.mode == answersBroken {
			if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close() //nolint:errcheck // the point
			}
			return
		}
		close(u.started)
		if u.mode == answersHeld {
			<-u.goOn
		}
		_, wrote := io.WriteString(rw, chatEvent(`"choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2}`)+"data: [DONE]\n\n")
		flush(rw)
		u.mu.Lock()
		u.wrote, u.ended = wrote, r.Context().Err()
		u.mu.Unlock()
	}))
	t.Cleanup(u.Close)
	return u
}

// flush sends what has been written so far, which a server that keeps writing must do for a reader to see it.
func flush(rw http.ResponseWriter) {
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
}

// chatEvent is one Chat chunk with the members every chunk has; members is what makes this one different.
func chatEvent(members string) string {
	return `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt",` + members + "}\n\n"
}

// sent is the call the test made, as the supplier read it.
func (u *testUpstream) sent() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.body
}

// TestATestSendsACappedStreamAndBooksWhatItFound: a test goes through the path a call takes, in the format asked for,
// streamed, with the output capped and no reasoning asked for; it returns the latency, the supplier's own request id and
// a cost row marked as a test and booked at a definite upper bound (docs/domains/quality.md「试探」).
func TestATestSendsACappedStreamAndBooksWhatItFound(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	up := newTestUpstream(t, answersWhole)
	w.pointAt(up.URL)
	result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	checkTestGeneration(t, &result)
	checkTestRequest(t, up.sent())
	checkTestCostRow(w, result)
	checkTestAudit(w)
}

func checkTestGeneration(t *testing.T, result *TestResult) {
	t.Helper()
	if got := result.Outcome(); got != TestPassed {
		t.Errorf("outcome %v, want passed; failure %+v", got, result.Failure)
	}
	if !result.Streamed || result.FirstToken == nil || *result.FirstToken > result.Total || result.Total <= 0 {
		t.Errorf("streamed %v, first token %v of %v; want a stream read to its end", result.Streamed, result.FirstToken, result.Total)
	}
	if result.UpstreamRequest != "ref-1" {
		t.Errorf("the supplier's request id is %q, want ref-1", result.UpstreamRequest)
	}
	if cause, scope, ok := result.Problem(); ok {
		t.Errorf("a passing test has the problem %s/%s", cause, scope)
	}
	// 18 prompt tokens and the 16-token cap, both at their highest rate: 18×0.075 + 16×0.3.
	if got := result.Estimate(); got != "6.15" {
		t.Errorf("the estimate is %s, want 6.15", got)
	}
}

func checkTestRequest(t *testing.T, body string) {
	t.Helper()
	// What was asked for: the cap in the format's own member, a stream, and nothing that asks for reasoning.
	for _, want := range []string{`"max_tokens":16`, `"stream":true`, `"model":"gpt"`, `"content":"hi"`, `"include_usage":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("the call's body %s lacks %s", body, want)
		}
	}
	for _, unwanted := range []string{"reasoning", "max_completion_tokens"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the call's body %s asks for %s", body, unwanted)
		}
	}
}

func checkTestCostRow(w *world, result TestResult) {
	t := w.t
	t.Helper()

	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "test the endpoint", result)
	})
	var (
		test                    bool
		flag, ref, state, est   string
		site, account, usageRow *string
	)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT test, cost_flag, coalesce(charge_ref, ''), state, estimated_native::text,
			site_id::text, account_id::text, usage::text
			FROM suppliers.attempt_costs WHERE request_id = $1 AND attempt_seq = 1`, uuid.UUID(result.Request)).
			Scan(&test, &flag, &ref, &state, &est, &site, &account, &usageRow)
	})
	if !test || flag != "complete" || state != "pending" || ref != "ref-1" || est != "6.15" || site != nil || account != nil {
		t.Errorf("the row: test %v, flag %s, ref %s, state %s, estimate %s, tenant %v/%v; want a test's own row", test, flag, ref, state, est, site, account)
	}
	// What the supplier reported is stored beside the estimate, not instead of it.
	if usageRow == nil || !strings.Contains(*usageRow, `"items":{"input":3,"output":2}`) {
		t.Errorf("the row's usage is %v, want the supplier's 3 and 2", usageRow)
	}
}

func checkTestAudit(w *world) {
	t := w.t
	t.Helper()
	// The audit entry ties the estimate to the supplier's own key of the charge.
	entry := w.auditDetail("endpoint.test")
	for _, want := range []string{`"outcome": "passed"`, `"estimated_native": "6.15"`, `"upstream_request_id": "ref-1"`, `"format": "chat"`, `"native_unit": "quota"`} {
		if !strings.Contains(entry, want) {
			t.Errorf("the audit entry %s lacks %s", entry, want)
		}
	}
}

// auditDetail is the detail of the newest audit entry of an action.
func (w *world) auditDetail(action string) (detail string) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT detail::text FROM platform.audit_log WHERE action = $1 ORDER BY at DESC LIMIT 1`, action).Scan(&detail)
	})
	return detail
}

// TestATestThatFailsIsBookedAndSaysWhy: an endpoint's own failure is a failed test that still books what it was
// estimated at, because the platform may have been billed for the attempt.
func TestATestThatFailsIsBookedAndSaysWhy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt(newTestUpstream(t, answersFailed).URL)
	result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outcome(); got != TestFailed {
		t.Errorf("outcome %v, want failed", got)
	}
	if cause, scope, ok := result.Problem(); !ok || cause != "upstream_error" || scope != "endpoint" {
		t.Errorf("the problem is %s/%s (%v), want upstream_error at the endpoint", cause, scope, ok)
	}
	if result.Status != http.StatusInternalServerError || result.Streamed {
		t.Errorf("status %d, streamed %v; want 500 and no stream", result.Status, result.Streamed)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "test the endpoint", result)
	})
	if n := w.count(`SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1 AND test AND cost_flag = 'complete'`,
		uuid.UUID(result.Request)); n != 1 {
		t.Error("a failed test wrote no cost row")
	}
	if entry := w.auditDetail("endpoint.test"); !strings.Contains(entry, `"outcome": "failed"`) || !strings.Contains(entry, `"upstream_status": 500`) {
		t.Errorf("the audit entry %s does not say how the test failed", entry)
	}
}

// TestATestBreaksWithTheStream: a stream that stops before its end is the endpoint's failure, not a pass and not an
// inconclusive answer.
func TestATestBreaksWithTheStream(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt(newTestUpstream(t, answersBroken).URL)
	result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outcome(); got != TestFailed || result.Streamed {
		t.Errorf("outcome %v, streamed %v; want a failed stream", got, result.Streamed)
	}
	if cause, _, _ := result.Problem(); cause != "stream_broken" {
		t.Errorf("the cause is %s, want stream_broken", cause)
	}
	if result.FirstToken == nil {
		t.Error("the stream committed before it broke, so the first token was measured")
	}
}

// TestATestOfAFormatTheEndpointDoesNotServeCostsNothing: the endpoint is only tested in the formats it serves, and a
// refusal is known before anything is sent or booked.
func TestATestOfAFormatTheEndpointDoesNotServeCostsNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	up := newTestUpstream(t, answersWhole)
	w.pointAt(up.URL)
	if _, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Messages}); err == nil {
		t.Fatal("a format the endpoint does not serve was tested")
	}
	if body := up.sent(); body != "" {
		t.Errorf("the supplier was called with %s", body)
	}
	if n := w.count(`SELECT count(*) FROM suppliers.attempt_costs`); n != 0 {
		t.Errorf("%d cost rows, want none", n)
	}
	if _, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: uuidOf(9), Format: protocol.Chat}); err == nil {
		t.Error("an endpoint the snapshot does not hold was tested")
	}
}

// TestATestIsReadToItsEndThoughTheCallerGoesAway: the generation is not cancelled with the request that asked for it. A
// supplier that ignores a disconnect bills the whole generation, so a test that stopped at the client's cancellation
// would spend money and record nothing (docs/domains/quality.md「试探」).
func TestATestIsReadToItsEndThoughTheCallerGoesAway(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	up := newTestUpstream(t, answersHeld)
	w.pointAt(up.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type answer struct {
		result TestResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := w.svc.TestEndpoint(ctx, TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
		done <- answer{result, err}
	}()
	<-up.started
	if other, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		if other != nil {
			other.Done()
		}
		t.Errorf("another call acquired the streaming test's only slot: %v", err)
	}
	cancel()
	close(up.goOn)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.result.Streamed || got.result.Outcome() != TestPassed {
		t.Errorf("outcome %v, streamed %v; want the answer read to its end", got.result.Outcome(), got.result.Streamed)
	}
	<-up.finished
	if other, err := w.svc.Acquire(w.endpoint); err != nil {
		t.Errorf("the completed test kept its slot: %v", err)
	} else {
		other.Done()
	}
	up.mu.Lock()
	wrote, ended := up.wrote, up.ended
	up.mu.Unlock()
	if wrote != nil || ended != nil {
		t.Errorf("the supplier's stream ended with %v, its request's context with %v; want it written out in full", wrote, ended)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "test the endpoint", got.result)
	})
	if n := w.count(`SELECT count(*) FROM suppliers.attempt_costs WHERE test AND usage->'items'->>'output' = '2'`); n != 1 {
		t.Error("the generation the caller abandoned was not booked")
	}
}

// TestReconciliationMatchesATestButNeverJudgesIt: a test's estimate is a bound of our own choosing, so its charge is
// matched to it and attributed, never held, and never compared with the estimate: nothing a test costs can delist an
// endpoint or be reported as an estimate too low (H-I9).
func TestReconciliationMatchesATestButNeverJudgesIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt(newTestUpstream(t, answersWhole).URL)
	result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "test the endpoint", result)
	})
	// A charge far above the estimate, which for a call would delist the endpoint.
	w.ingest(w.charge("ref-1", "400", w.clock.now()))
	report := w.reconcile(w.clock.now())
	if report.Matched != 1 || len(report.Delisted) != 0 || len(report.Over) != 0 {
		t.Errorf("report %+v, want the charge matched and nothing judged", report)
	}
	if got := w.chargeOf("ref-1"); got.State != "matched" || got.Attribution != "attempt" || !got.Attempt {
		t.Errorf("the charge is %+v, want it attributed to the test", got)
	}
	if n := w.count(`SELECT count(*) FROM suppliers.endpoints WHERE id = $1 AND cost_state = 'known'`, w.endpoint); n != 1 {
		t.Error("a test's charge delisted the endpoint")
	}
	if n := w.count(`SELECT count(*) FROM suppliers.charge_events e JOIN suppliers.charge_records c ON c.id = e.charge_record_id
		WHERE c.charge_ref = 'ref-1' AND e.payload->>'verdict' = 'unjudged'`); n != 1 {
		t.Error("the test's charge was judged")
	}
}

// TestATestIsNotAnAttemptWaitingForItsCharge: reconciliation never reports a test as an attempt whose charge has not
// come, the way it reports a call's, because a test's charge may never be ingested at all (H-I9).
func TestATestIsNotAnAttemptWaitingForItsCharge(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt(newTestUpstream(t, answersWhole).URL)
	result, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "test the endpoint", result)
	})
	w.record(w.attempt(newRequest(), 1, "a-calls-ref"))
	report := w.reconcile(w.clock.now().Add(time.Hour))
	if len(report.Unmatched) != 1 || report.Unmatched[0].Ref != "a-calls-ref" {
		t.Errorf("the attempts waiting for a charge are %+v, want only the call's", report.Unmatched)
	}
}
