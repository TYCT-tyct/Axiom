package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// A stream's beats, by the events of the recorded sample.
func (h *harness) feed(which ...int) beat { return beat{data: events(h.events, which...)} }

func (h *harness) after(d time.Duration, which ...int) beat {
	return beat{wait: d, data: events(h.events, which...)}
}

// elapsed is how long the call has run, in the bubble's time.
func (h *harness) elapsed() time.Duration { return time.Since(h.started) }

// TestAnUpstreamThatSaysNothingNewBeforeTheCommitPointIsTriedElsewhere: after the idle timeout the attempt is cancelled
// and the next entry answers; a comment, a keep-alive and complete events without content do not count as news, so
// they do not hold the timeout off.
func TestAnUpstreamThatSaysNothingNewBeforeTheCommitPointIsTriedElsewhere(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, startBy: 10 * time.Minute})
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(
			h.feed(evStart), beat{wait: 50 * time.Second, data: ": still here\n\n"}, h.after(50*time.Second, evBlock), h.after(50*time.Second, evPing),
		)}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		idle := h.cfg.IdleTimeout
		if got := h.elapsed(); got != idle {
			t.Errorf("the call took %v, the idle timeout is %v", got, idle)
		}
		ends := h.ended().ending.Attempts
		if h.w.status != 200 || len(ends) != 2 || ends[0].Result != billing.FailedBeforeCommit || ends[1].Result != billing.Committed {
			t.Errorf("status %d, attempts %+v", h.w.status, ends)
		}
		if strings.Contains(h.w.body(), "event: ping") {
			t.Errorf("a ping before the commit point reached the client:\n%s", h.w.body())
		}
	})
}

// TestContentKeepsAnUpstreamAlive: a stream that sends content now and then lives for longer than the idle timeout.
func TestContentKeepsAnUpstreamAlive(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 1})
		var beats []beat
		beats = append(beats, h.feed(evStart, evBlock), h.after(100*time.Second, evHello))
		for range 4 {
			beats = append(beats, h.after(100*time.Second, evBang))
		}
		beats = append(beats, h.after(100*time.Second, evBlockStop, evMsgDelta, evMsgStop))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(beats...)}
		h.run()
		if got := h.elapsed(); got != 600*time.Second || !strings.Contains(h.w.body(), "message_stop") {
			t.Errorf("took %v:\n%s", got, h.w.body())
		}
		if a := h.ended().ending.Attempts[0]; a.Result != billing.Committed || a.Finish != billing.FinishStop {
			t.Errorf("%+v", a)
		}
	})
}

// TestAfterTheCommitPointTheStreamIsKeptAliveAndThenEndedInPlace: a quiet stream gets the format's own keep-alive each
// time it has been quiet for the keep-alive interval, none before the first content and none after the ending; an
// upstream that stays quiet is ended with a timeout, where it is: no other entry is tried (I4), and the settlement is of
// the attempt that was committed.
func TestAfterTheCommitPointTheStreamIsKeptAliveAndThenEndedInPlace(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		// An idle timeout that is not a whole number of keep-alive intervals, or the last keep-alive and the end would be
		// due in the same instant.
		h := newHarness(t, ctx, options{entries: 2, config: func(c *Config) { c.IdleTimeout = 100 * time.Second }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock), h.after(time.Second, evHello))}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		if h.up.endpoint(2).sends != 0 {
			t.Error("another entry was tried after the commit point")
		}
		checkKeepAlives(t, h)
		last := h.w.writes[len(h.w.writes)-1].data
		if !strings.Contains(last, "event: error") || !strings.Contains(last, `"error_type":"timeout"`) {
			t.Errorf("the ending: %s", last)
		}
		if ends := h.ended().ending.Attempts; len(ends) != 1 || ends[0].Result != billing.Committed || ends[0].Finish != billing.FinishTimeout {
			t.Errorf("%+v", h.ended().ending)
		}
	})
}

