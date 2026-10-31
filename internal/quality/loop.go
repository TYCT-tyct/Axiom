package quality

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

const (
	probeConcurrency = 4
	probeHourlyLimit = 600
	probeLease       = 2 * time.Minute
	probeCommitGrace = 5 * time.Second
)

// Pauses is routing's read-only view of endpoint and supplier Retry-After pauses.
type Pauses interface {
	PausedUntil(endpoint, supplier uuid.UUID) time.Time
}

// ProbeSource is the supplier snapshot and the real generation path, including credential admission.
type ProbeSource interface {
	Routable() ([]suppliers.Routable, error)
	TestEndpoint(context.Context, suppliers.TestRequest) (suppliers.TestResult, error)
}

// Config supplies the background loop; Node is the stable identity used for capacity alerts. Identity says what an
// endpoint claims to serve, from the catalog, and is nil when nothing can answer it: verification then stays unknown.
type Config struct {
	Node     string
	DB       *pg.DB
	Scope    pg.Scope
	Mode     Mode
	Source   ProbeSource
	Pauses   Pauses
	Alerts   alert.Sink
	Logger   *slog.Logger
	Meter    metric.Meter
	Now      func() time.Time
	Rand     func() float64
	Identity func(catalog.ModelID) (Claimed, bool)
}

// Loop persists transitions and schedules probes. Tick has one caller; workers only return results through done.
type Loop struct {
	unavailable         map[uuid.UUID]bool
	monitor             *Monitor
	db                  *pg.DB
	scope               pg.Scope
	source              ProbeSource
	pauses              Pauses
	alerts              alert.Sink
	logger              *slog.Logger
	now                 func() time.Time
	rand                func() float64
	node                string
	identity            func(catalog.ModelID) (Claimed, bool)
	budgetState         uint8
	owner               uuid.UUID
	retry               *change
	active              int
	budget              [probeHourlyLimit]time.Time
	used                int
	done                chan probeEnd
	verifyDone          chan verifyEnd
	stale               bool
	cleanup             time.Time
	probes, transitions metric.Int64Counter
	verification        metric.Int64Counter
	duration            metric.Float64Histogram
}

type probeEnd struct {
	claim  healthRecord
	result suppliers.TestResult
	err    error
}

// verifyEnd is one verification generation's result: the outcome its answer was judged to have, and what it claimed.
type verifyEnd struct {
	endpoint suppliers.EndpointID
	claim    claim
	claimed  Claimed
	outcome  verifyOutcome
	result   suppliers.TestResult
	err      error
}

// Start loads persisted state synchronously, before the node serves traffic.
func Start(ctx context.Context, c Config) (*Monitor, *Loop, error) {
	mode, err := ParseMode(string(c.Mode))
	if err != nil {
		return nil, nil, err
	}
	c.Mode = mode
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Rand == nil {
		c.Rand = rand.Float64
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	m := New(c.Meter)
	m.mode = c.Mode
	l := &Loop{
		monitor: m, db: c.DB, scope: c.Scope, source: c.Source, pauses: c.Pauses, alerts: c.Alerts, logger: c.Logger,
		node: c.Node, now: c.Now, rand: c.Rand, identity: c.Identity, unavailable: map[uuid.UUID]bool{}, owner: uuid.NewV7(),
		done: make(chan probeEnd, probeConcurrency), verifyDone: make(chan verifyEnd, probeConcurrency),
	}
	m.loop = l

	l.instrument(c.Meter)
	if err := l.load(ctx); err != nil {
		return nil, nil, err
	}
	return m, l, nil
}

// Run retries failed writes with bounded backoff; Tick is also usable with a controlled clock.
func (l *Loop) Run(ctx context.Context) {
	wait := time.Second
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if err := l.Drain(context.WithoutCancel(ctx)); err != nil {
				l.logger.Error("quality shutdown persistence failed", "err", err)
			}
			return
		case <-timer.C:
		}
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := l.Tick(work)
		cancel()
		if err != nil {
			l.logger.Error("quality loop failed", "err", err)
			wait = min(wait*2, 30*time.Second)
		} else {
			wait = time.Second
		}
	}
}

