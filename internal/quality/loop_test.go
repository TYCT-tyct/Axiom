package quality

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

type probeFixture struct {
	mu        sync.Mutex
	endpoints []suppliers.Routable
	stale     bool
	until     time.Time
	calls     chan suppliers.TestRequest
	release   chan struct{}
	result    suppliers.TestResult
}

func (f *probeFixture) Routable() ([]suppliers.Routable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stale {
		return nil, suppliers.ErrStale
	}
	return append([]suppliers.Routable(nil), f.endpoints...), nil
}

func (f *probeFixture) PausedUntil(_, _ uuid.UUID) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.until
}

func (f *probeFixture) TestEndpoint(ctx context.Context, r suppliers.TestRequest) (suppliers.TestResult, error) {
	f.calls <- r
	select {
	case <-f.release:
	case <-ctx.Done():
		return suppliers.TestResult{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.result, nil
}

func fixtureLoop(t *testing.T, mode Mode) (*Monitor, *Loop, *probeFixture, *time.Time) {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f := &probeFixture{calls: make(chan suppliers.TestRequest, 32), release: make(chan struct{}, 32), result: suppliers.TestResult{Streamed: true}}
	t.Cleanup(func() { close(f.release) })
	m, l, err := Start(t.Context(), Config{DB: db, Scope: pg.PlatformScope(), Mode: mode, Source: f, Pauses: f, Now: func() time.Time { return now }, Rand: func() float64 { return .5 }})
	if err != nil {
		t.Fatal(err)
	}
	return m, l, f, &now
}

func removeEndpoint(t *testing.T, m *Monitor, id uuid.UUID, at time.Time) {
	t.Helper()
	m.Live(id, 1)
	for i := range 5 {
		m.Observe(t.Context(), Observation{Endpoint: id, Source: uuid.UUID{byte(i%2 + 1)}, Sent: at.Add(-time.Second), Ended: at, Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}})
	}
}

func tick(t *testing.T, l *Loop) {
	t.Helper()
	if err := l.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func finishProbe(t *testing.T, l *Loop, f *probeFixture) {
	t.Helper()
	f.release <- struct{}{}
	end := <-l.done
	l.active--
	l.finish(t.Context(), end)
	if err := l.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func addProbeEndpoint(f *probeFixture) uuid.UUID {
	id := uuid.NewV7()
	f.endpoints = append(f.endpoints, suppliers.Routable{ID: id, Credentials: 1, Formats: []protocol.Format{protocol.Chat}})
	return id
}

func TestPersistLeaseRestartAndRecovery(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	if l.active != 0 {
		t.Fatal("probe ran before due")
	}
	restarted, other, err := Start(t.Context(), Config{DB: l.db, Scope: l.scope, Mode: Enforce, Source: f, Pauses: f, Now: l.now, Rand: l.rand})
	if err != nil {
		t.Fatal(err)
	}
	if at, _ := restarted.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("restart lost breaker")
	}
	*now = now.Add(30 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, loop := range []*Loop{l, other} {
		wg.Go(func() { errs <- loop.Tick(t.Context()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if l.active+other.active != 1 {
		t.Fatalf("competing nodes started %d probes", l.active+other.active)
	}
	<-f.calls
	winner := l
	if other.active == 1 {
		winner = other
	}
	exerciseProbeRecovery(t, winner, f, now, id)
}

func exerciseProbeRecovery(t *testing.T, winner *Loop, f *probeFixture, now *time.Time, id uuid.UUID) {
	t.Helper()
	f.mu.Lock()
	f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	f.mu.Unlock()
	finishProbe(t, winner, f)
	e := winner.monitor.Live(id, 1)
	e.mu.Lock()
	next := e.record.Next
	level := e.record.Backoff
	e.mu.Unlock()
	if next != now.Add(time.Minute).UnixNano() || level != 1 {
		t.Fatalf("backoff level %d next %v", level, time.Unix(0, next))
	}
	*now = now.Add(time.Minute)
	tick(t, winner)
	<-f.calls
	f.mu.Lock()
	f.result = suppliers.TestResult{Streamed: true}
	f.mu.Unlock()
	finishProbe(t, winner, f)
	if at, limit := e.Routing(*now); !at.IsZero() || limit != 1 {
		t.Fatalf("recovery %v, limit %d", at, limit)
	}
	var occurrences int
	if err := winner.db.ReadTx(t.Context(), winner.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT sum(occurrences) FROM quality.health_events WHERE endpoint_id=$1`, id).Scan(&occurrences)
	}); err != nil {
		t.Fatal(err)
	}
	if occurrences != 3 {
		t.Fatalf("transition count %d, want a trip, an offline and a restore", occurrences)
	}
}

func TestProbeDeferrals(t *testing.T) {
	for _, reason := range []string{"manual", "owned", "pause", "stale", "observe", "budget"} {
		t.Run(reason, func(t *testing.T) { checkProbeDeferral(t, reason) })
	}
}

func configureDeferral(reason string, l *Loop, f *probeFixture, now time.Time) {
	switch reason {
	case "manual":
		f.endpoints = nil
	case "owned":
		f.endpoints[0].Account = uuid.NewV7()
	case "pause":
		f.until = now.Add(time.Hour)
	case "stale":
		f.stale = true
	case "observe":
	case "budget":
		for i := range l.budget {
			l.budget[i] = now
		}
	}
}

func checkProbeDeferral(t *testing.T, reason string) {
	t.Helper()
	mode := Enforce
	if reason == "observe" {
		mode = Observe
	}
	m, l, f, now := fixtureLoop(t, mode)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	configureDeferral(reason, l, f, *now)
	*now = now.Add(31 * time.Second)
	err := l.Tick(t.Context())
	if reason == "stale" {
		if !errors.Is(err, suppliers.ErrStale) {
			t.Fatalf("stale: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if l.active != 0 {
		t.Fatalf("%s started a probe", reason)
	}
	if reason == "manual" {
		checkDisabledPersistence(t, m, l, now, id)
	}
	if reason == "pause" && l.used != 0 {
		t.Fatal("pause consumed budget")
	}
	if reason == "stale" {
		f.stale = false
		tick(t, l)
		if l.active != 0 {
			t.Fatal("stale recovery burst instead of rescheduling")
		}
	}
}

func checkDisabledPersistence(t *testing.T, m *Monitor, l *Loop, now *time.Time, id uuid.UUID) {
	t.Helper()
	*now = now.Add(24 * time.Hour)
	tick(t, l)
	var opened int64
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT opened_at FROM quality.endpoint_health WHERE endpoint_id=$1`, id).Scan(&opened)
	}); err != nil {
		t.Fatal(err)
	}
	if opened == 0 {
		t.Fatal("manual disable cleared durable automatic state")
	}
	if err := m.Prepare(t.Context(), []suppliers.Routable{{ID: id, Credentials: 1}}); err != nil {
		t.Fatal(err)
	}
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("reappearing endpoint lost durable breaker")
	}
}

func TestProbeConcurrencyCap(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	for range 8 {
		removeEndpoint(t, m, addProbeEndpoint(f), *now)
	}
	tick(t, l)
	*now = now.Add(30 * time.Second)
	tick(t, l)
	if l.active != 4 || l.used != 4 {
		t.Fatalf("active=%d budget=%d", l.active, l.used)
	}
	for range 4 {
		<-f.calls
	}
	tick(t, l)
	if l.active != 4 || l.used != 4 {
		t.Fatal("concurrency cap exceeded")
	}
}

func TestQualitySQLDoesNotWriteSuppliers(t *testing.T) {
	tree := os.DirFS(".")
	tables := regexp.MustCompile(`(?i)(FROM|JOIN|UPDATE|INTO)\s+suppliers\s*\.`)
	err := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(path, "_test.go") || !strings.HasSuffix(path, ".sql") && !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		if tables.Match(body) {
			t.Errorf("%s references suppliers tables", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommittedSignalSurvivesRestart(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	if signals := offlineSignals(t, l); signals != 0 {
		t.Fatalf("a trip alone sent %d offline signals", signals)
	}
	*now = now.Add(30 * time.Second)
	tick(t, l)
	f.mu.Lock()
	f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	f.mu.Unlock()
	finishProbe(t, l, f)
	if signals := offlineSignals(t, l); signals != 1 {
		t.Fatalf("the failed re-check persisted %d offline signals", signals)
	}
	restarted, other, err := Start(t.Context(), Config{DB: l.db, Scope: l.scope, Mode: Enforce, Source: f, Pauses: f, Now: l.now, Rand: l.rand})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute)
	tick(t, other)
	f.mu.Lock()
	f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	f.mu.Unlock()
	finishProbe(t, other, f)
	if signals := offlineSignals(t, l); signals != 1 {
		t.Fatalf("a repeated failed probe persisted %d offline signals", signals)
	}
	if at, _ := restarted.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("restart lost durable breaker")
	}
}

func offlineSignals(t *testing.T, l *Loop) (signals int) {
	t.Helper()
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='endpoint_offline'`).Scan(&signals)
	}); err != nil {
		t.Fatal(err)
	}
	return signals
}

func TestLosingEpochDiscardsDependentTransitions(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	other, second, err := Start(t.Context(), Config{DB: l.db, Scope: l.scope, Mode: Enforce, Source: f, Pauses: f, Now: l.now, Rand: l.rand})
	if err != nil {
		t.Fatal(err)
	}
	removeEndpoint(t, m, id, *now)
	removeEndpoint(t, other, id, now.Add(time.Second))
	e := second.monitor.of(id)
	e.mu.Lock()
	e.record.Owner = uuid.NewV7()
	e.record.Lease = now.Add(2 * time.Minute).UnixNano()
	claim := e.record
	e.mu.Unlock()
	second.finish(t.Context(), probeEnd{claim: claim, result: suppliers.TestResult{Streamed: true}})
	tick(t, l)
	tick(t, second)
	if at, _ := other.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("losing writer's dependent restore overwrote winning epoch")
	}
	var events int
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT sum(occurrences) FROM quality.health_events WHERE endpoint_id=$1`, id).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("losing epoch wrote %d events", events)
	}
}

func TestExpiredLeaseCannotRestoreNewProbeRound(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	*now = now.Add(30 * time.Second)
	tick(t, l)
	<-f.calls
	*now = now.Add(2*time.Minute + time.Second)
	secondSource := &probeFixture{endpoints: f.endpoints, calls: make(chan suppliers.TestRequest, 1), release: make(chan struct{}, 1), result: suppliers.TestResult{Streamed: true}}
	t.Cleanup(func() { close(secondSource.release) })
	_, second, err := Start(t.Context(), Config{DB: l.db, Scope: l.scope, Mode: Enforce, Source: secondSource, Pauses: f, Now: l.now, Rand: l.rand})
	if err != nil {
		t.Fatal(err)
	}
	tick(t, second)
	<-secondSource.calls
	finishProbe(t, l, f)
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("expired lease accepted old success")
	}
	finishProbe(t, second, secondSource)
	var opened int64
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT opened_at FROM quality.endpoint_health WHERE endpoint_id=$1`, id).Scan(&opened)
	}); err != nil {
		t.Fatal(err)
	}
	if opened != 0 {
		t.Fatal("current lease could not restore endpoint")
	}
}

func TestQueuedProbeCannotOverwriteNewLease(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	*now = now.Add(30 * time.Second)
	tick(t, l)
	<-f.calls
	f.release <- struct{}{}
	end := <-l.done
	l.active--
	l.finish(t.Context(), end)
	owner := uuid.NewV7()
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE quality.endpoint_health SET probe_owner=$2,probe_lease_until=$3 WHERE endpoint_id=$1`, id, owner, now.Add(3*time.Minute).UnixNano())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() {
		t.Fatal("queued old probe overwrote new lease")
	}
}

func TestProbeLeavesTimeToPersistCost(t *testing.T) {
	_, l, f, _ := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	ctx, cancel := context.WithTimeout(t.Context(), suppliers.TestDeadline)
	defer cancel()
	err := l.db.Tx(ctx, l.scope, func(tx pg.Tx) error {
		_, err := l.generate(ctx, tx, suppliers.TestRequest{Endpoint: id, Format: protocol.Chat}, "automatic health recovery")
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("insufficient commit budget: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("a paid generation started without time left to record its cost")
	}
}

func TestHealthMaintenanceRetention(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	if err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO quality.health_events (endpoint_id,epoch,at,kind,cause,failures,sources,request_id,signal_epoch,signal_kind,mode)
 VALUES ($1,100,$2,'would_remove','',0,0,$1,100,'would_remove','observe'),($1,101,$3,'would_remove','',0,0,$1,101,'would_remove','observe')`, uuid.NewV7(), now.Add(-91*24*time.Hour).UnixNano(), now.Add(-89*24*time.Hour).UnixNano())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	f.until = now.Add(time.Hour)
	for range 4 {
		tick(t, l)
	}
	var expired, retained int
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE epoch=100),count(*) FILTER (WHERE epoch=101) FROM quality.health_events`).Scan(&expired, &retained)
	}); err != nil {
		t.Fatal(err)
	}
	if expired != 0 || retained != 1 {
		t.Fatalf("retention: %d/%d", expired, retained)
	}
}
