package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The administrator's segment settings (docs/domains/billing.md「预留与结算」). The defaults are code: an administrator
// records only the departures, and a node reloads the record within settingsEvery so a save needs no restart.
const (
	defaultSegmentOutput    int64 = 1000
	defaultSegmentOvershoot       = 2 * time.Second
	maxSegmentOutput        int64 = 1_000_000
	maxSegmentOvershoot           = 10 * time.Minute
	settingsEvery                 = 30 * time.Second
)

// DefaultSegments are the settings that apply where the administrator recorded none.
func DefaultSegments() Segments {
	return Segments{Output: defaultSegmentOutput, Overshoot: defaultSegmentOvershoot}
}

// SettingError is an administrator's value outside its bounds. Field is the setting's name in the admin API.
type SettingError struct{ Field, Reason string }

func (e *SettingError) Error() string { return "billing: " + e.Field + " " + e.Reason }

// SegmentSettings is the administrator's record: nil is the default in code.
type SegmentSettings struct {
	Output    *int64
	Overshoot *time.Duration
}

// Effective is the settings a node uses: each unset value falls back to its code default.
func (s SegmentSettings) Effective() Segments {
	d := DefaultSegments()
	return Segments{Output: cmpOrPtr(s.Output, d.Output), Overshoot: cmpOrPtr(s.Overshoot, d.Overshoot)}
}

// Check reports the first value outside its bounds. minOvershoot is the node's extension wait: the allowance must cover
// it, or a request could wait for an extension longer than its reservation covers (internal/gateway/README.md I1).
func (s SegmentSettings) Check(minOvershoot time.Duration) error {
	if s.Output != nil && (*s.Output < 1 || *s.Output > maxSegmentOutput) {
		return &SettingError{"output_tokens", fmt.Sprintf("must be from 1 to %d", maxSegmentOutput)}
	}
	floor := max(time.Second, minOvershoot)
	if s.Overshoot != nil && *s.Overshoot%time.Second != 0 {
		return &SettingError{"overshoot_seconds", "must be whole seconds"}
	}
	if s.Overshoot != nil && (*s.Overshoot < floor || *s.Overshoot > maxSegmentOvershoot) {
		return &SettingError{"overshoot_seconds", fmt.Sprintf("must be from %d to %d seconds", floor/time.Second, maxSegmentOvershoot/time.Second)}
	}
	if s.Effective().Overshoot < minOvershoot {
		return &SettingError{"overshoot_seconds", fmt.Sprintf("must be at least the extension wait of %d seconds", minOvershoot/time.Second)}
	}
	return nil
}

func cmpOrPtr[T int64 | time.Duration](v *T, fallback T) T {
	if v == nil {
		return fallback
	}
	return *v
}

// SegmentPolicy is the one live segment setting a node uses: routing marks an entry segmentable by it and billing
// reserves by the same value, so a change reaches both together (#229).
type SegmentPolicy struct {
	v            atomic.Pointer[Segments]
	db           *pg.DB
	scope        pg.Scope
	minOvershoot time.Duration
	logger       *slog.Logger
}

// NewSegmentPolicy makes a policy that does not read the database: the value given applies until it is replaced.
func NewSegmentPolicy(s Segments) *SegmentPolicy {
	p := &SegmentPolicy{}
	p.v.Store(&s)
	return p
}

