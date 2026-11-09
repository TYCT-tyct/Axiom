package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// recordingRig is a ledger rig whose executor writes the call log, and the upstreams it calls.
func recordingRig(t *testing.T) (*rig, *fakeUpstreams) {
	t.Helper()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	records, err := observability.NewRecorder(t.Context(), r.db, platformScope, observability.Config{
		Dir: t.TempDir(), Capacity: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r.exec.Records = records
	return r, up
}

// flush writes every record the rig's recorder holds.
func (r *rig) flush() {
	r.t.Helper()
	if err := r.exec.Records.Close(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
}

// loggedAttempt is an attempt row as the test reads it back.
type loggedAttempt struct {
	errorKind  string
	firstToken int64
}

// firstTokenWait is how long the recording tests' upstreams wait before their first content: the first token stored is
// at least this.
const firstTokenWait = 20 * time.Millisecond

// failsUnknown is a supplier failure of a cause no rule recognises, which the next entry is tried after.
func failsUnknown(context.Context) (*http.Response, *suppliers.Failure) {
	return nil, failing(routing.ScopeEndpoint, true, suppliers.Failure{Cause: suppliers.Unknown{Status: 520}})
}

// answersLate streams an answer whose first content comes after firstTokenWait, ending normally or broken off.
func answersLate(sample []string, broken bool) scripted {
	beats := []beat{{data: events(sample, evStart)}, {data: events(sample, evBlock)}, {wait: firstTokenWait, data: text(10)}}
	if broken {
		return serve(append(beats, beat{hangup: true})...)
	}
	return serve(append(beats, beat{data: events(sample, evBlockStop, evMsgDelta, evMsgStop)})...)
}

// TestCallRecordKeepsErrorKindsAndFirstTokens: the call and each attempt are stored with why they failed and when their
// first content came, so the log's error filter and its first-token figures reflect real calls (#729 G2).
func TestCallRecordKeepsErrorKindsAndFirstTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		script    func(sample []string, n int64) scripted // n is the attempt's number across the endpoints
		callError string
		attempts  []loggedAttempt // firstToken is 1 where the attempt's first token must be at least firstTokenWait
	}{
		{"a fallback answers", func(s []string, n int64) scripted {
			if n == 1 {
				return failsUnknown
			}
			return answersLate(s, false)
		}, "", []loggedAttempt{{"unknown", 0}, {"", 1}}},
		{
			"the stream breaks after content", func(s []string, _ int64) scripted { return answersLate(s, true) },
			"upstream_error",
			[]loggedAttempt{{"upstream_error", 1}},
		},
		{
			"every attempt fails", func([]string, int64) scripted { return failsUnknown },
			"upstream_error",
			[]loggedAttempt{{"unknown", 0}, {"unknown", 0}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, up := recordingRig(t)
			a := r.funded(1e12)
			sample, _ := sample(t, protocol.Messages)
			var made atomic.Int64
			for _, e := range up.eps {
				e.script = []scripted{func(ctx context.Context) (*http.Response, *suppliers.Failure) {
					return tc.script(sample, made.Add(1))(ctx)
				}}
			}
			r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))
			r.flush()
			call, attempts := r.loggedCall()
			checkLogged(t, call, attempts, tc.callError, tc.attempts)
		})
	}
}

// checkLogged compares the stored call and attempts with the error kinds wanted and whether each first token came.
func checkLogged(t *testing.T, call loggedCall, attempts []loggedAttempt, callError string, want []loggedAttempt) {
	t.Helper()
	if call.errorKind != callError {
		t.Errorf("the call's error kind is %q, want %q", call.errorKind, callError)
	}
	answered := want[len(want)-1].firstToken != 0
	if !firstTokenAsWanted(call.firstToken, answered) || call.firstToken > call.latency {
		t.Errorf("the call's first token is %d ms of %d ms; content came %v after %s", call.firstToken, call.latency, answered, firstTokenWait)
	}
	if len(attempts) != len(want) {
		t.Fatalf("attempts %+v, want %+v", attempts, want)
	}
	for i, w := range want {
		if attempts[i].errorKind != w.errorKind || !firstTokenAsWanted(attempts[i].firstToken, w.firstToken != 0) {
			t.Errorf("attempt %d is %+v, want %+v (1: at least %s)", i+1, attempts[i], w, firstTokenWait)
		}
	}
}

