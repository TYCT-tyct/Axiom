package quality

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The administrator's probe settings (docs/domains/quality.md「试探」): the hourly budget and concurrency of recovery
// probes, the intervals of the fingerprint check, and the first probe delay. The defaults are code; the record holds
// only departures, and a node reloads it within settingsEvery.
const (
	defaultHourlyBudget = 600
	defaultConcurrency  = 4
	defaultVerifyEvery  = 6 * time.Hour
	defaultVerifyMaxAge = 24 * time.Hour
	defaultVerifyRetry  = 15 * time.Minute
	defaultProbeBase    = 30 * time.Second

	maxHourlyBudget = 100_000
	// maxProbeConcurrency bounds the channels a node builds at start; a larger setting is refused, not truncated.
	maxProbeConcurrency = 64
	maxVerifyInterval   = 30 * 24 * time.Hour
	maxVerifyRetry      = 24 * time.Hour
	maxProbeBase        = probeMax // the breaker never waits longer than this, so a larger base would be silently capped
	minVerifyInterval   = time.Minute
	settingsEvery       = 30 * time.Second
)

// ProbeSettings are the administrator's probe settings. A zero value is the default in code, so the record
// stores only what was deliberately changed.
type ProbeSettings struct {
	HourlyBudget int
	Concurrency  int
	VerifyEvery  time.Duration
	VerifyMaxAge time.Duration
	VerifyRetry  time.Duration
	ProbeBase    time.Duration
}

// DefaultProbeSettings are the settings that apply where the administrator recorded none.
func DefaultProbeSettings() ProbeSettings {
	return ProbeSettings{
		HourlyBudget: defaultHourlyBudget, Concurrency: defaultConcurrency,
		VerifyEvery: defaultVerifyEvery, VerifyMaxAge: defaultVerifyMaxAge, VerifyRetry: defaultVerifyRetry,
		ProbeBase: defaultProbeBase,
	}
}

// Effective is the settings a node uses: every zero value falls back to its code default.
func (s ProbeSettings) Effective() ProbeSettings {
	d := DefaultProbeSettings()
	return ProbeSettings{
		HourlyBudget: cmpOr(s.HourlyBudget, d.HourlyBudget),
		Concurrency:  cmpOr(s.Concurrency, d.Concurrency),
		VerifyEvery:  cmpOr(s.VerifyEvery, d.VerifyEvery),
		VerifyMaxAge: cmpOr(s.VerifyMaxAge, d.VerifyMaxAge),
		VerifyRetry:  cmpOr(s.VerifyRetry, d.VerifyRetry),
		ProbeBase:    cmpOr(s.ProbeBase, d.ProbeBase),
	}
}

// Check reports the first value outside its bounds, and the relations between the intervals.
func (s ProbeSettings) Check() error {
	for _, b := range []struct {
		field           string
		value, min, max int64
	}{
		{"hourly_budget", int64(s.HourlyBudget), 1, maxHourlyBudget},
		{"concurrency", int64(s.Concurrency), 1, maxProbeConcurrency},
	} {
		if b.value != 0 && (b.value < b.min || b.value > b.max) {
			return &SettingError{b.field, fmt.Sprintf("must be from %d to %d", b.min, b.max)}
		}
	}
	for _, b := range []struct {
		field           string
		value, min, max time.Duration
	}{
		{"verify_every_seconds", s.VerifyEvery, minVerifyInterval, maxVerifyInterval},
		{"verify_max_age_seconds", s.VerifyMaxAge, minVerifyInterval, maxVerifyInterval},
		{"verify_retry_seconds", s.VerifyRetry, minVerifyInterval, maxVerifyRetry},
		{"probe_base_seconds", s.ProbeBase, time.Second, maxProbeBase},
	} {
		if b.value != 0 && (b.value < b.min || b.value > b.max) {
			return &SettingError{b.field, fmt.Sprintf("must be from %d to %d seconds", b.min/time.Second, b.max/time.Second)}
		}
	}
	e := s.Effective()
	if e.VerifyMaxAge < e.VerifyEvery {
		return &SettingError{"verify_max_age_seconds", "must be at least verify_every_seconds"}
	}
	if e.VerifyRetry > e.VerifyEvery {
		return &SettingError{"verify_retry_seconds", "must be at most verify_every_seconds"}
	}
	return nil
}

