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

// TestVerificationPassesAndFailsClosed: a passed check is what sets Verified, with its time and confidence; one
// contradiction clears it at once, three in a row are a fake, and a later pass restores the endpoint. The check is a
// real generation with the challenge's own prompt and a captured answer.
func TestVerificationPassesAndFailsClosed(t *testing.T) {
	m, l, f, now, id := verificationWorld(t)
	c := newChallenge(func() float64 { return .5 }, stated)
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	tick(t, l)
	if l.active != 1 {
		t.Fatalf("active %d, want one verification in flight", l.active)
	}
	collectVerification(t, l, f)
	if got := m.Measured(id, *now); !got.Verified || got.VerifyConfidence != 1 || !got.VerifiedAt.Equal(*now) {
		t.Fatalf("after a pass: %+v, want verified now with confidence 1", got)
	}
	req := <-f.calls
	if req.Prompt == "" || !strings.Contains(req.Prompt, c.nonce) || req.Capture != verifyReply || req.Output != verifyOutput {
		t.Errorf("the check sent %+v, want the nonce, the capture and %d tokens", req, verifyOutput)
	}

	// One contradiction: no longer verified, but not yet concluded fake.
	*now = now.Add(verifyEvery)
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "OpenAI")}
	tick(t, l)
	collectVerification(t, l, f)
	if got := m.Measured(id, *now); got.Verified || got.VerifyConfidence != 0 {
		t.Fatalf("after a contradiction: %+v, want unverified", got)
	}
	if v := verificationOf(m, id); v.failures != 1 || v.failed() {
		t.Fatalf("after one contradiction: %+v", v)
	}

	// Two more, each at its backed-off schedule, are the fake.
	for i, retry := range []time.Duration{verifyRetry, 2 * verifyRetry} {
		*now = now.Add(retry)
		tick(t, l)
		collectVerification(t, l, f)
		if v := verificationOf(m, id); v.failures != i+2 {
			t.Fatalf("after %d contradictions: %+v", i+2, v)
		}
	}
	if v := verificationOf(m, id); !v.failed() {
		t.Fatalf("three contradictions are not a fake: %+v", v)
	}

	// A pass after the backoff restores it.
	*now = now.Add(backoff(verifyRetry, verifyFailures, verifyEvery))
	f.result = suppliers.TestResult{Streamed: true, Reply: verificationAnswer(c, "Anthropic")}
	tick(t, l)
	collectVerification(t, l, f)
	if got := m.Measured(id, *now); !got.Verified {
		t.Fatalf("after a later pass: %+v", got)
	}
	if v := verificationOf(m, id); v.failures != 0 || v.failed() {
		t.Fatalf("a pass did not clear the contradictions: %+v", v)
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
	got := m.Measured(id, *now)
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
