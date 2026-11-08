package suppliers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// What the admin does with suppliers (README.md「管理接口」). Each function takes the caller's transaction, which must
// hold the platform scope: the audit entry is written in it. Nothing here talks to a supplier except Detect; the
// sync does, and finds what was decided here in the rows and in the supplier's configuration generation.

// The ways a change is refused.
var (
	// ErrNoSupplier is a supplier that is not there.
	ErrNoSupplier = errors.New("suppliers: no such supplier")
	// ErrSupplierExists is an origin and account already connected.
	ErrSupplierExists = errors.New("suppliers: that origin and account are already connected")
	// ErrNoAdapter is a kind of system this program has no adapter for, or an adapter a person cannot connect.
	ErrNoAdapter = errors.New("suppliers: no adapter of that kind that can be connected")
	// ErrBadOrigin is an origin that is no https://host[:port].
	ErrBadOrigin = errors.New("suppliers: the origin is not https://host[:port]")
	// ErrAddressRefused is an origin the outbound client refuses to connect to (egress.ErrAddressDenied).
	ErrAddressRefused = errors.New("suppliers: the outbound client refuses that address")
	// ErrUnreachable is an origin that did not answer a probe.
	ErrUnreachable = errors.New("suppliers: the site did not answer")
	// ErrStatusLocked is a supplier whose status a person cannot move: it is not in a state this change starts from.
	ErrStatusLocked = errors.New("suppliers: the supplier's status does not allow this")
	// ErrNoGroup is a group the supplier does not have.
	ErrNoGroup = errors.New("suppliers: the supplier has no such group")
	// ErrGroupGone is a group the supplier no longer lists, which cannot be selected.
	ErrGroupGone = errors.New("suppliers: the supplier no longer lists that group")
	// ErrGroupNotSelected is a group that is not selected, which a key cannot be bound to.
	ErrGroupNotSelected = errors.New("suppliers: the group is not selected")
	// ErrKeyBound is a key of the supplier that is already bound.
	ErrKeyBound = errors.New("suppliers: that key is already bound")
	// ErrAliasExists is an upstream name that already has an alias in the scope.
	ErrAliasExists = errors.New("suppliers: that name already has an alias in that scope")
	// ErrNotPending is a name the group does not list as waiting for a model: it is not listed, the catalog
	// recognised it, or an alias of the group or of the whole supplier covers it.
	ErrNotPending = errors.New("suppliers: the group does not list that name as waiting for a model")
	// ErrModelTaken is a model the group already has a name for: another would take its endpoint over.
	ErrModelTaken = errors.New("suppliers: the group already has a name for that model")
	// ErrUnknownFormat is a formats declaration naming a call format this node does not know.
	ErrUnknownFormat = errors.New("suppliers: unknown call format")
	// ErrAliasRevision is a correction based on an alias revision that is no longer current.
	ErrAliasRevision = errors.New("suppliers: the alias changed since it was read")
	// ErrUnitValue is a value that is not above 0 and at most 1000 with at most 15 decimals.
	ErrUnitValue = errors.New("suppliers: a unit value is above 0 and at most 1000, with at most 15 decimals")
	// ErrNoCredential is a credential the supplier does not have.
	ErrNoCredential = errors.New("suppliers: the supplier has no such credential")
	// ErrNoSupplierEndpoint is an endpoint the supplier does not have.
	ErrNoSupplierEndpoint = errors.New("suppliers: the supplier has no such endpoint")
	// ErrLimits is a concurrency that is not 1 to 2147483647, or a per-minute limit that is not 1 to
	// MaxRequestsPerMinute or none.
	ErrLimits = fmt.Errorf("suppliers: concurrency is 1 to 2147483647, and requests a minute 1 to %d or none", MaxRequestsPerMinute)
)

// LoginError is credentials that cannot make a login: the member that is wrong, and which rule it breaks.
type LoginError struct{ Field, Reason string }

func (e *LoginError) Error() string { return "suppliers: login: " + e.Field + " " + e.Reason }

// Credentials are what a person gives for a supplier's login. They are secrets and are only sealed.
type Credentials = adapter.Credentials

// Status is where a supplier is in its life.
type Status string

