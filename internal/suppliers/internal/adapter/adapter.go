// Package adapter is the contract between the suppliers module and the code that knows one kind of supplier system
// (new-api, sub2api, ...). An adapter has two halves that combine freely: a management half that reads the supplier's
// account, offer, keys and charges, and a call half of quirks that the module's shared call code asks about. Adapters
// live in packages of their own and are listed in package registry, one line each (internal/suppliers/README.md
// 「适配器」).
package adapter

import (
	"context"
	"errors"
	"regexp"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
)

// Kind names an adapter and is stored in suppliers.kind. It is text, not an enum, so that removing an adapter needs no
// migration; the pattern is the table's own CHECK.
type Kind string

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// Valid reports whether k can be stored: the pattern of suppliers.kind.
func (k Kind) Valid() bool { return kindPattern.MatchString(string(k)) }

// Adapter is what the module needs to know about one kind of supplier system. Its management half is read-only: it
// never creates keys, tops up or revokes anything at the supplier, so that no state at the supplier can exist that
// we failed to record. Abilities added later, such as writing, are separate optional interfaces, so that what an
// adapter can do is what it implements.
type Adapter interface {
	Kind() Kind
	// Buckets declares the supplier's rate limits that this adapter's requests count against.
	Buckets() []BucketSpec
	// Detect recognises the system and its generation from public endpoints only; it holds no credentials.
	Detect(ctx context.Context, c Client) (Detection, error)
	// Open logs in with the credentials login holds and returns the management half.
	Open(ctx context.Context, c Client, login LoginStore) (Manager, error)
	Calls() CallQuirks
}

// Detection is what Detect recognised. Captcha is set when the supplier's login needs a human check, which no program
// can pass: its credentials have to be made by a person.
type Detection struct {
	Generation, Version string
	Captcha             bool
}

// The errors every adapter reports the same way, so the module's shared code can tell what to do without reading the
// supplier's words. They wrap at most a code from a fixed list, never text from the supplier.
var (
	// ErrNotRecognised: Detect found that the address is not a supplier system of this adapter's kind.
	ErrNotRecognised = errors.New("adapter: not a system of this kind")
	// ErrAuthRejected: the supplier refused the account credentials (expired, revoked, missing a permission). Polling
	// the supplier stops until a person fixes them; trying again would only risk the account.
	ErrAuthRejected = errors.New("adapter: the supplier rejected the account credentials")
	// ErrUnrecognised: the supplier answered in a form this adapter does not know, as after an upgrade there. Only the
	// task that read it stops, and nothing it read is written.
	ErrUnrecognised = errors.New("adapter: the response is not in a form this adapter knows")
	// ErrNotPool: the login does not administer the supplier's accounts, so the supplier is a relay and has no pool to
	// read. It is not a failure: a relay's capacity is its own, and its weight is not Axiom's to lower.
	ErrNotPool = errors.New("adapter: the supplier is not a pool this login administers")
)

// Pool is the optional half of a management side whose supplier is a pool of upstream accounts: a self-hosted system
// (sub2api, CPA, ...) whose administrator interface lists the accounts and how much of their quota windows is left. A
// relay does not implement it; a pool whose login is not an administrator answers ErrNotPool.
type Pool interface {
	// Pool reads the pool's capacity per group. GroupRef is the supplier's own group ID, the one its offer lists. It
	// reports a group only when at least one account is assigned to it: a group absent from the answer is not known,
	// which is not the same as empty.
	Pool(ctx context.Context) (PoolReading, error)
}

// PoolReading is one read of a pool. Complete is false when the directory had more pages than one read may take:
// Groups is then a prefix, and the caller must keep its previous reading instead of treating the groups it did not
// reach as recovered.
type PoolReading struct {
	Groups   []GroupCapacity
	Complete bool
}

// GroupCapacity is what one group of a pool has: its accounts and the room left in their quota windows. The counts are
// exact; Headroom is the mean over a sample of the accounts that can serve now, of the percent of the tighter of the
// supplier's quota windows (its five-hour and weekly windows) that is still unused. nil means no account reported a
// window, which is not the same as empty.
type GroupCapacity struct {
	GroupRef         string
	Accounts, Failed int // Failed: accounts the pool marks failed, which cannot serve
	Usable           int // Usable: accounts it may schedule now, whether a window was reported or not
	Headroom         *int
}

// Amount is a quantity in the supplier's native unit, as decimal text at the supplier's own precision. Upstream
// amounts never pass through float64: a rounded cost would show up as a difference at reconciliation.
type Amount string

// Funding is where a supplier draws what a call costs from. Only the wallet is supported: a subscription's window is
// not money, and counting it as cash cost would invent a cost.
type Funding uint8

// The funding sources.
const (
	FundingOther Funding = iota
	FundingWallet
)

// LoginStore gives an adapter the supplier's sealed account credentials. Swap writes only over the version Load
// returned and fails otherwise, so that two writers of a one-time refresh token cannot overwrite each other. The
// store also checks the supplier's lease and configuration generation in the same transaction.
type LoginStore interface {
	Load(ctx context.Context) (raw []byte, version int64, err error)
	Swap(ctx context.Context, version int64, raw []byte) error
}

// ErrStaleLogin is Swap's error when the stored credentials are no longer the version the caller read.
var ErrStaleLogin = errors.New("adapter: the stored login changed since it was read")