// Drain waits for the bounded in-flight generations and persists their results. It shares Tick's single caller.
func (l *Loop) Drain(ctx context.Context) error {
	for l.active > 0 {
		select {
		case end := <-l.done:
			l.active--
			l.finish(ctx, end)
		case end := <-l.verifyDone:
			l.active--
			l.finishVerification(ctx, end)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return l.flush(ctx)
}

func (l *Loop) flush(ctx context.Context) error {
	for {
		if l.retry == nil {
			select {
			case c := <-l.monitor.changes:
				l.retry = &c
			default:
				return nil
			}
		}
		if err := l.write(ctx, *l.retry); err != nil {
			return err
		}
		l.retry = nil
	}
}

// collectResults takes every finished generation without waiting; Drain waits for them instead.
func (l *Loop) collectResults(ctx context.Context) {
	for {
		select {
		case end := <-l.done:
			l.active--
			l.finish(ctx, end)
		case end := <-l.verifyDone:
			l.active--
			l.finishVerification(ctx, end)
		default:
			return
		}
	}
}

// Tick first commits evidence, then claims due probes in order. A stale source sends no traffic.
func (l *Loop) Tick(ctx context.Context) error {
	l.collectResults(ctx)
	if err := l.flush(ctx); err != nil {
		return err
	}
	endpoints, err := l.source.Routable()
	if err != nil {
		l.stale = true
		return err
	}
	if err := l.monitor.Prepare(ctx, endpoints); err != nil {
		return err
	}
	now := l.now()
	due := l.due(ctx, endpoints, now)
	if l.stale {
		l.stale = false
		for _, e := range due {
			e.mu.Lock()
			r := e.record
			r.Next = now.Add(time.Duration(1 + l.rand()*float64(probeBase))).UnixNano()
			l.monitor.transition(e, r, "", false)
			e.mu.Unlock()
		}
		return nil
	}
	if err := l.schedule(ctx, due, endpoints, now); err != nil {
		return err
	}
	if l.identity != nil {
		l.scheduleVerification(ctx, l.verificationDue(endpoints, now), endpoints, now)
	}
	if err := l.checkModels(endpoints); err != nil {
		return err
	}
	return l.collect(ctx, endpoints, now)
}

func (l *Loop) schedule(ctx context.Context, due []*State, endpoints []suppliers.Routable, now time.Time) error {
	for _, e := range due {
		if l.active >= probeConcurrency {
			l.probeMetric(ctx, "deferred")
			return l.budgetSignal(true)
		}
		if l.spent(now) {
			l.probeMetric(ctx, "deferred")
			return l.budgetSignal(true)
		}
		claim, ok, err := l.claim(ctx, e, now)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		ep := endpoints[slices.IndexFunc(endpoints, func(ep suppliers.Routable) bool { return ep.ID == claim.ID })]
		l.budget[l.used%len(l.budget)] = now
		l.used++
		l.active++
		go l.probe(context.WithoutCancel(ctx), claim, ep, probePrompt(l.rand()))
	}
	return l.budgetSignal(false)
}

func (l *Loop) due(ctx context.Context, endpoints []suppliers.Routable, now time.Time) []*State {
	var due []*State
	for _, ep := range endpoints {
		e := l.monitor.Live(ep.ID, ep.Credentials)
		e.mu.Lock()
		e.lastSeen = now
		l.warnDown(e, now)
		r := e.record
		if l.monitor.mode == Enforce && ep.Platform() && r.Opened != 0 && r.Next <= now.UnixNano() && r.Lease <= now.UnixNano() && e.pending == 0 {
			if ep.Funds != nil && !ep.Funds.CanPay(platform.Exact{}) {
				l.probeMetric(ctx, "deferred")
			} else if until := l.pauses.PausedUntil(ep.ID, ep.Supplier); until.After(now) {
				r.Next = until.UnixNano()
				l.monitor.transition(e, r, "", false)
				l.probeMetric(ctx, "deferred")
			} else {
				due = append(due, e)
			}
		}
		e.mu.Unlock()
	}
	slices.SortFunc(due, compareNextProbe)
	return due
}

func compareNextProbe(a, b *State) int {
	a.mu.Lock()
	at := a.record.Next
	a.mu.Unlock()
	b.mu.Lock()
	bt := b.record.Next
	b.mu.Unlock()
	if at < bt {
		return -1
	}
	if at > bt {
		return 1
	}
	return a.id.Compare(b.id)
}

func (l *Loop) warnDown(e *State, now time.Time) {
	r := e.record
	if r.Opened != 0 && !r.Warned && now.Sub(time.Unix(0, r.Opened)) >= time.Hour && r.Lease <= now.UnixNano() && e.pending == 0 {
		r.Warned = true
		l.monitor.transition(e, r, "still_down", false)
	}
}

func (l *Loop) probe(ctx context.Context, claim healthRecord, ep suppliers.Routable, prompt string) {
	end := probeEnd{claim: claim}
	ctx, cancel := context.WithTimeout(ctx, suppliers.TestDeadline+2*probeCommitGrace)
	defer cancel()
	end.err = l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		result, err := l.generate(ctx, tx, suppliers.TestRequest{Endpoint: ep.ID, Format: ep.Formats[0], Prompt: prompt, AvoidCredential: claim.Credential}, "automatic health recovery")
		end.result = result
		return err
	})
	l.done <- end
}

// TestEndpoint intentionally outlives caller cancellation, so a paid generation needs its full budget plus commit time.
func (l *Loop) generate(ctx context.Context, tx pg.Tx, request suppliers.TestRequest, reason string) (suppliers.TestResult, error) {
	deadline, _ := ctx.Deadline()
	if time.Until(deadline) < suppliers.TestDeadline+probeCommitGrace {
		return suppliers.TestResult{}, context.DeadlineExceeded
	}
	result, err := l.source.TestEndpoint(ctx, request)
	if err != nil || uuid.UUID(result.Request) == (uuid.UUID{}) {
		return result, err
	}
	return result, suppliers.RecordTest(ctx, tx, audit.System("quality.probe"), reason, result)
}