// The statuses (the table's CHECK).
const (
	Connecting     Status = "connecting"
	Active         Status = "active"
	LowBalance     Status = "low_balance"
	NeedsAttention Status = "needs_attention"
	Paused         Status = "paused"
	Removing       Status = "removing"
	Removed        Status = "removed"
)

// Admin is the admin side of the module in one node.
type Admin struct {
	registry *registry.Registry
	keys     *secret.Keyring
	egress   *egress.Client
	now      func() time.Time
	byok     bool
}

// AdminConfig is what the admin side needs.
type AdminConfig struct {
	// Keys seals the logins.
	Keys *secret.Keyring
	// Egress is the outbound client probes and address checks go through.
	Egress *egress.Client
	// Registry is the adapters of the program; nil is registry.Default.
	Registry *registry.Registry
	// Byok allows accounts to create provider keys. It is off until every node knows the openai adapter and the byok
	// status; a deployment turns it on only after the rollout (docs/domains/accounts.md「自带的供应商 key」).
	Byok bool
	// Clock is the time; nil is time.Now.
	Clock func() time.Time
}

// NewAdmin makes the admin side.
func NewAdmin(cfg AdminConfig) (*Admin, error) {
	r := cfg.Registry
	if r == nil {
		var err error
		if r, err = registry.Default(); err != nil {
			return nil, fmt.Errorf("adapters: %w", err)
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Admin{registry: r, keys: cfg.Keys, egress: cfg.Egress, now: cfg.Clock, byok: cfg.Byok}, nil
}

// onboarding is the adapter of kind, which a person can connect.
func (a *Admin) onboarding(kind string) (adapter.Onboarding, error) {
	ad, ok := a.registry.Get(adapter.Kind(kind))
	if !ok {
		return nil, ErrNoAdapter
	}
	o, ok := ad.(adapter.Onboarding)
	if !ok {
		return nil, ErrNoAdapter
	}
	return o, nil
}

// SupplierQuery chooses the suppliers to list, newest first. After is the last supplier of the page before, the zero
// UUID for the first page; an empty Status is every status.
type SupplierQuery struct {
	Status   Status
	After    uuid.UUID
	PageSize int
}

// Suppliers lists a page of suppliers; more tells whether suppliers remain beyond it.
func (*Admin) Suppliers(ctx context.Context, tx pg.Tx, q SupplierQuery) (suppliers []Supplier, more bool, err error) {
	params := store.ListSuppliersPageParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Status != "" {
		status := string(q.Status)
		params.Status = &status
	}
	if q.After != (uuid.UUID{}) {
		params.After = &q.After
	}
	rows, err := store.New().ListSuppliersPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the suppliers: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	suppliers = make([]Supplier, len(rows))
	for i, r := range rows {
		suppliers[i] = supplierOf(store.GetSupplierViewRow(r))
	}
	return suppliers, more, nil
}

// Supplier is one supplier with its groups, credentials and endpoints; ErrNoSupplier when there is none.
func (a *Admin) Supplier(ctx context.Context, tx pg.Tx, id uuid.UUID) (Detail, error) {
	summary, err := a.view(ctx, tx, id)
	if err != nil {
		return Detail{}, err
	}
	q := store.New()
	d := Detail{Supplier: summary}
	groups, err := q.ListGroupsOf(ctx, tx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("read the groups: %w", err)
	}
	for _, g := range groups {
		group, err := groupOf(groupFacts{
			ID: g.ID, Ref: g.UpstreamRef, Name: g.Name, Ratio: g.Ratio, Facts: g.Facts, Selected: g.Selected,
			Present: g.Present, KeyEligible: g.KeyEligible, Codename: g.Codename, KeyError: g.KeyError,
			Pool: poolOf(g.PoolAccounts, g.PoolFailed, g.PoolUsable, g.PoolHeadroom, g.PoolCapacity, g.PoolLow, g.PoolObservedAt),
		})
		if err != nil {
			return Detail{}, err
		}
		d.Groups = append(d.Groups, group)
	}
	creds, err := q.ListCredentialsOf(ctx, tx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("read the credentials: %w", err)
	}
	for _, c := range creds {
		d.Credentials = append(d.Credentials, credentialOf(
			c.ID, c.SupplierGroupID, c.UpstreamKeyRef, c.Status, c.CooldownUntil, c.ExpiresAt, c.MaxConcurrency, c.RequestsPerMinute))
	}
	applyKeyStates(d.Groups, d.Credentials)
	endpoints, err := q.ListEndpointsOf(ctx, tx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("read the endpoints: %w", err)
	}
	for _, e := range endpoints {
		d.Endpoints = append(d.Endpoints, Endpoint{
			ID: e.ID, Group: e.SupplierGroupID, Model: e.ModelID, UpstreamName: e.UpstreamName, Formats: e.Formats,
			CostState: e.CostState, Enabled: e.Enabled, Quantization: derefQuantization(e.Quantization),
		})
	}
	return d, nil
}

// locked is the supplier's row, locked for a change: it must be there and in one of the statuses the change starts
// from (none: any status that is not an end of life, removing or removed).
func (*Admin) locked(ctx context.Context, tx pg.Tx, id uuid.UUID, from ...Status) (store.LockSupplierForChangeRow, error) {
	rows, err := store.New().LockSupplierForChange(ctx, tx, id)
	if err != nil {
		return store.LockSupplierForChangeRow{}, fmt.Errorf("lock the supplier: %w", err)
	}
	if len(rows) == 0 {
		return store.LockSupplierForChangeRow{}, ErrNoSupplier
	}
	status := Status(rows[0].Status)
	if len(from) == 0 {
		from = []Status{Connecting, Active, LowBalance, NeedsAttention, Paused}
	}
	if !slices.Contains(from, status) {
		return store.LockSupplierForChangeRow{}, fmt.Errorf("%w: it is %s", ErrStatusLocked, status)
	}
	return rows[0], nil
}

func (*Admin) record(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, id uuid.UUID, reason string, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "supplier", ID: id.String()}, Detail: detail,
	})
}

