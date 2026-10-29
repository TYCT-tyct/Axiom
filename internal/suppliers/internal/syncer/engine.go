package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
)

// ErrFenced is the Store's answer to a Commit that names an older version than the one stored: another writer got
// there first, and nothing of this step is kept.
var ErrFenced = errors.New("syncer: the state changed since it was read")

// Store is where a supplier's state lives. Commit applies a step whole or not at all: the state, its events, the cost
// plans and the charge records in one transaction, and only when the stored version is expect. Charges are upserted by
// their Ref, so a record read twice is one row.
type Store interface {
	Load(ctx context.Context) (State, error)
	Commit(ctx context.Context, expect int64, c Commit) error
}

// Settings are the engine's numbers. They are data in the module (the intervals are a back-office setting); Defaults
// holds the values of the design.
type Settings struct {
	Interval    [numTasks]time.Duration
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// Jitter spreads a backoff so suppliers that failed together do not retry together; nil means none.
	Jitter func(time.Duration) time.Duration
	// MaxPages bounds the pages one round reads; the pass continues in the next.
	MaxPages int
	// LowBalance is the balance under which BalanceLow is sent.
	LowBalance adapter.Amount
}

// Defaults are the design's values.
func Defaults() Settings {
	return Settings{
		Interval:    [numTasks]time.Duration{time.Minute, 5 * time.Minute, 5 * time.Minute, 2 * time.Minute},
		BackoffBase: time.Minute, BackoffMax: 30 * time.Minute,
		MaxPages:   50,
		LowBalance: "5",
	}
}

// Engine runs the rounds of one supplier. It holds no state of the supplier between calls.
type Engine struct {
	store Store
	mgr   adapter.Manager
	set   Settings
	now   func() time.Time
	// held is, for each task that the supplier's limits held up and that has not gone on since, when it may.
	held map[Task]time.Time
}

// New makes the engine of one supplier. now is the clock; tests move it.
func New(store Store, mgr adapter.Manager, set Settings, now func() time.Time) *Engine {
	return &Engine{store: store, mgr: mgr, set: set, now: now}
}

// clone copies what a Commit may change, so that a step that fails leaves the state it started from.
func (s State) clone() State {
	s.Plans, s.Confirmed = maps.Clone(s.Plans), maps.Clone(s.Confirmed)
	s.Groups, s.Keys = slices.Clone(s.Groups), slices.Clone(s.Keys)
	return s
}

func fresh(st *State) Commit { return Commit{State: st.clone()} }

// commit stores c over *st and makes c's state the current one. The next version is the engine's to name.
func (e *Engine) commit(ctx context.Context, st *State, c *Commit) error {
	c.State.Version = st.Version + 1
	if err := e.store.Commit(ctx, st.Version, *c); err != nil {
		return err
	}
	*st = c.State
	*c = fresh(st)
	return nil
}