// LoadSegmentPolicy reads the administrator's record and starts from the value in force. A record this node cannot
// use (its overshoot is shorter than this node's extension wait) fails the start rather than reserving by a value that
// cannot cover a cut (docs/domains/billing.md「预留与结算」).
func LoadSegmentPolicy(ctx context.Context, db *pg.DB, scope pg.Scope, minOvershoot time.Duration, logger *slog.Logger) (*SegmentPolicy, error) {
	if logger == nil {
		logger = slog.Default()
	}
	p := &SegmentPolicy{db: db, scope: scope, minOvershoot: minOvershoot, logger: logger}
	if err := p.reload(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// Load is the settings in force.
func (p *SegmentPolicy) Load() Segments { return *p.v.Load() }

// check refuses a value outside its bounds: the policy a node starts with must be one it could have saved.
func (p *SegmentPolicy) check() error {
	s := p.Load()
	if s.Output < 1 || s.Output > maxSegmentOutput {
		return &SettingError{"output_tokens", fmt.Sprintf("must be from 1 to %d", maxSegmentOutput)}
	}
	floor := max(time.Second, p.minOvershoot)
	if s.Overshoot < floor || s.Overshoot > maxSegmentOvershoot {
		return &SettingError{"overshoot_seconds", fmt.Sprintf("must be from %d to %d seconds", floor/time.Second, maxSegmentOvershoot/time.Second)}
	}
	return nil
}

// watch rereads the record every settingsEvery until ctx ends. A failed read or a value this node cannot use keeps the
// previous value and is logged; the save that wrote it was validated by the node that served it.
func (p *SegmentPolicy) watch(ctx context.Context) {
	if p.db == nil {
		return
	}
	ticker := time.NewTicker(settingsEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.reload(ctx); err != nil && ctx.Err() == nil {
				p.logger.Error("billing: reload the segment settings; the previous value stays in force", "err", err)
			}
		}
	}
}

func (p *SegmentPolicy) reload(ctx context.Context) error {
	var settings SegmentSettings
	if err := p.db.ReadTx(ctx, p.scope, func(tx pg.Tx) error {
		var err error
		settings, err = ReadSegmentSettings(ctx, tx)
		return err
	}); err != nil {
		return fmt.Errorf("billing: read the segment settings: %w", err)
	}
	if err := settings.Check(p.minOvershoot); err != nil {
		return fmt.Errorf("billing: the saved segment settings are not usable on this node: %w", err)
	}
	s := settings.Effective()
	p.v.Store(&s)
	return p.check()
}

// ReadSegmentSettings reads the administrator's record; ErrNotFound-free: no row is all defaults.
func ReadSegmentSettings(ctx context.Context, tx pg.Tx) (SegmentSettings, error) {
	var output, overshoot *int64
	err := tx.QueryRow(ctx, `SELECT output_tokens,overshoot_seconds FROM billing.segment_settings WHERE singleton`).Scan(&output, &overshoot)
	if errors.Is(err, pg.ErrNoRows) {
		return SegmentSettings{}, nil
	}
	if err != nil {
		return SegmentSettings{}, err
	}
	var s SegmentSettings
	if output != nil {
		s.Output = output
	}
	if overshoot != nil {
		d := time.Duration(*overshoot) * time.Second
		s.Overshoot = &d
	}
	return s, nil
}

// SetSegmentSettings validates and replaces the record and its audit entry in one turn. A record with neither value
// set is deleted, so the defaults stay the only source of what is not deliberately different.
func SetSegmentSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, s SegmentSettings, minOvershoot time.Duration, reason string) error {
	if err := s.Check(minOvershoot); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing.segment_settings'))`); err != nil {
		return err
	}
	before, err := ReadSegmentSettings(ctx, tx)
	if err != nil {
		return err
	}
	var overshoot *int64
	if s.Overshoot != nil {
		n := int64(*s.Overshoot / time.Second)
		overshoot = &n
	}
	if s.Output == nil && s.Overshoot == nil {
		_, err = tx.Exec(ctx, `DELETE FROM billing.segment_settings`)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO billing.segment_settings (singleton,output_tokens,overshoot_seconds) VALUES (true,$1,$2)
			ON CONFLICT (singleton) DO UPDATE SET output_tokens=$1,overshoot_seconds=$2`, s.Output, overshoot)
	}
	if err != nil {
		return fmt.Errorf("billing: write the segment settings: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "segment_settings.update", Reason: reason,
		Target: audit.Target{Module: "billing", Kind: "segment_settings", ID: uuid.Nil().String()},
		Detail: map[string]any{"before": segmentDetail(before), "after": segmentDetail(s)},
	})
}

func segmentDetail(s SegmentSettings) map[string]any {
	var overshoot any
	if s.Overshoot != nil {
		overshoot = int64(*s.Overshoot / time.Second)
	}
	return map[string]any{"output_tokens": s.Output, "overshoot_seconds": overshoot}
}
