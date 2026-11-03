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
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
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

// KeyStore is the optional half of a Store that lets the sync onboard a supplier's groups by itself. A Store without it
// keeps the keys a person bound, and makes none.
type KeyStore interface {
	// KeysToMake lists the groups that need a key of ours and have none, with the name the key is made under.
	KeysToMake(ctx context.Context) ([]KeyTarget, error)
	// KeysToBind lists the credentials a person bound by reference that the sync has not read the text of yet, and
	// whose group is not waiting out a failure.
	KeysToBind(ctx context.Context, now time.Time) ([]BindTarget, error)
}

// KeyTarget is a group the sync makes a key for, and the name that marks the key as ours.
type KeyTarget struct {
	GroupRef string
	Name     string
}

// BindTarget is a credential waiting for its key's text.
type BindTarget struct {
	ID       uuid.UUID
	Ref      adapter.KeyRef
	GroupRef string
}

// Settings are the engine's numbers. They are data in the module (the intervals are a back-office setting); Defaults
// holds the values of the design.
type Settings struct {
	Interval    [numTasks]time.Duration
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// Jitter spreads a backoff so suppliers that failed together do not retry together; nil means none.
	Jitter func(time.Duration) time.Duration
	// MaxPages bounds the pages of charge records one round reads; the pass continues in the next.
	MaxPages int
	// LowBalance is the balance under which BalanceLow is sent.
	LowBalance adapter.Amount
	// KeyRetry is how long a group waits after its key could not be made or read for a reason the login or the group
	// has to fix.
	KeyRetry time.Duration
	// DirectFormats lets the state keep embeddings, rerank and legacy completions. While it is false the state holds
	// only the formats a
	// pre-P18 node can name: such a node may load this state for any task and project it, and a name it does not know
	// fails its whole step.
	DirectFormats bool
}

// Defaults are the design's values.
func Defaults() Settings {
	return Settings{
		Interval:    [numTasks]time.Duration{time.Minute, 5 * time.Minute, 5 * time.Minute, 2 * time.Minute},
		BackoffBase: time.Minute, BackoffMax: 30 * time.Minute,
		MaxPages:   50,
		LowBalance: "5",
		KeyRetry:   time.Hour,
	}
}