// recordByok is record for a row an account owns: the audit row must carry the tenant, because the account's own
// transaction is what writes it and platform.audit_log's row-level security refuses a row with no scope.
func (*Admin) recordByok(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, site, account, id uuid.UUID, reason string, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "supplier", ID: id.String(), Site: site, Account: account},
		Detail: detail,
	})
}

// SetStatus pauses a supplier that is in use or resumes one that is paused; another change of status is the sync's, or
// the removal's. A supplier already as asked, paused or in use, is left as it is.
func (a *Admin) SetStatus(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, to Status, reason string) (Supplier, error) {
	row, err := a.locked(ctx, tx, id, Active, LowBalance, Paused)
	if err != nil {
		return Supplier{}, err
	}
	if paused := Status(row.Status) == Paused; paused != (to == Paused) {
		action := "supplier.pause"
		if to == Active {
			action = "supplier.resume"
		}
		if err := store.New().SetSupplierStatusBumpingConfigGen(ctx, tx, store.SetSupplierStatusBumpingConfigGenParams{ID: id, Status: string(to)}); err != nil {
			return Supplier{}, fmt.Errorf("set the status: %w", err)
		}
		if err := a.record(ctx, tx, actor, action, id, reason, map[string]any{"from": row.Status, "to": string(to)}); err != nil {
			return Supplier{}, err
		}
	}
	return a.view(ctx, tx, id)
}

