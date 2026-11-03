package quality

import (
	"context"
	"slices"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// verificationDue is the endpoints whose claim is due a check: routable, not broken, not being probed or verified,
// not paused and funded. It consumes no budget; scheduleVerification does.
func (l *Loop) verificationDue(endpoints []suppliers.Routable, now time.Time) []*State {
	var due []*State
	for _, ep := range endpoints {
		if !ep.Platform() || len(ep.Formats) == 0 {
			continue
		}
		e := l.monitor.Live(ep.ID, ep.Credentials)
		e.mu.Lock()
		l.bindVerification(e, ep)
		if l.verificationReady(e, ep, now) {
			due = append(due, e)
		}
		e.mu.Unlock()
	}
	slices.SortFunc(due, compareNextVerification)
	return due
}

// bindVerification keeps the evidence about the configuration it checked: when the endpoint now names another model,
// upstream or format, the old evidence stops counting, its fake conclusion is withdrawn, and the new claim is due now.
// The caller holds e.mu.
func (l *Loop) bindVerification(e *State, ep suppliers.Routable) {
	want := claimOf(ep)
	if e.verify.claim == want {
		return
	}
	if e.verify.failed() {
		l.resolveFake(ep.ID)
	}
	e.verify = verification{claim: want}
}

// verificationReady says a check is due now: the endpoint is not broken, not being probed or verified, not paused and
// funded. The caller holds e.mu.
func (l *Loop) verificationReady(e *State, ep suppliers.Routable, now time.Time) bool {
	if e.record.Opened != 0 || e.pending != 0 || e.verifying || !e.verify.due(now) {
		return false
	}
	if ep.Funds != nil && !ep.Funds.CanPay(platform.Exact{}) {
		return false
	}
	return l.pauses == nil || !l.pauses.PausedUntil(ep.ID, ep.Supplier).After(now)
}

func compareNextVerification(a, b *State) int {
	a.mu.Lock()
	at := a.verify.next
	a.mu.Unlock()
	b.mu.Lock()
	bt := b.verify.next
	b.mu.Unlock()
	if at.Before(bt) {
		return -1
	}
	if at.After(bt) {
		return 1
	}
	return a.id.Compare(b.id)
}

// scheduleVerification starts a check for each due endpoint the budget and the concurrency allow, after the recovery
// probes of the same tick have had their share.
func (l *Loop) scheduleVerification(ctx context.Context, due []*State, endpoints []suppliers.Routable, now time.Time) {
	settings := l.currentSettings()
	for _, e := range due {
		if l.active >= settings.Concurrency || l.spent(now) {
			l.verifyMetric(ctx, "deferred")
			return
		}
		idx := slices.IndexFunc(endpoints, func(ep suppliers.Routable) bool { return ep.ID == e.id })
		if idx < 0 {
			continue
		}
		ep := endpoints[idx]
		claimed, ok := l.identity(catalog.ModelID(ep.Model))
		if !ok || !claimed.comparable() || ep.Upstream == "" {
			// Nothing to compare an answer with; a catalog that has not loaded is retried soon.
			e.mu.Lock()
			e.verify.attempted = now
			e.verify.next = now.Add(l.spread(settings.VerifyRetry))
			e.mu.Unlock()
			continue
		}
		want := claimOf(ep)
		e.mu.Lock()
		if e.verifying || !e.verify.due(now) || e.verify.claim != want {
			e.mu.Unlock()
			continue
		}
		e.verifying = true
		e.mu.Unlock()
		l.budget.spend(now)
		l.active++
		go l.verify(context.WithoutCancel(ctx), ep, want, newChallenge(l.rand, claimed))
	}
}

// verify runs one verification generation and hands its answer back to the single Tick caller.
func (l *Loop) verify(ctx context.Context, ep suppliers.Routable, want claim, c challenge) {
	end := verifyEnd{endpoint: ep.ID, claim: want, claimed: c.claimed}
	ctx, cancel := context.WithTimeout(ctx, suppliers.TestDeadline+2*probeCommitGrace)
	defer cancel()
	end.err = l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		result, err := l.generate(ctx, tx, suppliers.TestRequest{
			Endpoint: ep.ID, Format: want.format, Prompt: c.prompt(), Output: verifyOutput, Capture: verifyReply,
		}, "automatic model verification")
		end.result = result
		return err
	})
	// A check whose cost and audit did not commit is treated like a probe's: the generation may have happened, but
	// the evidence is not recorded, so it changes nothing.
	if end.err == nil {
		end.outcome = judgeVerification(end.result, c)
	}
	l.verifyDone <- end
}

// finishVerification takes one check's result: a pass makes the endpoint Verified until the pass ages out, a
// contradiction clears it at once, and an answer that says nothing keeps the last evidence.
func (l *Loop) finishVerification(ctx context.Context, end verifyEnd) {
	current, ok := l.currentClaim(end.endpoint)
	e := l.monitor.of(end.endpoint)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.verifying = false
	if !l.verificationApplies(e, end, current, ok) {
		l.verifyMetric(ctx, "stale")
		return
	}
	now := l.now()
	if end.err != nil {
		l.logger.Error("quality verification failed", "err", end.err, "request", end.result.Request.String())
	}
	l.applyVerification(ctx, e, end, now)
}

// verificationApplies says a finished check describes the configuration that serves traffic now, and otherwise clears
// evidence about a configuration that is gone. The caller holds e.mu.
func (l *Loop) verificationApplies(e *State, end verifyEnd, current claim, ok bool) bool {
	if ok && current == end.claim && e.verify.claim == end.claim {
		return true
	}
	if ok && e.verify.claim != current {
		if e.verify.failed() {
			l.resolveFake(e.id)
		}
		e.verify = verification{claim: current}
	}
	return false
}

// applyVerification records the outcome of a check about the current claim. The caller holds e.mu.
func (l *Loop) applyVerification(ctx context.Context, e *State, end verifyEnd, now time.Time) {
	settings := l.currentSettings()
	e.verify.attempted = now
	switch end.outcome {
	case verifyPassed:
		if e.verify.failed() {
			l.resolveFake(e.id)
		}
		e.verify.outcome = verifyPassed
		e.verify.passed, e.verify.failures, e.verify.checked = true, 0, now
		e.verify.next = now.Add(l.spread(settings.VerifyEvery))
	case verifyContradicted:
		was := e.verify.failures
		e.verify.outcome = verifyContradicted
		e.verify.passed = false
		e.verify.failures++
		e.verify.next = now.Add(l.spread(backoff(e.verify.failures, settings.VerifyRetry, settings.VerifyEvery)))
		if was < verifyFailures && e.verify.failures >= verifyFailures {
			l.raiseFake(e.id, end.claimed.Author, e.verify.failures, now)
		}
		l.logger.Warn("quality verification contradicted the claimed model",
			"endpoint", e.id.String(), "author", end.claimed.Author, "failures", e.verify.failures,
			"request", end.result.Request.String())
	case verifyUnknown:
		e.verify.outcome = verifyUnknown
		e.verify.next = now.Add(l.spread(settings.VerifyRetry))
	}
	l.verifyMetric(ctx, end.outcome.String())
}

// currentClaim is the claim the endpoint makes in the routing snapshot the check ran against.
func (l *Loop) currentClaim(id suppliers.EndpointID) (claim, bool) {
	endpoints, err := l.source.Routable()
	if err != nil {
		return claim{}, false
	}
	for _, ep := range endpoints {
		if ep.ID == id {
			if len(ep.Formats) == 0 {
				return claim{}, false
			}
			return claimOf(ep), true
		}
	}
	return claim{}, false
}