// Blocked is when the first of the tasks that the supplier's limits held up may go on, or the zero time if none is
// waiting. A task stays waiting over the Rounds until it succeeds or fails otherwise, so a caller that keeps the Manager
// can run the Round again at each time Blocked gives, for a Manager that keeps how far a listing got (adapter.Manager)
// only keeps it while it lives.
func (e *Engine) Blocked() (first time.Time) {
	for _, at := range e.held {
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first
}

// Round runs the tasks of the supplier that are due, in order. A task's failure is recorded in the state and does not
// stop the others, except an auth rejection, which ends the round and all polling: the error returned is the store's
// (including ErrFenced), never a task's.
func (e *Engine) Round(ctx context.Context) error {
	st, err := e.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("syncer: load: %w", err)
	}
	if st.Status != Normal {
		clear(e.held) // no task runs again, so none is waited for
	}
	for t := Task(0); t < numTasks && st.Status == Normal; t++ {
		if err := e.step(ctx, &st, t); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) step(ctx context.Context, st *State, t Task) error {
	ts, now := st.Tasks[t], e.now()
	if ts.Halted {
		delete(e.held, t)
	}
	if ts.Halted || now.Before(ts.Due) || now.Before(ts.BackoffUntil) {
		return nil
	}
	c := fresh(st)
	err := runners[t](e, ctx, st, &c)
	if err != nil {
		c = fresh(st) // a failed step writes nothing of what it had read
	}
	e.settle(&c, t, err, now)
	return e.commit(ctx, st, &c)
}

// runners are the tasks, in the order of Task. The charge scan reads st as well, because it commits page by page.
var runners = [numTasks]func(e *Engine, ctx context.Context, st *State, c *Commit) error{
	TaskBalance: func(e *Engine, ctx context.Context, _ *State, c *Commit) error { return e.balance(ctx, c) },
	TaskCatalog: func(e *Engine, ctx context.Context, _ *State, c *Commit) error { return e.catalog(ctx, c) },
	TaskKeys:    func(e *Engine, ctx context.Context, _ *State, c *Commit) error { return e.keys(ctx, c) },
	TaskCharges: (*Engine).scan,
}

// settle records how a task went: its next due time, or the failure and what it means.
func (e *Engine) settle(c *Commit, t Task, err error, now time.Time) {
	ts := &c.State.Tasks[t]
	delete(e.held, t)
	switch {
	case err == nil:
		*ts = TaskState{Due: now.Add(e.set.Interval[t]), LastOK: now}
		if t == TaskCharges && c.State.ScanOpen {
			ts.Due = now // the pass is not finished: the next round goes on with it
		}
	case errors.Is(err, adapter.ErrAuthRejected):
		c.State.Status, c.State.Reason = NeedsAttention, "the supplier rejected the account credentials"
		c.Events = append(c.Events, Event{Kind: NeedsAttentionEvent, Old: c.State.Tasks[TaskBalance].LastOK.Format(time.RFC3339Nano), Why: c.State.Reason})
	case errors.Is(err, adapter.ErrBucketsFull):
		ts.Due = now.Add(time.Minute) // not a failure: the supplier's limits are used up for now
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			ts.Due = blocked.Until
		}
		if e.held == nil {
			e.held = map[Task]time.Time{}
		}
		e.held[t] = ts.Due
	case errors.Is(err, adapter.ErrUnrecognised):
		ts.Halted = true
		c.Events = append(c.Events, Event{Kind: TaskHalted, Subject: t.String(), Old: ts.LastOK.Format(time.RFC3339Nano), Why: "the response is not in a form the adapter knows"})
	default:
		ts.Failures++
		ts.BackoffUntil = now.Add(e.backoff(ts.Failures))
	}
}

// backoff doubles from BackoffBase up to BackoffMax.
func (e *Engine) backoff(failures int) time.Duration {
	d := e.set.BackoffMax
	if shift := failures - 1; shift < 30 && e.set.BackoffBase<<shift < d {
		d = e.set.BackoffBase << shift
	}
	if e.set.Jitter != nil {
		d = e.set.Jitter(d)
	}
	return d
}

func rat(a adapter.Amount) *big.Rat {
	r, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return new(big.Rat)
	}
	return r
}

func (e *Engine) balance(ctx context.Context, c *Commit) error {
	acc, err := e.mgr.Account(ctx)
	if err != nil {
		return err
	}
	s := &c.State
	if !s.BalanceAt.IsZero() { // what changed since the last read; the first read changes nothing
		if s.Funding != acc.Funding {
			c.Events = append(c.Events, Event{Kind: FundingChanged, Old: fundingName(s.Funding), New: fundingName(acc.Funding)})
		}
		if was, now := rat(s.Balance).Sign() > 0, rat(acc.Balance).Sign() > 0; was != now {
			c.Events = append(c.Events, Event{Kind: BalanceCrossed, Old: string(s.Balance), New: string(acc.Balance), Why: map[bool]string{true: "above", false: "below"}[now]})
		}
	}
	s.Balance, s.Funding, s.BalanceAt = acc.Balance, acc.Funding, e.now()
	switch low := rat(acc.Balance).Cmp(rat(e.set.LowBalance)) < 0; {
	case low && !s.LowNotified:
		s.LowNotified = true
		c.Events = append(c.Events, Event{Kind: BalanceLow, New: string(acc.Balance)})
	case !low && s.LowNotified:
		s.LowNotified = false
		c.Events = append(c.Events, Event{Kind: BalanceRecovered, New: string(acc.Balance)})
	}
	return nil
}