// checkKeepAlives: the first write is the commit point, then a keep-alive for each time the keep-alive interval passed in
// silence, up to the idle timeout, then the ending.
func checkKeepAlives(t *testing.T, h *harness) {
	t.Helper()
	w := h.w
	if len(w.writes) < 3 || !strings.Contains(w.writes[0].data, `"text": "Hello"`) {
		t.Fatalf("the first write must be the commit point: %v", w.writes)
	}
	keep := string(protocol.KeepAlive(protocol.Messages))
	for i, x := range w.writes[1 : len(w.writes)-1] {
		if x.data != keep || x.at.Sub(w.writes[i].at) != h.cfg.KeepAlive {
			t.Fatalf("write %d is %q, %v after the one before", i+1, x.data, x.at.Sub(w.writes[i].at))
		}
	}
	if beats, want := len(w.writes)-2, int(h.cfg.IdleTimeout/h.cfg.KeepAlive); beats != want {
		t.Errorf("%d keep-alives, want %d", beats, want)
	}
}

// TestWritesHoldBackTheKeepAlive: a stream that writes often is not sent a keep-alive.
func TestWritesHoldBackTheKeepAlive(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		var beats []beat
		beats = append(beats, h.feed(evStart, evBlock, evHello))
		for range 5 {
			beats = append(beats, h.after(h.cfg.KeepAlive-time.Second, evBang))
		}
		beats = append(beats, h.after(time.Second, evBlockStop, evMsgDelta, evMsgStop))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(beats...)}
		h.run()
		if strings.Contains(h.w.body(), "keep-alive") || strings.Count(h.w.body(), "event: ping") != 0 {
			t.Errorf("a keep-alive in a stream that was never quiet:\n%s", h.w.body())
		}
	})
}

// TestAnUpstreamThatFailsBeforeTheCommitPointIsTriedElsewhere, in each way it can: it takes too long to answer, answers
// 200 with no content, or breaks off before any.
func TestAnUpstreamThatFailsBeforeTheCommitPointIsTriedElsewhere(t *testing.T) {
	cases := []struct {
		name    string
		first   func(h *harness) scripted
		elapsed func(c Config) time.Duration
	}{
		{"no response headers in time", func(h *harness) scripted { return late(10*time.Minute, whole(h)) }, func(c Config) time.Duration { return c.HeaderWait }},
		{"200 and nothing in it", func(h *harness) scripted { return serve(h.feed(evStart, evMsgDelta, evMsgStop)) }, func(Config) time.Duration { return 0 }},
		{"a hangup after the start", func(h *harness) scripted { return serve(h.feed(evStart), beat{hangup: true}) }, func(Config) time.Duration { return 0 }},
		{"a hangup with nothing sent", func(h *harness) scripted { return serve(beat{hangup: true}) }, func(Config) time.Duration { return 0 }},
		{"a broken event", func(h *harness) scripted { return serve(beat{data: "data: {not json\n\n"}) }, func(Config) time.Duration { return 0 }},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{entries: 2})
			h.up.eps[uuidOf(ep(1))].script = []scripted{tc.first(h)}
			h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			h.run()
			ends := h.ended().ending.Attempts
			if h.w.status != 200 || len(ends) != 2 || h.elapsed() != tc.elapsed(h.cfg) {
				t.Fatalf("%s: status %d, %d attempts, took %v", tc.name, h.w.status, len(ends), h.elapsed())
			}
			if first := ends[0]; (first.Result != billing.FailedBeforeCommit && first.Result != billing.Unknown) || ends[1].Result != billing.Committed {
				t.Errorf("%s: results %d and %d", tc.name, first.Result, ends[1].Result)
			}
		})
	}
}