// Engine runs the rounds of one supplier. It holds no state of the supplier between calls.
type Engine struct {
	store Store
	mgr   adapter.Manager
	set   Settings
	now   func() time.Time
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

// Round runs the tasks of the supplier that are due, in order. A task's failure is recorded in the state and does not
// stop the others, except an auth rejection, which ends the round and all polling: the error returned is the store's
// (including ErrFenced), never a task's.
func (e *Engine) Round(ctx context.Context) error {
	st, err := e.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("syncer: load: %w", err)
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
	if ts.Halted || now.Before(ts.Due) || now.Before(ts.BackoffUntil) {
		return nil
	}
	c := fresh(st)
	err := runners[t](e, ctx, st, &c)
	if err != nil && !errors.Is(err, adapter.ErrBucketsFull) {
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
		// Not a failure: the supplier's limits are used up for now, and the next round goes on then. A listing keeps
		// its position, so this ends the round instead of holding a worker until they reopen.
		ts.Due = now.Add(time.Minute)
		if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
			ts.Due = blocked.Until
		}
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
	observed := e.now() // a read begun before a refusal cannot clear that refusal when it eventually returns
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
	s.Balance, s.Funding, s.BalanceAt = acc.Balance, acc.Funding, observed
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

// keys reads the keys listing. A read is stored with the position it reached, also when the supplier's limits stopped
// it, so the next round goes on from there; the read that ends the listing holds every key and every name, and what
// changed since the last complete one is an event.
func (e *Engine) keys(ctx context.Context, c *Commit) error {
	page, err := e.mgr.Keys(ctx, c.State.KeysAt)
	if err != nil {
		return err
	}
	c.State.KeysAt = page.Next
	if !page.Done {
		return adapter.Blocked{Until: page.Until}
	}
	keys := page.Items
	old := make(map[adapter.KeyRef]adapter.KeyState, len(c.State.Keys))
	for _, k := range c.State.Keys {
		old[k.Ref] = k
	}
	// Advance the state before onboarding. If a later key read is limited, step keeps this commit; the event and the
	// state that explains it must land together, or the next complete listing emits the same event again.
	c.State.Keys = stateKeys(keys)
	for _, k := range keys {
		if prev, ok := old[k.Ref]; !ok || prev.Status != k.Status {
			c.Events = append(c.Events, Event{Kind: KeyChanged, Subject: string(k.Ref), Old: statusName(ok, prev.Status), New: statusName(true, k.Status)})
		}
		delete(old, k.Ref)
	}
	for _, ref := range slices.Sorted(maps.Keys(old)) {
		c.Events = append(c.Events, Event{Kind: KeyChanged, Subject: string(ref), Old: statusName(true, old[ref].Status)})
	}
	// Set the policies before any key write: onboarding reads key texts one by one, and a later read may be stopped by
	// a bucket. The commit that keeps an earlier write must also carry the complete listing's policies, or the new
	// credential would keep its default (unrestricted) policy.
	c.KeyPolicies = &keys
	extra, err := e.onboard(ctx, c, &keys)
	if err != nil {
		// A made or bound key can be written before a later read is limited. Keep those writes in the state too,
		// while the listing itself is already there for the next round's event comparison.
		c.State.Keys = stateKeys(append(slices.Clone(keys), writtenKeys(c)...))
		return err
	}
	c.State.Keys = stateKeys(append(slices.Clone(keys), extra...))
	return nil
}

// stateKeys is the part of a complete listing the strict state keeps: no names and no policies, sorted by reference,
// and one entry per reference.
func stateKeys(keys []adapter.KeyState) []adapter.KeyState {
	seen := make(map[adapter.KeyRef]bool, len(keys))
	out := make([]adapter.KeyState, 0, len(keys))
	for _, k := range keys {
		if seen[k.Ref] {
			continue
		}
		seen[k.Ref] = true
		k.Name, k.Policy = "", nil
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b adapter.KeyState) int { return cmpStr(string(a.Ref), string(b.Ref)) })
	return out
}

// writtenKeys turns the keys whose text was read before a blocked step into state entries. A made key is normally
// already appended to the listing, but a bound key the listing does not have is not.
func writtenKeys(c *Commit) []adapter.KeyState {
	out := make([]adapter.KeyState, 0, len(c.KeyWrites.Made)+len(c.KeyWrites.Bound))
	for _, m := range c.KeyWrites.Made {
		out = append(out, adapter.KeyState{Ref: m.Ref, GroupRef: m.GroupRef, Status: m.Status})
	}
	for _, b := range c.KeyWrites.Bound {
		out = append(out, adapter.KeyState{Ref: b.Ref, GroupRef: b.GroupRef, Status: adapter.KeyActive})
	}
	return out
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
// each cost plan that changed. The same answer twice changes nothing. Like the keys, a read is stored with the position
// it reached, so a listing the supplier's limits stopped goes on in the next round.
func (e *Engine) catalog(ctx context.Context, c *Commit) error {
	page, err := e.mgr.Offer(ctx, c.State.OfferAt)
	if err != nil {
		return err
	}
	c.State.OfferAt = page.Next
	if !page.Done {
		return adapter.Blocked{Until: page.Until}
	}
	prev := make(map[string]GroupSnap, len(c.State.Groups))
	for _, g := range c.State.Groups {
		prev[g.Ref] = g
	}
	plans := map[string]string{}
	var groups []GroupSnap
	for _, g := range page.Items {
		snap, err := group(c, g, prev[g.Ref], plans, e.set.DirectFormats)
		if err != nil {
			return err
		}
		groups = append(groups, snap)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Ref < groups[j].Ref })
	diffGroups(c, prev, groups)
	diffPlans(c, plans)
	c.State.Groups, c.State.Plans = groups, plans
	c.OfferRead = true
	if err := e.pool(ctx, c); err != nil && !errors.Is(err, adapter.ErrNotPool) {
		return err
	}
	return nil
}

// group is the snapshot of one group the listing read. Unless direct is set, a model keeps only the formats a pre-P18
// node can name, and a model the gate leaves with none is not listed: the formats column needs at least one.
func group(c *Commit, g adapter.Group, before GroupSnap, plans map[string]string, direct bool) (GroupSnap, error) {
	snap := GroupSnap{Ref: g.Ref, Name: g.Name, Ratio: string(g.Ratio), Facts: string(g.Facts)}
	if g.ModelsUnread {
		return keptGroup(c, g, snap, before, plans, direct)
	}
	for _, m := range g.Models {
		reported := make([]int, len(m.Formats))
		for i, f := range m.Formats {
			reported[i] = int(f)
		}
		formats, held := storable(reported, direct)
		if held {
			continue
		}
		plan, err := plan(c, g, m, "", plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.UpstreamName, Formats: formats, Without: m.Without, Plan: plan})
	}
	sort.Slice(snap.Models, func(i, j int) bool { return snap.Models[i].Name < snap.Models[j].Name })
	snap.KeyEligible = g.NeedsKey || hasPlan(snap.Models)
	return snap, nil
}

// hasPlan reports whether a model of the group can be sold with a known cost. A model without a call format is not sold,
// so its plan does not make the group one to make a key for.
func hasPlan(models []ModelSnap) bool {
	for _, m := range models {
		if m.Plan != "" && len(m.Formats) > 0 {
			return true
		}
	}
	return false
}

// keptGroup is the snapshot of a group whose models were not read: they stay as they were, behind the same gate, and
// only a confirmed base follows a changed ratio.
func keptGroup(c *Commit, g adapter.Group, snap, before GroupSnap, plans map[string]string, direct bool) (GroupSnap, error) {
	for _, m := range before.Models {
		formats, held := storable(m.Formats, direct)
		if held {
			continue
		}
		plan, err := plan(c, g, adapter.Model{UpstreamName: m.Name}, m.Plan, plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.Name, Formats: formats, Without: m.Without, Plan: plan})
	}
	snap.KeyEligible = g.NeedsKey || hasPlan(snap.Models)
	return snap, nil
}

// preP18Formats are the formats a node released before P18 can name. It names what that node knows rather than what is
// new, so a format added later is held back too until the gate opens.
var preP18Formats = []int{int(protocol.Messages), int(protocol.Responses), int(protocol.Chat)}

// storable is the formats the state may keep: all of them once the gate is open, else only those of preP18Formats.
// held reports a model the gate leaves with none, which is then not listed until it opens.
func storable(formats []int, direct bool) (kept []int, held bool) {
	if direct {
		return formats, false
	}
	kept = slices.DeleteFunc(slices.Clone(formats), func(f int) bool { return !slices.Contains(preP18Formats, f) })
	return kept, len(kept) == 0 && len(formats) > 0
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
