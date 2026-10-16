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
)

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
// A listing that takes more requests than a rate bucket allows in a window may be read over several calls: when a request
// is refused for want of allowance (an error wrapping ErrBucketsFull), the Manager may keep how far the listing got, and
// the call made again on the same Manager continues. The module keeps a Manager through the retries of a round.
type Manager interface {
	Account(ctx context.Context) (Account, error)
	Offer(ctx context.Context) (Offer, error)
	Keys(ctx context.Context) ([]KeyState, error)
	// KeySecret reads a key's text from the supplier by its id there. Keys are never typed in, so the text always
	// belongs to the key it is stored under.
	KeySecret(ctx context.Context, ref KeyRef) (secret.Secret, error)
	// Charges reads one page of the charge records, newest first, from the position in scan.
	Charges(ctx context.Context, scan ScanState) (ChargePage, ScanState, error)
}

// Account is the balance and where it is drawn from. UnitHint is the supplier's own statement of what a native unit
// is worth, which a person confirms.
type Account struct {
	Balance  Amount
	Funding  Funding
	UnitHint string
}

// Offer is what the supplier sells: groups and their models.
type Offer struct{ Groups []Group }

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
	ResetAt  time.Time // when an exhausted key's allowance returns, zero if not stated
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

// ChargePage is one page of charges and whether the scan has reached its end.
type ChargePage struct {
	Charges []Charge
	Done    bool
}

// ScanState is an adapter's own position in a scan, opaque to everyone else.
type ScanState []byte