// SettingError is an administrator's value outside its bounds. Field is the setting's name in the admin API.
type SettingError struct{ Field, Reason string }

func (e *SettingError) Error() string { return "quality: " + e.Field + " " + e.Reason }

func cmpOr[T int | time.Duration](v, fallback T) T {
	if v == 0 {
		return fallback
	}
	return v
}

// currentSettings is the value in force. The loop writes it, and the monitoring path reads ProbeBase and the verify
// age without its lock, so the value is immutable and swapped by pointer.
func (l *Loop) currentSettings() ProbeSettings {
	if s := l.settings.Load(); s != nil {
		return *s
	}
	return DefaultProbeSettings()
}

func (l *Loop) setSettings(s ProbeSettings) {
	old := l.currentSettings()
	l.settings.Store(&s)
	l.monitor.verifyAge.Store(int64(s.VerifyMaxAge))
	if old.VerifyEvery != s.VerifyEvery || old.VerifyRetry != s.VerifyRetry {
		l.rescheduleVerifications(s)
	}
}

// rescheduleVerifications rebuilds the next check from the last check's time and outcome after the administrator
// changes the interval or the first retry. It keeps the jitter and leaves an in-flight check alone: that check
// schedules itself with the value in force when it finishes.
func (l *Loop) rescheduleVerifications(s ProbeSettings) {
	l.monitor.mu.Lock()
	defer l.monitor.mu.Unlock()
	for _, e := range l.monitor.endpoints {
		e.mu.Lock()
		if !e.verifying {
			switch e.verify.outcome {
			case verifyPassed:
				e.verify.next = e.verify.checked.Add(l.spread(s.VerifyEvery))
			case verifyContradicted:
				e.verify.next = e.verify.checked.Add(l.spread(backoff(e.verify.failures, s.VerifyRetry, s.VerifyEvery)))
			case verifyUnknown:
				at := e.verify.attempted
				if at.IsZero() {
					at = e.verify.checked
				}
				if !at.IsZero() {
					e.verify.next = at.Add(l.spread(s.VerifyRetry))
				}
			}
		}
		e.mu.Unlock()
	}
}

// loadSettings reads the record at startup; a value this node cannot run fails the start rather than being replaced
// silently (docs/domains/quality.md「试探」).
func (l *Loop) loadSettings(ctx context.Context, now time.Time) error {
	if l.db == nil {
		l.setSettings(DefaultProbeSettings())
		l.settingsAt = now
		return nil
	}
	s, err := readSettings(ctx, l.db, l.scope)
	if err != nil {
		return fmt.Errorf("quality: read the probe settings: %w", err)
	}
	if err := s.Check(); err != nil {
		return fmt.Errorf("quality: the saved probe settings are not usable: %w", err)
	}
	l.setSettings(s.Effective())
	l.settingsAt = now
	return nil
}

// refreshSettings rereads the record every settingsEvery. A read that fails or a record that is not usable keeps the
// previous value and is logged: the node that saved it checked it, and a reload must not stop probes.
func (l *Loop) refreshSettings(ctx context.Context, now time.Time) {
	if l.db == nil || now.Sub(l.settingsAt) < settingsEvery {
		return
	}
	l.settingsAt = now
	s, err := readSettings(ctx, l.db, l.scope)
	if err == nil {
		s = s.Effective()
		err = s.Check()
	}
	if err != nil {
		l.logger.Error("quality: reload the probe settings; the previous value stays in force", "err", err)
		return
	}
	l.setSettings(s)
}

func readSettings(ctx context.Context, db *pg.DB, scope pg.Scope) (ProbeSettings, error) {
	var s ProbeSettings
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		var err error
		s, err = ReadProbeSettings(ctx, tx)
		return err
	})
	return s, err
}

