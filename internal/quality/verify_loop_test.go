package quality

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// stated is the claim verification tests check answers against: the endpoint says it serves Anthropic's model.
var stated = Claimed{Author: "anthropic", Others: []string{"openai", "google"}}

func verificationWorld(t *testing.T) (*Monitor, *Loop, *probeFixture, *time.Time, uuid.UUID) {
	t.Helper()
	m, l, f, now := fixtureLoop(t, Enforce)
	l.identity = func(catalog.ModelID) (Claimed, bool) { return stated, true }
	id := uuid.NewV7()
	f.endpoints = append(f.endpoints, suppliers.Routable{
		ID: id, Credentials: 1, Model: uuid.NewV7(), Formats: []protocol.Format{protocol.Chat},
	})
	return m, l, f, now, id
}

func verificationAnswer(c challenge, author string) string {
	return c.nonce + "\n" + strconv.Itoa(c.a+c.b) + "\n" + author
}

// collectVerification waits for the check the last Tick started and applies its result.
func collectVerification(t *testing.T, l *Loop, f *probeFixture) {
	t.Helper()
	f.release <- struct{}{}
	end := <-l.verifyDone
	l.active--
	l.finishVerification(t.Context(), end)
}

func verificationOf(m *Monitor, id uuid.UUID) verification {
	m.mu.RLock()
	e := m.endpoints[id]
	m.mu.RUnlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.verify
}

// startVerification sends one check and waits until the fake upstream holds it, so the test controls when the answer
// arrives.
func startVerification(t *testing.T, l *Loop, f *probeFixture, reply string) {
	t.Helper()
	f.result = suppliers.TestResult{Streamed: true, Reply: reply}
	tick(t, l)
	if l.active != 1 {
		t.Fatalf("active %d, want one verification in flight", l.active)
	}
}

func passVerification(t *testing.T, l *Loop, f *probeFixture, c challenge) {
	t.Helper()
	startVerification(t, l, f, verificationAnswer(c, "Anthropic"))
	collectVerification(t, l, f)
}

func contradictVerification(t *testing.T, l *Loop, f *probeFixture, c challenge) {
	t.Helper()
	startVerification(t, l, f, verificationAnswer(c, "OpenAI"))
	collectVerification(t, l, f)
}

// TestVerificationPassesAndFailsClosed: a passed check is what sets Verified, with its time and confidence; one
// contradiction clears it at once, three in a row are a fake, and a later pass restores the endpoint. The check is a
// real generation with the challenge's own prompt and a captured answer.
func TestVerificationPassesAndFailsClosed(t *testing.T) {
	t.Run("a pass sets the evidence", verificationPassSetsVerified)
	t.Run("a contradiction clears it at once", verificationContradictionFailsClosed)
	t.Run("three contradictions are a fake and a pass restores it", verificationFakeThenRestored)
}

func verificationPassSetsVerified(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	startVerification(t, l, f, verificationAnswer(c, "Anthropic"))
	collectVerification(t, l, f)
	got := measured(m, id, *now)
	if !got.Verified {
		t.Fatalf("after a pass: %+v, want verified", got)
	}
	if got.VerifyConfidence != 1 {
		t.Fatalf("the pass's confidence is %v, want 1", got.VerifyConfidence)
	}
	if !got.VerifiedAt.Equal(*now) {
		t.Fatalf("the pass is dated %v, want %v", got.VerifiedAt, *now)
	}
	req := <-f.calls
	if req.Prompt == "" || !strings.Contains(req.Prompt, c.nonce) {
		t.Errorf("the check's prompt %q does not ask for the nonce %q", req.Prompt, c.nonce)
	}
	if req.Capture != verifyReply {
		t.Errorf("the check captures %d bytes, want %d", req.Capture, verifyReply)
	}
	if req.Output != verifyOutput {
		t.Errorf("the check asks for %d tokens, want %d", req.Output, verifyOutput)
	}
}