func fundingName(f adapter.Funding) string {
	if f == adapter.FundingWallet {
		return "wallet"
	}
	return "other"
}

func (e *Engine) keys(ctx context.Context, c *Commit) error {
	keys, err := e.mgr.Keys(ctx)
	if err != nil {
		return err
	}
	old := make(map[adapter.KeyRef]adapter.KeyState, len(c.State.Keys))
	for _, k := range c.State.Keys {
		old[k.Ref] = k
	}
	for _, k := range keys {
		if prev, ok := old[k.Ref]; !ok || prev.Status != k.Status {
			c.Events = append(c.Events, Event{Kind: KeyChanged, Subject: string(k.Ref), Old: statusName(ok, prev.Status), New: statusName(true, k.Status)})
		}
		delete(old, k.Ref)
	}
	for _, gone := range slices.Sorted(maps.Keys(old)) {
		c.Events = append(c.Events, Event{Kind: KeyChanged, Subject: string(gone), Old: statusName(true, old[gone].Status)})
	}
	slices.SortFunc(keys, func(a, b adapter.KeyState) int { return cmpStr(string(a.Ref), string(b.Ref)) })
	c.State.Keys = keys
	return nil
}

func statusName(present bool, s adapter.KeyStatus) string {
	if !present {
		return ""
	}
	return [...]string{"active", "disabled", "quota_exhausted", "expired"}[s]
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// catalog reads the offer, compares it with the one before, and writes the differences as events, with a new version of
// each cost plan that changed. The same answer twice changes nothing.
func (e *Engine) catalog(ctx context.Context, c *Commit) error {
	offer, err := e.mgr.Offer(ctx)
	if err != nil {
		return err
	}
	prev := make(map[string]GroupSnap, len(c.State.Groups))
	for _, g := range c.State.Groups {
		prev[g.Ref] = g
	}
	plans := map[string]string{}
	var groups []GroupSnap
	for _, g := range offer.Groups {
		snap, err := group(c, g, prev[g.Ref], plans)
		if err != nil {
			return err
		}
		groups = append(groups, snap)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Ref < groups[j].Ref })
	diffGroups(c, prev, groups)
	diffPlans(c, plans)
	c.State.Groups, c.State.Plans = groups, plans
	return nil
}

func group(c *Commit, g adapter.Group, before GroupSnap, plans map[string]string) (GroupSnap, error) {
	snap := GroupSnap{Ref: g.Ref, Name: g.Name, Ratio: string(g.Ratio), Facts: string(g.Facts)}
	if g.ModelsUnread {
		// Nothing was read: the models stay as they were, and only a confirmed base follows a changed ratio.
		for _, m := range before.Models {
			plan, err := plan(c, g, adapter.Model{UpstreamName: m.Name}, m.Plan, plans)
			if err != nil {
				return snap, err
			}
			snap.Models = append(snap.Models, ModelSnap{Name: m.Name, Formats: m.Formats, Without: m.Without, Plan: plan})
		}
		return snap, nil
	}
	for _, m := range g.Models {
		plan, err := plan(c, g, m, "", plans)
		if err != nil {
			return snap, err
		}
		formats := make([]int, len(m.Formats))
		for i, f := range m.Formats {
			formats[i] = int(f)
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.UpstreamName, Formats: formats, Without: m.Without, Plan: plan})
	}
	sort.Slice(snap.Models, func(i, j int) bool { return snap.Models[i].Name < snap.Models[j].Name })
	return snap, nil
}

