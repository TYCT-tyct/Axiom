package suppliers

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// Supplier is a supplier as the admin lists it. Strings that are empty are not there.
type Supplier struct {
	ID                                  uuid.UUID
	Name, Kind, Origin, UpstreamAccount string
	Generation, DetectedVersion         string
	NativeUnit, SettlementCurrency      string
	Status                              Status
	StatusReason                        string
	BalanceNative, LowBalanceThreshold  string
	BalanceObservedAt                   *time.Time
	UnitValue                           string
}

func supplierOf(r store.GetSupplierViewRow) Supplier {
	return Supplier{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Origin: r.Origin, UpstreamAccount: r.UpstreamAccount,
		Generation: deref(r.Generation), DetectedVersion: deref(r.DetectedVersion), NativeUnit: r.NativeUnit,
		SettlementCurrency: r.SettlementCurrency, Status: Status(r.Status), StatusReason: deref(r.StatusReason),
		BalanceNative: r.BalanceNative, LowBalanceThreshold: r.LowBalanceThreshold, BalanceObservedAt: r.BalanceObservedAt,
		UnitValue: r.UnitValue,
	}
}

func derefQuantization(s *string) Quantization {
	if s == nil {
		return ""
	}
	return Quantization(*s)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Group is one of a supplier's groups. Facts is the supplier's own statement about it.
type Group struct {
	ID                uuid.UUID
	UpstreamRef, Name string
	Ratio             string
	Selected, Present bool
	Codename          string
	Facts             map[string]any
	// KeyState is how the key Axiom uses for the group stands: ready (one is stored), pending (the sync will make one),
	// failed (the last attempt did not work, see KeyReason) or unavailable (the group is not in use, gone, or cannot be
	// called, so no key is needed).
	KeyState string
	// KeyReason is the fixed reason a key could not be made: not_permitted, group_unavailable or refused.
	KeyReason string
	// Pool is what the management API of a pool last said of the group's accounts; nil when this supplier is not one, or
	// nothing has been read yet.
	Pool *Pool
}

// Pool is one group's capacity as the pool's management API reported it. Accounts, Failed and Usable are exact counts;
// Headroom and Capacity are percentages and nil when no account reported a quota window. Low is where routing halves
// the group's weight.
type Pool struct {
	Accounts, Failed, Usable int
	Headroom, Capacity       *int
	Low                      bool
	ObservedAt               time.Time
}

// Credential is a key Axiom uses for a group.
type Credential struct {
	ID, Group      uuid.UUID
	UpstreamKeyRef string
	Status         string
	CooldownUntil  *time.Time
	ExpiresAt      *time.Time
	MaxConcurrency int
	// RequestsPerMinute is the account RPM ceiling for selecting this credential; nil adds no ceiling.
	RequestsPerMinute *int
}

// credentialOf is a credential as the admin shows it.
func credentialOf(id, group uuid.UUID, ref, status string, cooldown, expires *time.Time, concurrency int32, perMinute *int32) Credential {
	return Credential{
		ID: id, Group: group, UpstreamKeyRef: ref, Status: status, CooldownUntil: cooldown, ExpiresAt: expires,
		MaxConcurrency: int(concurrency), RequestsPerMinute: limited(perMinute),
	}
}

// limited is a per-minute limit as the admin shows it: a column with no value is no ceiling of Axiom's, which stays no
// value here. An old node may write above the cap during a rolling release; show the cap the new code applies.
func limited(n *int32) *int {
	if n == nil {
		return nil
	}
	v := min(int(*n), MaxRequestsPerMinute)
	return &v
}

// Endpoint is one model in one of the supplier's groups, as the sync projected it: what a person picks to test.
type Endpoint struct {
	ID, Group, Model uuid.UUID
	UpstreamName     string
	Formats          []string
	CostState        string
	Enabled          bool
	// Quantization is the level an admin recorded, empty when none is. It is not the supplier's report: the sync cannot
	// see it.
	Quantization Quantization
}

// Detail is a supplier with its groups, the keys bound to them and its endpoints, as the sync last stored them.
type Detail struct {
	Supplier    Supplier
	Groups      []Group
	Credentials []Credential
	Endpoints   []Endpoint
}

// groupFacts is one row of a group as the tables hold it, before the fixed names of its key state are worked out.
type groupFacts struct {
	ID                             uuid.UUID
	Ref, Name, Ratio               string
	Facts                          []byte
	Selected, Present, KeyEligible bool
	Codename, KeyError             *string
	Pool                           *Pool
}

func groupOf(f groupFacts) (Group, error) {
	state, reason := keyStateOf(f.Present, f.Selected, f.Codename != nil, f.KeyEligible, f.KeyError)
	g := Group{
		ID: f.ID, UpstreamRef: f.Ref, Name: f.Name, Ratio: f.Ratio, Selected: f.Selected, Codename: deref(f.Codename),
		Present: f.Present, KeyState: state, KeyReason: reason, Pool: f.Pool,
	}
	if err := jsonx.Unmarshal(f.Facts, &g.Facts); err != nil {
		return Group{}, fmt.Errorf("the facts of group %s: %w", f.ID, err)
	}
	return g, nil
}

// keyStateOf answers how a group's key stands from the group's own row: failed if the last attempt said why, pending if
// the projection says it needs a key, and unavailable if it does not. A group the supplier no longer lists is
// unavailable. A group a person unselected is unavailable too, but a group never chosen is still being onboarded by the
// sync (its code name is empty): it can need a key and can fail, and that must be shown before anyone selects it.
// Whether one is stored is read from the credentials; the detail has them and raises pending to ready, while a change
// answers without reading every credential.
func keyStateOf(present, selected, chosen, eligible bool, keyError *string) (string, string) {
	switch {
	case !present || (chosen && !selected):
		return "unavailable", ""
	case keyError != nil:
		return "failed", *keyError
	case eligible:
		return "pending", ""
	default:
		return "unavailable", ""
	}
}

// poolOf reads the pool columns of a group. A group no pool read has reported has no row, which is nil and not a
// zeroed capacity.
func poolOf(accounts, failed, usable, headroom, capacity *int32, low bool, observed *time.Time) *Pool {
	if accounts == nil {
		return nil
	}
	p := &Pool{Accounts: int(*accounts), Failed: int(*failed), Usable: int(*usable), Low: low}
	if headroom != nil {
		v := int(*headroom)
		p.Headroom = &v
	}
	if capacity != nil {
		v := int(*capacity)
		p.Capacity = &v
	}
	if observed != nil {
		p.ObservedAt = *observed
	}
	return p
}

// applyKeyStates raises a group's state from its credentials: one with a stored key is ready, one whose key is waiting
// for its text is pending, and a binding whose read already failed keeps its fixed reason. The groups were read without
// the credentials so that a change answers without reading them; the detail reads them anyway and applies this.
func applyKeyStates(groups []Group, credentials []Credential) {
	hasKey := make(map[uuid.UUID]bool, len(credentials))
	waiting := make(map[uuid.UUID]bool, len(credentials))
	for _, c := range credentials {
		switch c.Status {
		case "binding":
			waiting[c.Group] = true
		case "erased":
		default:
			hasKey[c.Group] = true
		}
	}
	for i := range groups {
		switch {
		case hasKey[groups[i].ID]:
			groups[i].KeyState, groups[i].KeyReason = "ready", ""
		case waiting[groups[i].ID] && groups[i].KeyState != "failed":
			groups[i].KeyState, groups[i].KeyReason = "pending", ""
		}
	}
}

// view reads the supplier of the owners as it is, without its groups and credentials: a change answers with the
// supplier it changed, of every owner, while it holds the supplier's row lock, and must not read more than that.
func (*Admin) view(ctx context.Context, tx pg.Tx, id uuid.UUID, owners Owners) (Supplier, error) {
	rows, err := store.New().GetSupplierView(ctx, tx, store.GetSupplierViewParams{ID: id, EveryOwner: bool(owners)})
	if err != nil {
		return Supplier{}, fmt.Errorf("read the supplier: %w", err)
	}
	if len(rows) == 0 {
		return Supplier{}, ErrNoSupplier
	}
	return supplierOf(rows[0]), nil
}

// TopUpBasis is what a top-up of a supplier is computed from (docs/domains/operations.md「转账的累计金额上限」): who
// owns it (Site nil for the platform; Account for a key an account brought, which is never topped up through Axiom),
// its units, and the unit value in force, UnitValue empty when none is confirmed.
type TopUpBasis struct {
	ID                             uuid.UUID
	Name                           string
	Site                           *uuid.UUID
	Account                        bool
	Status                         Status
	NativeUnit, SettlementCurrency string
	UnitValueID, UnitValue         string
}

// TopUpBasis reads supplier id's; ErrNoSupplier when there is none.
func (*Admin) TopUpBasis(ctx context.Context, tx pg.Tx, id uuid.UUID) (TopUpBasis, error) {
	rows, err := store.New().TopUpBasis(ctx, tx, id)
	if err != nil {
		return TopUpBasis{}, fmt.Errorf("read supplier %s: %w", id, err)
	}
	if len(rows) == 0 {
		return TopUpBasis{}, ErrNoSupplier
	}
	r := rows[0]
	return TopUpBasis{
		ID: r.ID, Name: r.Name, Site: r.SiteID, Account: r.OwnerScope == "account", Status: Status(r.Status),
		NativeUnit: r.NativeUnit, SettlementCurrency: r.SettlementCurrency, UnitValueID: r.UnitValueID, UnitValue: r.UnitValue,
	}, nil
}