// Remove takes a supplier out for good, in the one transaction: its status is `removed` and its configuration generation
// is raised, so it leaves the routing snapshot at the next reload and a round of the sync that began before is
// discarded; its sealed login is deleted; and the sealed key of each credential is cleared and the credential marked
// `erased`. The credentials stay as rows, because attempts made with them point at the row. Calls in flight go on with
// the key they hold in memory. Axiom never deletes keys at the supplier, including the ones it made, so the keys a person
// has to revoke there stay listed on the erased credential rows (their upstream_key_ref), and the audit entry says how
// many. A supplier removed already is returned as it is; a supplier a former version left in `removing` is removed now.
func (a *Admin) Remove(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, reason string) (Supplier, error) {
	row, err := a.locked(ctx, tx, id, Connecting, Active, LowBalance, NeedsAttention, Paused, Removing, Removed)
	if err != nil {
		return Supplier{}, err
	}
	if Status(row.Status) == Removed {
		return a.view(ctx, tx, id)
	}
	q := store.New()
	creds, err := q.ListCredentialsOf(ctx, tx, id)
	if err != nil {
		return Supplier{}, fmt.Errorf("read the credentials: %w", err)
	}
	if err := q.SetSupplierStatusBumpingConfigGen(ctx, tx, store.SetSupplierStatusBumpingConfigGenParams{ID: id, Status: string(Removed)}); err != nil {
		return Supplier{}, fmt.Errorf("set the status: %w", err)
	}
	if err := q.DeleteSupplierLogin(ctx, tx, id); err != nil {
		return Supplier{}, fmt.Errorf("delete the login: %w", err)
	}
	if err := q.EraseCredentialsOf(ctx, tx, id); err != nil {
		return Supplier{}, fmt.Errorf("erase the keys: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.remove", id, reason, map[string]any{"from": row.Status, "keys_to_revoke_count": len(creds)}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}

// SetEndpointQuantization records the level an endpoint serves at, or clears it with nil. Only an admin sets it: the
// supplier's own model list does not report it, so the sync cannot. Every other node sees it on its next Reload.
func (a *Admin) SetEndpointQuantization(ctx context.Context, tx pg.Tx, actor audit.Actor, supplierID, endpointID uuid.UUID,
	level *Quantization, reason string,
) (Endpoint, error) {
	e, err := a.lockedEndpoint(ctx, tx, supplierID, endpointID)
	if err != nil {
		return Endpoint{}, err
	}
	var stored *string
	if level != nil {
		value := string(*level)
		stored = &value
	}
	if err := store.New().SetEndpointQuantization(ctx, tx, store.SetEndpointQuantizationParams{
		SupplierID: supplierID, ID: endpointID, Quantization: stored,
	}); err != nil {
		return Endpoint{}, fmt.Errorf("set the endpoint's quantization: %w", err)
	}
	from := derefQuantization(e.Quantization)
	to := Quantization("")
	if level != nil {
		to = *level
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "supplier.endpoint.quantization", Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "endpoint", ID: endpointID.String()},
		Detail: map[string]any{"supplier": supplierID.String(), "from": string(from), "to": string(to)},
	}); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		ID: e.ID, Group: e.SupplierGroupID, Model: e.ModelID, UpstreamName: e.UpstreamName, Formats: e.Formats,
		CostState: e.CostState, Enabled: e.Enabled, Quantization: to,
	}, nil
}

// SetEndpointEnabled turns an endpoint on or off for routing. It is a person's or the agent's switch: the sync never
// writes it, so an endpoint turned off stays off until someone turns it on. Every node routes by it from its next
// Reload; an endpoint whose cost is not known stays out of routing either way.
func (a *Admin) SetEndpointEnabled(ctx context.Context, tx pg.Tx, actor audit.Actor, supplierID, endpointID uuid.UUID,
	enabled bool, reason string,
) (Endpoint, error) {
	e, err := a.lockedEndpoint(ctx, tx, supplierID, endpointID)
	if err != nil {
		return Endpoint{}, err
	}
	if err := store.New().SetEndpointEnabled(ctx, tx, store.SetEndpointEnabledParams{
		SupplierID: supplierID, ID: endpointID, Enabled: enabled,
	}); err != nil {
		return Endpoint{}, fmt.Errorf("switch the endpoint: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "supplier.endpoint.enabled", Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "endpoint", ID: endpointID.String()},
		Detail: map[string]any{"supplier": supplierID.String(), "from": e.Enabled, "to": enabled},
	}); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		ID: e.ID, Group: e.SupplierGroupID, Model: e.ModelID, UpstreamName: e.UpstreamName, Formats: e.Formats,
		CostState: e.CostState, Enabled: enabled, Quantization: derefQuantization(e.Quantization),
	}, nil
}

// lockedEndpoint is an endpoint of the supplier, whose row is locked for a change as locked locks it.
func (a *Admin) lockedEndpoint(ctx context.Context, tx pg.Tx, supplierID, endpointID uuid.UUID) (store.GetEndpointOfRow, error) {
	if _, err := a.locked(ctx, tx, supplierID); err != nil {
		return store.GetEndpointOfRow{}, err
	}
	rows, err := store.New().GetEndpointOf(ctx, tx, store.GetEndpointOfParams{SupplierID: supplierID, ID: endpointID})
	if err != nil {
		return store.GetEndpointOfRow{}, fmt.Errorf("read the endpoint: %w", err)
	}
	if len(rows) == 0 {
		return store.GetEndpointOfRow{}, ErrNoSupplierEndpoint
	}
	return rows[0], nil
}