func (l *Loop) finish(ctx context.Context, end probeEnd) {
	e := l.monitor.of(end.claim.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.currentProbe(end.claim, l.now()) {
		return
	}
	now := l.now()
	r := e.record
	r.Last = now.UnixNano()
	r.Lease = 0
	r.Owner = uuid.UUID{}
	outcome := suppliers.TestInconclusive
	if end.err == nil {
		outcome = end.result.Outcome()
	} else {
		l.logger.Error("quality probe failed", "err", end.err)
	}
	r.Result = outcome.String()
	if credential := end.result.CredentialID(); credential != (uuid.UUID{}) {
		r.Credential = credential
	}
	r.Request = uuid.UUID(end.result.Request)
	kind := ""
	switch outcome {
	case suppliers.TestPassed:
		e.count(success, now) // evidence for the quality score; the window stays for independent sources
		r.Opened = 0
		r.Restored = now.UnixNano()
		r.Next = 0
		kind = "restored"
	case suppliers.TestFailed:
		e.count(failure, now)
		r.Backoff = min(r.Backoff+1, 5)
	case suppliers.TestInconclusive:
	}
	l.reschedule(&r, end.result.Failure, now)
	if l.monitor.transition(e, r, kind, true) && outcome == suppliers.TestPassed {
		e.window = window{}
		e.successes = 0
	}
	l.probeMetric(ctx, outcome.String())
	if l.duration != nil && end.result.Request != (platform.RequestID{}) {
		l.duration.Record(ctx, end.result.Total.Seconds())
	}
}

// spent says this hour's probe budget is used up.
func (l *Loop) spent(now time.Time) bool {
	at := l.budget[l.used%len(l.budget)]
	return !at.IsZero() && now.Sub(at) < time.Hour
}

// spread is d with up to a tenth of jitter either way, so checks due together do not all go at once.
func (l *Loop) spread(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (.9 + .2*l.rand()))
}

func (l *Loop) reschedule(r *healthRecord, f *suppliers.Failure, now time.Time) {
	if r.Opened == 0 {
		return
	}
	r.Next = now.Add(l.monitor.delay(r.Backoff)).UnixNano()
	if f == nil {
		return
	}
	if rate, ok := f.Cause.(suppliers.RateLimited); ok {
		r.Next = max(r.Next, now.Add(min(rate.RetryAfter, 2*time.Hour)).UnixNano())
	}
}

func (l *Loop) collect(ctx context.Context, endpoints []suppliers.Routable, now time.Time) error {
	if !l.cleanup.IsZero() && now.Sub(l.cleanup) < 24*time.Hour {
		return nil
	}
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM quality.health_events WHERE at<$1`, now.Add(-90*24*time.Hour).UnixNano())
		return err
	})
	if err != nil {
		return err
	}
	l.cleanup = now
	present := make(map[suppliers.EndpointID]struct{}, len(endpoints))
	for _, ep := range endpoints {
		present[ep.ID] = struct{}{}
	}
	l.monitor.mu.Lock()
	defer l.monitor.mu.Unlock()
	for id, e := range l.monitor.endpoints {
		e.mu.Lock()
		if e.lastSeen.IsZero() {
			e.lastSeen = now
		}
		_, exists := present[id]
		absent := !exists
		if absent && now.Sub(e.lastSeen) > 24*time.Hour && e.pending == 0 && e.record.Lease <= now.UnixNano() {
			delete(l.monitor.endpoints, id)
			l.monitor.collected[id] = struct{}{}
		}
		e.mu.Unlock()
	}
	return nil
}

func (l *Loop) probeMetric(ctx context.Context, result string) {
	if l.probes != nil {
		l.probes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}

func (l *Loop) verifyMetric(ctx context.Context, result string) {
	if l.verification != nil {
		l.verification.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
}

// The question and the number vary independently: 256 small, deterministic answers without a fixed probe phrase.
func probePrompt(u float64) string {
	questions := [4]string{"Write the integer after ", "Return one greater than ", "Add one to ", "What number follows "}
	n := min(int(u*256), 255)
	return questions[n/64] + strconv.Itoa(n%64) + ". Answer briefly."
}

func (l *Loop) instrument(meter metric.Meter) {
	if meter != nil {
		var err error
		l.duration, err = meter.Float64Histogram("axiom.quality.probe.duration", metric.WithUnit("s"))
		if err != nil {
			panic(err)
		}
		l.probes = counter(meter, "axiom.quality.probes", "Health probe results.")
		l.verification = counter(meter, "axiom.quality.verification",
			"Model verification checks by result: passed, contradicted or inconclusive, and deferred when the budget or the concurrency is spent.")
		l.transitions = counter(meter, "axiom.quality.health.transitions", "Health transitions.")
	}
}

func (e *State) currentProbe(claim healthRecord, now time.Time) bool {
	return e.record.Epoch == claim.Epoch && e.record.Owner == claim.Owner && e.record.Lease == claim.Lease && now.UnixNano() < claim.Lease
}