// TestAStreamThatBreaksAfterTheCommitPointEndsWithAnErrorAndSettlesWhatCameFirst: the status is 200 and cannot change, no other
// entry is tried, and what was committed is settled.
func TestAStreamThatBreaksAfterTheCommitPointEndsWithAnErrorAndSettlesWhatCameFirst(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart, evBlock, evHello), beat{hangup: true})}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		if h.w.status != 200 || h.up.endpoint(2).sends != 0 || !strings.Contains(body, "event: error") || !strings.Contains(body, `"error_type":"upstream_error"`) {
			t.Errorf("status %d:\n%s", h.w.status, body)
		}
		if a := h.ended().ending.Attempts[0]; a.Result != billing.Committed || a.Finish != billing.FinishError {
			t.Errorf("%+v", a)
		}
	})
}

// TestTheCallStopsAtItsLongestTime: an attempt is cancelled when the call's time is out, and a call whose time is out
// starts no other: before the first content that is a 408, after it the stream ends with a timeout.
func TestTheCallStopsAtItsLongestTime(t *testing.T) {
	config := func(c *Config) {
		c.MaxCall, c.IdleTimeout, c.StartBy, c.QueueWait = 10*time.Minute, 9*time.Minute, 9*time.Minute, 9*time.Minute
	}
	run(t, func(t *testing.T, ctx context.Context) {
		// Two entries that never say anything: the first is given up at the idle timeout, the second is out of time.
		h := newHarness(t, ctx, options{entries: 2, config: config, startBy: 9*time.Minute + 30*time.Second})
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold()}
		h.up.eps[uuidOf(ep(2))].script = []scripted{hold()}
		h.run()
		if h.w.status != http.StatusRequestTimeout || h.elapsed() != 10*time.Minute || len(h.ended().ending.Attempts) != 2 {
			t.Errorf("status %d after %v, %d attempts\n%s", h.w.status, h.elapsed(), len(h.ended().ending.Attempts), h.w.body())
		}
		if !strings.Contains(h.w.body(), `"error_type":"timeout"`) {
			t.Errorf("the answer: %s", h.w.body())
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, config: config, startBy: 9 * time.Minute})
		// The idle timeout would end this at 14 minutes; the call has ten.
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello), h.after(5*time.Minute, evBang))}
		h.run()
		last := h.w.writes[len(h.w.writes)-1].data
		if h.elapsed() != 10*time.Minute || h.up.endpoint(2).sends != 0 || !strings.Contains(last, `"error_type":"timeout"`) {
			t.Errorf("after %v:\n%s", h.elapsed(), last)
		}
		if a := h.ended().ending.Attempts[0]; a.Finish != billing.FinishTimeout || a.Result != billing.Committed {
			t.Errorf("%+v", a)
		}
	})
}

// TestNoNewAttemptStartsAfterStartBy: an entry that fails after the window is not followed by another, and a call that is
// past it before its first attempt is a 408 with none made.
func TestNoNewAttemptStartsAfterStartBy(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, startBy: 10 * time.Second})
		h.up.eps[uuidOf(ep(1))].script = []scripted{late(20*time.Second, failure(routing.ScopeEndpoint, false))}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		if h.w.status != http.StatusBadGateway || h.up.endpoint(2).sends != 0 || len(h.ended().ending.Attempts) != 1 {
			t.Errorf("status %d, entry 2 sends %d", h.w.status, h.up.endpoint(2).sends)
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{startBy: -time.Second})
		h.run()
		if h.w.status != http.StatusRequestTimeout || h.up.endpoint(1).acquired != 0 {
			t.Errorf("status %d, %d acquired", h.w.status, h.up.endpoint(1).acquired)
		}
	})
}