// Manager is the read-only management half of an adapter.
//
// Its two listings, the offer and the keys, are read over as many reads as the supplier's limits need: a read is given
// the position the one before returned (none for the first) and returns the position it reached, so that the module
// stores it in the supplier's sync state and the next round goes on from there instead of holding a worker until the
// limits reopen (Listing, Page).
type Manager interface {
	Account(ctx context.Context) (Account, error)
	// Offer reads the offer from the position in from, as far as the supplier's limits allow.
	Offer(ctx context.Context, from Listing) (Page[Group], error)
	// Keys reads the keys from the position in from, as far as the supplier's limits allow.
	Keys(ctx context.Context, from Listing) (Page[KeyState], error)
	// KeySecret reads a key's text from the supplier by its id there. Keys are never typed in, so the text always
	// belongs to the key it is stored under.
	KeySecret(ctx context.Context, ref KeyRef) (secret.Secret, error)
	// Charges reads one page of the charge records, newest first, from the position in scan.
	Charges(ctx context.Context, scan ScanState) (ChargePage, ScanState, error)
}

// Listing is an adapter's own position in a paged listing, opaque to everyone else. It carries what the pass has read so
// far, so that a listing a rate limit, an error or a restart stopped goes on where it stopped instead of from the start.
// A position the adapter cannot read, or one of a listing the supplier changed under it, begins the listing again: a
// page may then be read twice, but none is skipped.
type Listing []byte

// Page is what one read of a paged listing got. Only the read that ends the listing (Done) holds items: it holds every
// one the pass read, and no Next. A read that stops earlier holds the position to go on from in Next, and Until is when
// the supplier's limits let the pass go on; an incomplete page always has a nonzero Until.
type Page[T any] struct {
	Items []T
	Next  Listing
	Done  bool
	Until time.Time
}

// Account is the balance and where it is drawn from. UnitHint is the supplier's own statement of what a native unit
// is worth, which a person confirms.
type Account struct {
	Balance  Amount
	Funding  Funding
	UnitHint string
}

// Group is one of the supplier's groups. Facts is canonical JSON of what the supplier says about it (platform,
// subscription type, peak pricing, long context), which Formats reads.
type Group struct {
	Ref, Name string
	Ratio     Amount
	Facts     GroupFacts
	Models    []Model
	// ModelsUnread is set when the supplier did not let the adapter read this group's models this time (no key in the
	// group, or a balance too low to list): Models is then empty because nothing was read, and the module keeps the
	// models it last saw instead of taking the group to have none.
	ModelsUnread bool
}

// GroupFacts is a group's facts as canonical JSON.
type GroupFacts []byte

// Model is a model a group offers under the supplier's name for it. Cost is nil when it cannot be expressed as a
// catalog schedule, which makes the cost unknown and keeps the endpoint out of routing.
type Model struct {
	UpstreamName string
	Formats      []protocol.Format
	Cost         *catalog.Schedule
	// Without are the capabilities the supplier's price does not cover (images priced at a ratio of their own, say):
	// the endpoint does not claim them, so routing keeps requests that need them away.
	Without protocol.Capabilities
}

// KeyRef is the supplier's own id of a key.
type KeyRef string

// KeyStatus is what the supplier says about one of our keys.
type KeyStatus uint8

// The statuses a supplier can report.
const (
	KeyActive KeyStatus = iota
	KeyDisabled
	KeyQuotaExhausted
	KeyExpired
)

// KeyState is one key as the supplier lists it.
type KeyState struct {
	Ref      KeyRef
	GroupRef string
	Status   KeyStatus
	ResetAt  time.Time  // when an exhausted key's allowance returns, zero if not stated
	Policy   *KeyPolicy `json:",omitzero"` // read with the listing; absent in the legacy persisted State.Keys
}

// KeyPolicy is the upstream permission of a key, independent of Axiom's concurrency limits.
// Models nil means unrestricted; an empty list permits nothing, including unreadable restrictions.
type KeyPolicy struct {
	Models    []string `json:",omitzero"`
	ExpiresAt *time.Time
}

// ChargeRef is the supplier's own id of a request, as it comes back in response headers and in the charge records.
type ChargeRef string

// Charge is one record of the supplier's charge list. Usage is what the record says was used, on the billing items,
// and Reported is the items it states: an item outside Reported was not reported, which differs from one reported as
// zero, because only reported items correct an estimate (usage × the request's locked price,
// docs/domains/billing.md「用量来源与暂估」) and the rest stay estimated. Amount alone cannot be split back into items.
type Charge struct {
	Ref        ChargeRef
	Key        KeyRef
	OccurredAt time.Time
	Amount     Amount
	Usage      catalog.Usage
	Reported   catalog.ItemSet
	Funding    Funding
	Model      string
}

// ChargePage is one page of charges and whether the pass has reached its end. A pass may cover less than the time up
// to now (an adapter reads a backlog window by window), so Done does not say how far the records are read: Through
// does. On a Done page it is the time before which every record has been read, and every adapter reports it (the
// contract in adaptertest), so that whoever reads the records to a moment does not take a backlog for read.
type ChargePage struct {
	Charges []Charge
	Done    bool
	Through time.Time
}

// ScanState is an adapter's own position in a scan, opaque to everyone else.
type ScanState []byte