func verificationContradictionFailsClosed(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	passVerification(t, l, f, c)
	*now = now.Add(verifyEvery)
	contradictVerification(t, l, f, c)
	got := measured(m, id, *now)
	if got.Verified {
		t.Fatalf("after a contradiction: %+v, want unverified", got)
	}
	if got.VerifyConfidence != 0 {
		t.Fatalf("a contradicted pass keeps confidence %v", got.VerifyConfidence)
	}
	v := verificationOf(m, id)
	if v.failures != 1 {
		t.Fatalf("after one contradiction: %+v", v)
	}
	if v.failed() {
		t.Fatal("one contradiction concluded a fake")
	}
}

func verificationFakeThenRestored(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	passVerification(t, l, f, c)
	for i, retry := range []time.Duration{verifyEvery, verifyRetry, 2 * verifyRetry} {
		*now = now.Add(retry)
		contradictVerification(t, l, f, c)
		if v := verificationOf(m, id); v.failures != i+1 {
			t.Fatalf("after %d contradictions: %+v", i+1, v)
		}
	}
	if v := verificationOf(m, id); !v.failed() {
		t.Fatalf("three contradictions are not a fake: %+v", v)
	}
	*now = now.Add(backoff(verifyFailures))
	passVerification(t, l, f, c)
	if got := measured(m, id, *now); !got.Verified {
		t.Fatalf("after a later pass: %+v", got)
	}
	v := verificationOf(m, id)
	if v.failures != 0 {
		t.Fatalf("a pass did not clear the contradictions: %+v", v)
	}
	if v.failed() {
		t.Fatal("a pass left the endpoint concluded fake")
	}
}

// TestInconclusiveVerificationKeepsThePass: a refusal or a cut-off answer says nothing about the claim, so it neither
// verifies nor un-verifies, and is retried sooner than a full interval.
func TestInconclusiveVerificationKeepsThePass(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	tick(t, l)
	collectVerification(t, l, f)

	*now = now.Add(verifyEvery)
	f.result = suppliers.TestResult{Streamed: true, Reply: "I cannot answer that."}
	tick(t, l)
	collectVerification(t, l, f)
	got := measured(m, id, *now)
	if !got.Verified || got.VerifyConfidence != .75 {
		t.Fatalf("after an inconclusive check: %+v, want the pass still counting at 0.75", got)
	}
	if v := verificationOf(m, id); v.failures != 0 || !v.next.Equal(now.Add(verifyRetry)) {
		t.Fatalf("after an inconclusive check: %+v, want a retry at %v", v, now.Add(verifyRetry))
	}
}

// TestVerificationChecksOnlyPlatformEndpoints: an endpoint the catalog cannot identify, and one a tenant owns, are
// never checked, so their traffic never pays for it.
func TestVerificationChecksOnlyPlatformEndpoints(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	l.identity = func(catalog.ModelID) (Claimed, bool) { return Claimed{}, false }
	tick(t, l)
	if l.active != 0 {
		t.Fatal("an unidentified endpoint was checked")
	}
	if v := verificationOf(m, id); !v.next.Equal(now.Add(verifyRetry)) {
		t.Fatalf("an unidentified endpoint is not retried soon: %+v", v)
	}

	owned := uuid.NewV7()
	f.endpoints = append(f.endpoints, suppliers.Routable{
		ID: owned, Site: uuid.NewV7(), Credentials: 1, Model: uuid.NewV7(), Formats: []protocol.Format{protocol.Chat},
	})
	l.identity = func(catalog.ModelID) (Claimed, bool) { return stated, true }
	tick(t, l)
	if l.active != 0 {
		t.Fatal("a tenant-owned endpoint was checked")
	}
}

// setVerificationUpstream changes the upstream name the endpoint projects, as a supplier sync does, and returns the new
// claim.
func setVerificationUpstream(f *probeFixture, upstream string) claim {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints[0].Upstream = upstream
	return claimOf(f.endpoints[0])
}

// TestVerificationFollowsTheUpstreamName: a pass is evidence about the upstream name that was checked. Renaming it
// clears the pass at once, so the new upstream is not verified until it passes a check of its own.
func TestVerificationFollowsTheUpstreamName(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	tick(t, l)
	collectVerification(t, l, f)
	if !measured(m, id, *now).Verified {
		t.Fatal("the first check did not verify the endpoint")
	}

	want := setVerificationUpstream(f, "another-upstream-name")
	*now = now.Add(time.Second)
	tick(t, l)
	if got := measured(m, id, *now); got.Verified || !got.VerifiedAt.IsZero() {
		t.Fatalf("a renamed upstream kept the old pass: %+v", got)
	}
	if v := verificationOf(m, id); v.claim != want || v.checked != (time.Time{}) || v.passed || v.failures != 0 {
		t.Fatalf("the old evidence was not cleared: %+v, want the claim %+v", v, want)
	}
	if l.active != 1 {
		t.Fatal("the renamed upstream was not checked at once")
	}
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	collectVerification(t, l, f)
	if !measured(m, id, *now).Verified {
		t.Fatal("the new upstream did not verify")
	}
}