// plan decides the cost plan of one endpoint and returns its fingerprint, or "" when the cost is unknown: a schedule
// the supplier published, else a person's confirmed base scaled by the group's ratio, else nothing. kept is the
// fingerprint to keep when nothing new can be worked out (a model that was not read this time).
func plan(c *Commit, g adapter.Group, m adapter.Model, kept string, plans map[string]string) (string, error) {
	key := PlanKey(g.Ref, m.UpstreamName)
	var schedule catalog.Schedule
	var basis string
	switch base, confirmed := c.State.Confirmed[m.UpstreamName]; {
	case m.Cost != nil:
		schedule, basis = *m.Cost, "published"
	case confirmed:
		parsed, err := catalog.ParseSchedule([]byte(base))
		if err != nil {
			return "", fmt.Errorf("syncer: the confirmed base of %s: %w", m.UpstreamName, err)
		}
		if schedule, err = adapter.ScaleSchedule(parsed, g.Ratio); err != nil {
			return "", err
		}
		basis = "confirmed"
	default:
		if kept != "" {
			plans[key] = kept
		}
		return kept, nil
	}
	b, err := schedule.MarshalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	fp := hex.EncodeToString(sum[:8])
	plans[key] = fp
	if c.State.Plans[key] != fp {
		c.Plans = append(c.Plans, Plan{Endpoint: key, Fingerprint: fp, Schedule: string(b), Basis: basis})
	}
	return fp, nil
}

func diffGroups(c *Commit, prev map[string]GroupSnap, now []GroupSnap) {
	seen := map[string]bool{}
	for _, g := range now {
		seen[g.Ref] = true
		before, ok := prev[g.Ref]
		if !ok {
			c.Events = append(c.Events, Event{Kind: GroupAdded, Subject: g.Ref, New: g.Name})
		} else {
			if before.Ratio != g.Ratio {
				c.Events = append(c.Events, Event{Kind: RatioChanged, Subject: g.Ref, Old: before.Ratio, New: g.Ratio})
			}
			if before.Facts != g.Facts {
				c.Events = append(c.Events, Event{Kind: GroupChanged, Subject: g.Ref})
			}
		}
		diffModels(c, g, before)
	}
	for _, ref := range slices.Sorted(maps.Keys(prev)) {
		if !seen[ref] {
			c.Events = append(c.Events, Event{Kind: GroupRemoved, Subject: ref})
		}
	}
}

// diffModels sends ModelAdded and ModelRemoved for the models of g against those of before, and ModelChanged for one
// whose formats or capability limits differ.
func diffModels(c *Commit, g, before GroupSnap) {
	had := map[string]ModelSnap{}
	for _, m := range before.Models {
		had[m.Name] = m
	}
	for _, m := range g.Models {
		old, ok := had[m.Name]
		switch {
		case !ok:
			c.Events = append(c.Events, Event{Kind: ModelAdded, Subject: g.Ref + "/" + m.Name})
		case !slices.Equal(old.Formats, m.Formats) || old.Without != m.Without:
			c.Events = append(c.Events, Event{Kind: ModelChanged, Subject: g.Ref + "/" + m.Name})
		}
		delete(had, m.Name)
	}
	for _, name := range slices.Sorted(maps.Keys(had)) {
		c.Events = append(c.Events, Event{Kind: ModelRemoved, Subject: g.Ref + "/" + name})
	}
}

// diffPlans sends PlanChanged for each endpoint whose plan differs from the one before, including one that has lost
// its plan and so its place in routing.
func diffPlans(c *Commit, now map[string]string) {
	for _, key := range slices.Sorted(maps.Keys(now)) {
		if old := c.State.Plans[key]; old != now[key] {
			c.Events = append(c.Events, Event{Kind: PlanChanged, Subject: key, Old: old, New: now[key]})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.State.Plans)) {
		if _, ok := now[key]; !ok {
			c.Events = append(c.Events, Event{Kind: PlanChanged, Subject: key, Old: c.State.Plans[key]})
		}
	}
}