// firstTokenAsWanted reports whether a stored first token, in milliseconds, is at least firstTokenWait when content
// came after it, and 0 when none came.
func firstTokenAsWanted(ms int64, came bool) bool {
	if !came {
		return ms == 0
	}
	return ms >= firstTokenWait.Milliseconds()
}

// loggedCall is the one call the rig's log holds and its attempts in order.
type loggedCall struct {
	errorKind           string
	firstToken, latency int64
}

func (r *rig) loggedCall() (loggedCall, []loggedAttempt) {
	r.t.Helper()
	var call loggedCall
	var attempts []loggedAttempt
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		if err := tx.QueryRow(r.t.Context(), `SELECT error_kind, first_token_ms, latency_ms FROM observability.call_log`).
			Scan(&call.errorKind, &call.firstToken, &call.latency); err != nil {
			return err
		}
		rows, err := tx.Query(r.t.Context(), `SELECT error_kind, first_token_ms FROM observability.call_attempt ORDER BY attempt_seq`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var at loggedAttempt
			if err := rows.Scan(&at.errorKind, &at.firstToken); err != nil {
				return err
			}
			attempts = append(attempts, at)
		}
		return rows.Err()
	}); err != nil {
		r.t.Fatal(err)
	}
	return call, attempts
}

// TestCallRecordNamesTheRequester: the end user is stored as the account's keyed hash, never as sent; the session is
// the one the client named; the class is the kind of call; and the agreement versions are the ones the account's owner
// had accepted, while training stays not allowed (#729 G3, G4).
func TestCallRecordNamesTheRequester(t *testing.T) {
	t.Parallel()
	r, up := recordingRig(t)
	a := r.funded(1e12)
	owner := uuid.NewV7()
	r.sql(
		"INSERT INTO accounts.users (id, site_id, account_id, email, password_hash, factor_version, status, invite_code) VALUES ('"+
			owner.String()+"', '"+siteID.String()+"', '"+a.id.String()+"', 'owner@example.com', '$argon2id$test', 1, 'active', 'owner01')",
		"INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body) VALUES ('"+siteID.String()+"', 'terms', 'zh', 1, 'T'), ('"+
			siteID.String()+"', 'terms', 'en', 2, 'T2'), ('"+siteID.String()+"', 'privacy', 'en', 1, 'P')",
		"INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, locale, version) VALUES (gen_random_uuid(), '"+
			siteID.String()+"', '"+a.id.String()+"', '"+owner.String()+"', 'terms', 'zh', 1), (gen_random_uuid(), '"+
			siteID.String()+"', '"+a.id.String()+"', '"+owner.String()+"', 'privacy', 'en', 1)",
	)
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 1, 10, false)}
	}
	body := strings.Replace(stream("acme/beta"), `{"stream":true,`, `{"stream":true,"metadata":{"user_id":"end-user-1"},`, 1)
	if resp, out := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(body), "X-Session-Id", "session-1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, out)
	}
	r.flush()
	var endUser, session, class, consent string
	var training bool
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT end_user_hash, session_id, request_class, consent_version, training_allowed
			FROM observability.call_log`).Scan(&endUser, &session, &class, &consent, &training)
	}); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, r.exec.endUserKey)
	mac.Write(a.id[:])
	mac.Write([]byte("end-user-1"))
	if want := hex.EncodeToString(mac.Sum(nil)); endUser != want {
		t.Errorf("end user %q, want the account's keyed hash %q", endUser, want)
	}
	if session != "session-1" || class != "chat" || consent != "terms:zh:1,privacy:en:1" || training {
		t.Errorf("session %q, class %q, consent %q, training %v; want session-1, chat, terms:zh:1,privacy:en:1, false",
			session, class, consent, training)
	}
}

// TestRecordedRequestFields covers how the record names what the request said: the class by the endpoint, the session
// by the body ahead of the headers, and the accepted documents.
func TestRecordedRequestFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		call *Call
		want string
	}{
		{&Call{Request: protocol.Request{Format: protocol.Messages}}, "chat"},
		{&Call{Request: protocol.Request{Format: protocol.Responses}, Compact: true}, "compact"},
		{&Call{Request: protocol.Request{Format: protocol.Embeddings}}, "embeddings"},
		{&Call{Request: protocol.Request{Format: protocol.Rerank}}, "rerank"},
		{&Call{Request: protocol.Request{Format: protocol.Completions}}, "completions"},
	} {
		if got := classOf(tc.call); got != tc.want {
			t.Errorf("class of %v (compact %v) is %q, want %q", tc.call.Request.Format, tc.call.Compact, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name    string
		body    string
		headers map[string]string
		want    string
	}{
		{"the body wins", "body-1", map[string]string{"X-Session-Id": "header-1"}, "body-1"},
		{"OpenRouter's header", "", map[string]string{"X-Session-Id": "header-1", "Session_id": "codex-1"}, "header-1"},
		{"Claude Code's header", "", map[string]string{"X-Claude-Code-Session-Id": "claude-1", "Session_id": "codex-1"}, "claude-1"},
		{"Codex's header", "", map[string]string{"session_id": "codex-1"}, "codex-1"},
		{"a blank header is passed over", "", map[string]string{"X-Session-Id": "  ", "Session_id": "codex-1"}, "codex-1"},
		{"none", "", nil, ""},
		{"cut at the column's width", strings.Repeat("界", 200), nil, strings.Repeat("界", maxSessionID)},
	} {
		h := http.Header{}
		for name, value := range tc.headers {
			h[http.CanonicalHeaderKey(name)] = []string{value}
		}
		if got := namedSession(&Call{Request: protocol.Request{SessionID: tc.body}}, h); got != tc.want {
			t.Errorf("%s: session %q, want %q", tc.name, got, tc.want)
		}
	}
	terms := accounts.DocumentVersion{Kind: accounts.LegalTerms, Locale: accounts.LegalLocaleEN, Version: 3}
	privacy := accounts.DocumentVersion{Kind: accounts.LegalPrivacy, Locale: accounts.LegalLocaleZH, Version: 2}
	for c, want := range map[accounts.Consent]string{
		{}:                               "none",
		{Terms: terms}:                   "terms:en:3",
		{Privacy: privacy}:               "privacy:zh:2",
		{Terms: terms, Privacy: privacy}: "terms:en:3,privacy:zh:2",
	} {
		if got := consentOf(c); got != want {
			t.Errorf("consent %+v is %q, want %q", c, got, want)
		}
	}
}

// TestFirstTokenOfTheAnsweringAttempt: the call's first token is the answering attempt's, timed from the call's start;
// a failed attempt's content does not count, and a call no attempt answered has none.
func TestFirstTokenOfTheAnsweringAttempt(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	tries := []try{
		{started: start, firstToken: start.Add(50 * time.Millisecond), errorKind: "upstream_error"},
		{started: start.Add(time.Second), firstToken: start.Add(1300 * time.Millisecond)},
	}
	for _, tc := range []struct {
		name    string
		results []billing.Result
		want    time.Duration
	}{
		{"the second answers", []billing.Result{billing.FailedBeforeCommit, billing.Committed}, 1300 * time.Millisecond},
		{"an aggregated reply", []billing.Result{billing.FailedBeforeCommit, billing.Aggregated}, 1300 * time.Millisecond},
		{"none answers", []billing.Result{billing.FailedBeforeCommit, billing.FailedBeforeCommit}, 0},
	} {
		o := billing.Outcome{}
		for i, res := range tc.results {
			o.Attempts = append(o.Attempts, billing.AttemptResult{Seq: i + 1, Result: res})
		}
		if got := firstTokenOf(trail{tries: tries, start: start}, o); got != tc.want {
			t.Errorf("%s: first token %s, want %s", tc.name, got, tc.want)
		}
	}
	attempts := attemptsOf(billing.Outcome{Attempts: []billing.AttemptResult{{Seq: 1}, {Seq: 2}}}, tries)
	if attempts[0].FirstToken != 50*time.Millisecond || attempts[0].ErrorKind != "upstream_error" ||
		attempts[1].FirstToken != 300*time.Millisecond || attempts[1].ErrorKind != "" {
		t.Errorf("attempts %+v", attempts)
	}
}