// TestVerificationDropsACheckOfAnotherClaim: a check that started before the upstream name changed answers for a
// configuration that no longer serves traffic, so its pass is discarded and the new claim is checked instead.
func TestVerificationDropsACheckOfAnotherClaim(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	tick(t, l) // the check is in flight against the old claim

	want := setVerificationUpstream(f, "another-upstream-name")
	collectVerification(t, l, f) // its answer arrives after the change
	if got := measured(m, id, *now); got.Verified {
		t.Fatalf("an answer about another configuration verified the endpoint: %+v", got)
	}
	if v := verificationOf(m, id); v.claim != want || v.passed || v.checked != (time.Time{}) {
		t.Fatalf("the stale answer changed the state: %+v", v)
	}

	*now = now.Add(time.Second)
	tick(t, l)
	if l.active != 1 {
		t.Fatal("the changed claim was not checked again")
	}
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	collectVerification(t, l, f)
	if !measured(m, id, *now).Verified {
		t.Fatal("the check of the new claim did not verify")
	}
}

// TestVerificationAlertsAFakeOnce: crossing verifyFailures raises one critical fake alert, another contradiction does
// not repeat it, and a pass resolves it.
func TestVerificationAlertsAFakeOnce(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	rec := &signalRecorder{}
	l.alerts = rec
	c := newChallenge(func() float64 { return .5 }, stated)
	passVerification(t, l, f, c)

	for i := range verifyFailures + 1 {
		if i == 0 {
			*now = now.Add(verifyEvery) // the pass scheduled the next check six hours out
		} else {
			*now = now.Add(backoff(i))
		}
		contradictVerification(t, l, f, c)
	}
	raised, resolved := fakeSignals(rec, id)
	if raised != 1 {
		t.Fatalf("fake alerts raised: %d, want 1", raised)
	}
	if resolved != 0 {
		t.Fatalf("fake alerts resolved while still a fake: %d", resolved)
	}
	wantFakeAlert(t, rec, id)
	if measured(m, id, *now).Verified {
		t.Fatal("a fake concluded endpoint is still verified")
	}

	*now = now.Add(backoff(verifyFailures + 1))
	passVerification(t, l, f, c)
	if _, resolved = fakeSignals(rec, id); resolved != 1 {
		t.Fatalf("fake alerts resolved after a pass: %d, want 1", resolved)
	}
}

// fakeSignals counts the fake alert's raises and resolutions for one endpoint.
func fakeSignals(rec *signalRecorder, id uuid.UUID) (raised, resolved int) {
	for _, s := range rec.signals {
		if s.Kind != fakeAlert || s.Key != id.String() {
			continue
		}
		if s.Resolved {
			resolved++
		} else {
			raised++
		}
	}
	return raised, resolved
}

// wantFakeAlert checks the fields of the one raised fake alert.
func wantFakeAlert(t *testing.T, rec *signalRecorder, id uuid.UUID) {
	t.Helper()
	for _, s := range rec.signals {
		if s.Kind != fakeAlert || s.Resolved {
			continue
		}
		if s.Severity != "critical" {
			t.Fatalf("the fake alert's severity is %q, want critical", s.Severity)
		}
		if s.Key != id.String() {
			t.Fatalf("the fake alert's key is %q, want %q", s.Key, id.String())
		}
		if s.Fields["claimed_author"] != stated.Author {
			t.Fatalf("the fake alert claims %q, want %q", s.Fields["claimed_author"], stated.Author)
		}
		if s.Fields["failures"] != "3" {
			t.Fatalf("the fake alert counts %q failures, want 3", s.Fields["failures"])
		}
		return
	}
	t.Fatal("no fake alert was raised")
}
