// Package syncer keeps what the module knows of a supplier up to date: it runs the supplier's due tasks in rounds, backs
// off on failures, stops at once when the supplier rejects the account, turns differences into events, says when an
// endpoint may be routed, and takes a supplier out step by step (internal/suppliers/README.md 「同步」).
//
// The engine does the deciding and keeps nothing itself: everything it knows is a State that a Store loads and commits
// whole, with the events and records the step produced, so a step either happened with its events or did not. The
// Store is the boundary where leases, the fence and the database transaction belong.
package syncer

import (
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Task is one kind of work done for a supplier. The order is the order they run in a round.
type Task uint8

// The tasks.
const (
	TaskBalance Task = iota // balance and where it is drawn from
	TaskCatalog             // groups, multipliers, models
	TaskKeys                // the state of our keys
	TaskCharges             // the charge records
	numTasks
)

var taskNames = [numTasks]string{"balance", "catalog", "keys", "charges"}

func (t Task) String() string { return taskNames[t] }

// Status is where a supplier stands.
type Status uint8

// The statuses. Only Normal is polled. Removing and Removed are not set by the engine now (see RemovalStep); they keep
// their numbers because a stored state may hold one.
const (
	Normal         Status = iota
	NeedsAttention        // polling stopped until a person fixes the cause
	Removing
	Removed
)

// TaskState is the schedule of one task.
type TaskState struct {
	Due          time.Time // not before
	BackoffUntil time.Time // not before, after failures
	Failures     int       // in a row
	LastOK       time.Time
	Halted       bool // the supplier's answer was not understood: only this task stops, until a person resets it
}

// RemovalStep is the step a removal had reached, in the engine's days of doing the removal itself. A removal is one
// transaction of the admin now (suppliers.Admin.Remove), so no step is taken; the type and the members below it stay
// because they are in the stored state, which is read with jsonx.UnmarshalKnown and refuses a member it does not know.
type RemovalStep uint8

// ModelSnap is a model of a group as last read. Without are the capabilities its cost plan does not cover, which
// routing keeps requests that need them away from. Plan is the fingerprint of its cost plan, empty while the cost is
// unknown.
type ModelSnap struct {
	Name    string
	Formats []int
	Without protocol.Capabilities
	Plan    string
}

// GroupSnap is a group as last read.
type GroupSnap struct {
	Ref, Name, Ratio, Facts string
	Models                  []ModelSnap
}

// PlanKey names an endpoint the way the engine keeps it in its state and the way its cost plans are stored
// (suppliers.sync_plans.endpoint): the group's ref and the model's upstream name. Every writer and reader of a plan goes
// through it, because a key built in two places that disagree loses the plan without an error: every endpoint of that
// name would be left with its cost unknown and no reason in any log.
func PlanKey(ref, model string) string { return ref + "/" + model }

// State is everything the engine knows about one supplier. It is stored as it is (internal/suppliers/README.md「同步」
// 「持久化」), so the constants of Task, Status and RemovalStep only gain members at the end, and any other change to
// its fields that an old stored state would read differently needs a new format number in package persist.
type State struct {
	Version   int64 // the Store's fence: a Commit must name the version it read
	ConfigGen int64 // raised when the configuration changes; results of rounds that began before are void
	Status    Status
	Reason    string
	Tasks     [numTasks]TaskState

	Balance     adapter.Amount
	BalanceAt   time.Time
	Funding     adapter.Funding
	LowNotified bool // a BalanceLow event has been sent and the balance has not recovered

	Groups    []GroupSnap // sorted by Ref
	Keys      []adapter.KeyState
	Plans     map[string]string // "group/model" -> fingerprint of the current cost plan
	Confirmed map[string]string // model -> a person's confirmed base schedule (JSON); scaled by the group's ratio

	// OfferAt and KeysAt are how far the offer and the keys listing got: the adapter's own positions, kept with each
	// read of a listing so that one the supplier's limits, an error or a restart stopped goes on where it stopped
	// instead of being read from the start (adapter.Page). A listing that ended clears its position. They are stored
	// beside the state, not in it: an older node refuses a member of the state it does not know (jsonx.UnmarshalKnown),
	// and the two run side by side during a rollout (persist).
	OfferAt, KeysAt adapter.Listing `json:"-"`

	// Scan is the adapter's position in the charge records, opaque to the engine and stored with each page. ScanOpen
	// is whether a pass is still under way; the adapter keeps the watermark in Scan.
	Scan     adapter.ScanState
	ScanOpen bool

	// Removal, GraceUntil, RescanFrom and RemovalAt are never set now (RemovalStep says why they stay). Through is how far
	// the adapter says the charge records are read, from the last Done page that said.
	Removal                           RemovalStep
	GraceUntil, RescanFrom, RemovalAt time.Time
	Through                           time.Time
}

// Kind names an event.
type Kind uint8

// The events. They are written with the state that caused them and delivered after commit at least once, so the
// receivers must tolerate a repeat.
const (
	GroupAdded Kind = iota
	GroupRemoved
	RatioChanged
	ModelAdded
	ModelRemoved
	PlanChanged // a new version of an endpoint's cost plan
	KeyChanged
	BalanceLow
	NeedsAttentionEvent
	TaskHalted
	GroupChanged     // the facts that decide what a group serves
	ModelChanged     // a model's formats or capability limits
	FundingChanged   // where calls are paid from
	BalanceCrossed   // the balance went above or below zero, the line routing keeps a supplier out by
	BalanceRecovered // the balance rose back over the low-balance line
)

var kindNames = [...]string{
	"group_added", "group_removed", "ratio_changed", "model_added", "model_removed", "plan_changed", "key_changed",
	"balance_low", "needs_attention", "task_halted", "group_changed", "model_changed",
	"funding_changed", "balance_crossed", "balance_recovered",
}

// String is the name an event is stored and delivered under, so that reordering the constants changes no stored event.
func (k Kind) String() string { return kindNames[k] }

// Event is something that changed. The text fields are names, fingerprints, success observations and a short reason of ours: never text
// the supplier wrote.
type Event struct {
	Kind          Kind
	Subject       string // group ref, "group/model", key ref
	Old, New, Why string // Failure events keep the task success baseline in Old for clock-independent recovery.
}

// Plan is a new version of a cost plan, appended and never changed.
type Plan struct {
	Endpoint    string // "group/model"
	Fingerprint string
	Schedule    string // canonical JSON
	Basis       string // "published" or "confirmed"
}

// Commit is the outcome of one step, applied whole or not at all.
type Commit struct {
	State   State
	Events  []Event
	Plans   []Plan
	Charges []adapter.Charge // upserted by Ref
}