// ReadProbeSettings reads the administrator's record; no row is all defaults.
func ReadProbeSettings(ctx context.Context, tx pg.Tx) (ProbeSettings, error) {
	var budget, concurrency, every, maxAge, retry, base *int64
	err := tx.QueryRow(ctx, `SELECT hourly_budget,concurrency,verify_every_seconds,verify_max_age_seconds,
		verify_retry_seconds,probe_base_seconds FROM quality.probe_settings WHERE singleton`).
		Scan(&budget, &concurrency, &every, &maxAge, &retry, &base)
	if errors.Is(err, pg.ErrNoRows) {
		return ProbeSettings{}, nil
	}
	if err != nil {
		return ProbeSettings{}, err
	}
	var s ProbeSettings
	if budget != nil {
		s.HourlyBudget = int(*budget)
	}
	if concurrency != nil {
		s.Concurrency = int(*concurrency)
	}
	if every != nil {
		s.VerifyEvery = time.Duration(*every) * time.Second
	}
	if maxAge != nil {
		s.VerifyMaxAge = time.Duration(*maxAge) * time.Second
	}
	if retry != nil {
		s.VerifyRetry = time.Duration(*retry) * time.Second
	}
	if base != nil {
		s.ProbeBase = time.Duration(*base) * time.Second
	}
	return s, nil
}

// SetProbeSettings validates and replaces the record and its audit entry in one turn. A record with no value set is
// deleted, so the defaults stay the only source of what is not deliberately different.
func SetProbeSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, s ProbeSettings, reason string) error {
	if err := s.Check(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('quality.probe_settings'))`); err != nil {
		return err
	}
	before, err := ReadProbeSettings(ctx, tx)
	if err != nil {
		return err
	}
	if s == (ProbeSettings{}) {
		_, err = tx.Exec(ctx, `DELETE FROM quality.probe_settings`)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO quality.probe_settings (singleton,hourly_budget,concurrency,
			verify_every_seconds,verify_max_age_seconds,verify_retry_seconds,probe_base_seconds)
			VALUES (true,$1,$2,$3,$4,$5,$6)
			ON CONFLICT (singleton) DO UPDATE SET hourly_budget=$1,concurrency=$2,verify_every_seconds=$3,
			verify_max_age_seconds=$4,verify_retry_seconds=$5,probe_base_seconds=$6`,
			nullableInt(s.HourlyBudget), nullableInt(s.Concurrency), nullableSeconds(s.VerifyEvery),
			nullableSeconds(s.VerifyMaxAge), nullableSeconds(s.VerifyRetry), nullableSeconds(s.ProbeBase))
	}
	if err != nil {
		return fmt.Errorf("quality: write the probe settings: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "probe_settings.update", Reason: reason,
		Target: audit.Target{Module: "quality", Kind: "probe_settings", ID: uuid.Nil().String()},
		Detail: map[string]any{"before": probeDetail(before), "after": probeDetail(s)},
	})
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableSeconds(v time.Duration) any {
	if v == 0 {
		return nil
	}
	return int64(v / time.Second)
}

func probeDetail(s ProbeSettings) map[string]any {
	return map[string]any{
		"hourly_budget": nullableInt(s.HourlyBudget), "concurrency": nullableInt(s.Concurrency),
		"verify_every_seconds": nullableSeconds(s.VerifyEvery), "verify_max_age_seconds": nullableSeconds(s.VerifyMaxAge),
		"verify_retry_seconds": nullableSeconds(s.VerifyRetry), "probe_base_seconds": nullableSeconds(s.ProbeBase),
	}
}

// probeBudget is the probes started in the last hour, oldest first. It is not a fixed ring: an administrator may
// shrink the hourly budget below what the last hour already used, and the queue then refuses new probes until enough
// of the old ones age out. Only Tick touches it.
type probeBudget struct {
	at   []time.Time
	head int
}

func (b *probeBudget) spend(now time.Time) { b.at = append(b.at, now) }

// spent reports whether limit probes were started in the hour ending at now.
func (b *probeBudget) spent(now time.Time, limit int) bool {
	cutoff := now.Add(-time.Hour)
	for b.head < len(b.at) && !b.at[b.head].After(cutoff) {
		b.head++
	}
	if b.head >= 1024 && b.head*2 >= len(b.at) {
		b.at = append(b.at[:0], b.at[b.head:]...)
		b.head = 0
	}
	return b.count() >= limit
}

func (b *probeBudget) count() int { return len(b.at) - b.head }