// TestAThrottledEndpointIsWeighedDown: a 429 is told to routing, which gives the endpoint less of the traffic after it.
// Two endpoints alike and a draw that never changes: the first always comes first, until the 429.
func TestAThrottledEndpointIsWeighedDown(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, routing: routing.Config{Rand: func() float64 { return 0.5 }}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{throttled()}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		firstChoice := twoAlikeEndpoints(t, h.router)
		if got := firstChoice(); got != ep(1) {
			t.Fatalf("before the 429 the first choice is %v", got)
		}
		h.run()
		if got := firstChoice(); got != ep(2) {
			t.Errorf("after the 429 the first choice is still %v", got)
		}
		a := h.call.Plan.Record.Attempts
		if h.w.status != 200 || len(a) != 2 || !a[0].Failed || a[0].Action != routing.NextEntry || a[1].Failed {
			t.Errorf("status %d, the route's record: %+v", h.w.status, a)
		}
	})
}

// twoAlikeEndpoints gives the router two endpoints (ep(1), ep(2)) of one model that differ in nothing, and returns what
// plans the first choice.
func twoAlikeEndpoints(t *testing.T, router *routing.Router) func() routing.EndpointID {
	t.Helper()
	schedule, err := catalog.ParseSchedule([]byte(cheap))
	if err != nil {
		t.Fatal(err)
	}
	model := catalog.ModelID{1}
	all := routing.Of(protocol.Messages)
	var endpoints []routing.Endpoint
	for n := 1; n <= 2; n++ {
		endpoints = append(endpoints, routing.Endpoint{
			ID: ep(n), Supplier: sup(n), Model: model, Enabled: true, BalanceOK: true, Egresses: 1, Class: routing.ClassOfficial,
			Policy: routing.PolicyNoRetention, Verified: true, Quality: 1, Confidence: 1, Health: 1, Throughput: 100,
			ServiceTiers: 1 << catalog.ServiceTierStandard, Native: all, Served: all, Caps: ^protocol.Capabilities(0),
			Context: 1_000_000, MaxOutput: 1000, Streams: true, Cancels: true, Price: routing.PriceRef{Version: uuid.UUID{1}, Schedule: schedule},
		})
	}
	snap, err := router.NewSnapshot(time.Now(), endpoints, []routing.Preset{{Classes: routing.AllClasses, Policies: routing.AllPolicies}})
	if err != nil {
		t.Fatal(err)
	}
	router.Replace(snap)
	return func() routing.EndpointID {
		plan, wait, err := router.Plan(routing.Input{
			Req:    &protocol.Request{Format: protocol.Messages, Model: "m", Choices: 1, TextBytes: 100, ServiceTiers: 1 << catalog.ServiceTierStandard},
			Models: []catalog.ModelID{model},
		})
		if wait != nil || err != nil {
			t.Fatalf("plan: %v %v", wait, err)
		}
		return plan.Entries[0].Endpoint
	}
}

// TestAnUpstreamsOwnPingIsPassedOnAfterTheCommitPoint, and only then.
func TestAnUpstreamsOwnPingIsPassedOnAfterTheCommitPoint(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart, evPing, evBlock, evHello, evPing, evBang, evBlockStop, evMsgDelta, evMsgStop))}
		h.run()
		if got := strings.Count(h.w.body(), "event: ping"); got != 1 {
			t.Errorf("%d pings: %s", got, h.w.body())
		}
	})
}

// TestAnEventArrivingInPiecesKeepsTheUpstreamAlive: bytes that make progress on an event are news, though no event is
// whole until the last of them.
func TestAnEventArrivingInPiecesKeepsTheUpstreamAlive(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		hello := h.events[evHello]
		third := len(hello) / 3
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(
			h.feed(evStart, evBlock), beat{wait: 100 * time.Second, data: hello[:third]}, beat{wait: 100 * time.Second, data: hello[third : 2*third]},
			beat{wait: 100 * time.Second, data: hello[2*third:]}, h.feed(evBang, evBlockStop, evMsgDelta, evMsgStop),
		)}
		h.run()
		if h.elapsed() != 300*time.Second || !strings.Contains(h.w.body(), "message_stop") || len(h.ended().ending.Attempts) != 1 {
			t.Errorf("took %v: %s", h.elapsed(), h.w.body())
		}
	})
}
