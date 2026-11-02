package suppliers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
)

// Account-owned provider keys (docs/domains/accounts.md「自带的供应商 key」). A key is a supplier with the byok status:
// the rows the rest of the module already reads, made in one transaction, so that sealing, row-level security,
// capacity, failure classification and the routing snapshot need no second implementation.

// The ways a provider key is refused.
var (
	// ErrByokProvider is a provider this node has no call adapter for.
	ErrByokProvider = errors.New("suppliers: no such provider for a provider key")
	// ErrByokName is a name outside 1 to 200 characters.
	ErrByokName = errors.New("suppliers: a provider key's name is 1 to 200 characters")
	// ErrByokKey is a key that is not a plausible bearer token: empty, too long, or holding spaces or control bytes.
	ErrByokKey = errors.New("suppliers: the key is not a bearer token")
	// ErrByokModels is a provider with no listed catalog model this node can serve.
	ErrByokModels = errors.New("suppliers: no catalog model to serve with this key")
	// ErrByokNotFound is a provider key the account does not have, or one already removed.
	ErrByokNotFound = errors.New("suppliers: no such provider key")
	// ErrByokStatus is a status a provider key cannot be put in by a person.
	ErrByokStatus = errors.New("suppliers: not a status a provider key can take")
	// ErrByokDisabled is this deployment not having enabled provider keys yet.
	ErrByokDisabled = errors.New("suppliers: provider keys are not enabled on this deployment")
)

// byokProviders are the providers whose official API this node can call with an account's key. A provider is a kind in
// the adapter registry and the origin its API is at; adding one is a line here and an adapter.
var byokProviders = map[string]string{
	"openai": "https://api.openai.com",
}

// ByokModel is one catalog model a provider key serves, as the caller read it from the catalog snapshot: the model's
// own abilities decide the formats, and its reference price supplies the shape of the zero cost schedule.
type ByokModel struct {
	ID       uuid.UUID
	Upstream string
	Formats  []protocol.Format
	Schedule []byte
}

// ByokRequest is a key to make. Key is the plaintext and is only sealed; it is never stored or logged as it is.
type ByokRequest struct {
	Site, Account uuid.UUID
	Name          string
	Provider      string
	Key           string
	// Fallback says the key may use Axiom's endpoints for a model it covers when the key cannot serve it.
	Fallback bool
	Actor    audit.Actor
}

// ByokRule is one model an account's key keeps away from the platform: the key exists and the account forbade fallback,
// whether or not the key's credential is usable now. Routing reads these separately from Routable so a refusal cannot
// silently lift the account's choice.
type ByokRule struct {
	Site, Account, Model uuid.UUID
}

// ByokRules reads every account's no-fallback rule. It does not decrypt anything and does not depend on a credential's
// status: that is what makes the rule survive a 401.
func ByokRules(ctx context.Context, db *pg.DB, scope pg.Scope) ([]ByokRule, error) {
	var out []ByokRule
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		rows, err := store.New().ListByokNoFallback(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.SiteID == nil || r.AccountID == nil {
				continue
			}
			out = append(out, ByokRule{Site: *r.SiteID, Account: *r.AccountID, Model: r.ModelID})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the provider keys' fallback rules: %w", err)
	}
	return out, nil
}

// ByokKey is a provider key as the console lists it. It never holds the key or its ciphertext.
type ByokKey struct {
	ID        uuid.UUID
	Name      string
	Provider  string
	Hint      string
	Status    string
	Enabled   bool
	Rejected  bool
	Fallback  bool
	CreatedAt time.Time
}

// CreateByok makes an account's provider key: one supplier, one selected group, the models, an endpoint with a zero
// cost schedule for each, one sealed credential, the unit value and the balance that let routing pick it. models is
// what the caller read from the catalog; an empty list is refused. Everything commits or nothing does.
//
//nolint:cyclop,funlen,gocognit // one transaction writes the rows in their dependency order; splitting it would break the all-or-nothing read
func (a *Admin) CreateByok(ctx context.Context, tx pg.Tx, r ByokRequest, models []ByokModel) (ByokKey, error) {
	if !a.byok {
		return ByokKey{}, ErrByokDisabled
	}
	name := strings.TrimSpace(r.Name)
	if name == "" || len([]rune(name)) > 200 {
		return ByokKey{}, ErrByokName
	}
	origin, ok := byokProviders[r.Provider]
	if !ok {
		return ByokKey{}, ErrByokProvider
	}
	key := strings.TrimSpace(r.Key)
	if len(key) < 8 || len(key) > 4096 {
		return ByokKey{}, ErrByokKey
	}
	for _, c := range key {
		if c <= ' ' || c == unicode.ReplacementChar {
			return ByokKey{}, ErrByokKey
		}
	}
	if len(models) == 0 {
		return ByokKey{}, ErrByokModels
	}

	q := store.New()
	id, err := q.InsertByokSupplier(ctx, tx, store.InsertByokSupplierParams{
		SiteID: &r.Site, AccountID: &r.Account, Name: name, UpstreamAccount: uuid.NewV7().String(),
		ByokFallback: r.Fallback,
	})
	if err != nil {
		return ByokKey{}, fmt.Errorf("make the provider key: %w", err)
	}
	group, err := q.InsertGroup(ctx, tx, store.InsertGroupParams{
		SupplierID: id, UpstreamRef: origin, Name: r.Provider, Ratio: "1",
	})
	if err != nil {
		return ByokKey{}, fmt.Errorf("make the provider key's group: %w", err)
	}
	code := codename()
	if err := q.SelectGroup(ctx, tx, store.SelectGroupParams{ID: group, Selected: true, Codename: &code}); err != nil {
		return ByokKey{}, fmt.Errorf("select the provider key's group: %w", err)
	}
	credential := uuid.NewV7()
	sealed := a.keys.Seal(secret.New(key), secret.Location{
		Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key",
	})
	keyVersion := int64(sealed.KeyVersion)
	hintText := hintOf(key)
	if err := q.InsertByokCredential(ctx, tx, store.InsertByokCredentialParams{
		ID: credential, SupplierID: id, SupplierGroupID: group, UpstreamKeyRef: credential.String(),
		Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK,
		KeyVersion: &keyVersion, Hint: &hintText,
	}); err != nil {
		return ByokKey{}, fmt.Errorf("store the provider key: %w", err)
	}
	if err := q.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{
		SupplierID: id, Value: "1", Actor: "byok", Reason: "an account's own provider key: one unit, a dollar, costs its own price",
	}); err != nil {
		return ByokKey{}, fmt.Errorf("confirm the provider key's unit value: %w", err)
	}
	for _, m := range models {
		formats, err := store.FormatNames(m.Formats)
		if err != nil || len(formats) == 0 {
			continue // a model this node cannot name a call format for is not served by the key
		}
		if err := q.UpsertProjectedModel(ctx, tx, store.UpsertProjectedModelParams{
			SupplierID: id, SupplierGroupID: group, UpstreamName: m.Upstream, ModelID: &m.ID, Formats: formats,
		}); err != nil {
			return ByokKey{}, fmt.Errorf("map %s: %w", m.Upstream, err)
		}
		endpoint, err := q.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
			SupplierID: id, SupplierGroupID: group, ModelID: m.ID, UpstreamName: m.Upstream, Formats: formats,
		})
		if err != nil {
			return ByokKey{}, fmt.Errorf("make the endpoint for %s: %w", m.Upstream, err)
		}
		schedule, err := q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
			SupplierID: id, EndpointID: endpoint, NativeUnit: "usd", Schedule: m.Schedule,
			Basis: "confirmed", Fingerprint: "byok",
		})
		if err != nil {
			return ByokKey{}, fmt.Errorf("price the endpoint for %s: %w", m.Upstream, err)
		}
		if err := q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: endpoint, CostScheduleID: &schedule}); err != nil {
			return ByokKey{}, fmt.Errorf("put the endpoint for %s in service: %w", m.Upstream, err)
		}
	}
	if err := a.recordByok(ctx, tx, r.Actor, "supplier.byok_create", r.Site, r.Account, id, "an account added its own provider key", map[string]any{
		"provider": r.Provider, "hint": hintOf(key), "fallback": r.Fallback, "models": len(models),
	}); err != nil {
		return ByokKey{}, err
	}
	row, _, err := a.byokRow(ctx, tx, r.Site, r.Account, id)
	if err != nil {
		return ByokKey{}, err
	}
	return byokOf(row), nil
}

// hintOf is the last four characters of a key, which is what the console shows after the key is saved.
func hintOf(key string) string {
	if len(key) <= 4 {
		return key
	}
	return key[len(key)-4:]
}

// ListByok reads the account's provider keys, newest first.
func (*Admin) ListByok(ctx context.Context, tx pg.Tx, site, account uuid.UUID) ([]ByokKey, error) {
	rows, err := store.New().ListByokKeys(ctx, tx, store.ListByokKeysParams{SiteID: &site, AccountID: &account})
	if err != nil {
		return nil, fmt.Errorf("read the provider keys: %w", err)
	}
	out := make([]ByokKey, len(rows))
	for i, r := range rows {
		out[i] = byokOf(r)
	}
	return out, nil
}

// byokOf is the console's view of a row. The hint is the credential's, and a credential an account made always has one;
// the empty fallback is only for a row written before the column existed, which no BYOK row is.
func byokOf(r store.ListByokKeysRow) ByokKey {
	k := ByokKey{
		ID: r.ID, Name: r.Name, Provider: r.Kind, Status: r.CredentialStatus, Enabled: r.CredentialStatus == "active",
		Rejected: r.CredentialStatus == "rejected", Fallback: r.ByokFallback, CreatedAt: r.CreatedAt,
	}
	if r.Hint != nil {
		k.Hint = *r.Hint
	}
	return k
}

// SetByokEnabled switches a key on or off. Enabling a key the provider refused does not make it work: only a successful
// test clears rejected. The account's own scope reads the row, so another tenant's id is ErrByokNotFound.
func (a *Admin) SetByokEnabled(ctx context.Context, tx pg.Tx, site, account, id uuid.UUID, enabled bool, actor audit.Actor) (ByokKey, error) {
	row, credential, err := a.byokRow(ctx, tx, site, account, id)
	if err != nil {
		return ByokKey{}, err
	}
	if err := a.lockByok(ctx, tx, id, credential); err != nil {
		return ByokKey{}, err
	}
	if row, _, err = a.byokRow(ctx, tx, site, account, id); err != nil {
		return ByokKey{}, err
	}
	status := row.CredentialStatus
	switch {
	case !enabled && status == "active":
		status = "disabled"
	case enabled && status == "disabled":
		status = "active"
	}
	// A rejected key stays rejected: the switch cannot claim a key works when the provider refused it. Only a test
	// that succeeds clears it.
	if row.CredentialStatus != status {
		if err := store.New().SetByokCredentialStatus(ctx, tx, store.SetByokCredentialStatusParams{
			SupplierID: id, CredentialID: credential, Status: status,
		}); err != nil {
			return ByokKey{}, fmt.Errorf("switch the provider key: %w", err)
		}
		action := "supplier.byok_disable"
		if enabled {
			action = "supplier.byok_enable"
		}
		if err := a.recordByok(ctx, tx, actor, action, site, account, id, "the account switched its provider key", map[string]any{"hint": hint(row)}); err != nil {
			return ByokKey{}, err
		}
		row.CredentialStatus = status
	}
	return byokOf(row), nil
}

// RemoveByok takes a key out of service for good: the supplier becomes removed, the credential erased and its
// ciphertext cleared. Attempt cost rows stay, because they point at the credential.
func (a *Admin) RemoveByok(ctx context.Context, tx pg.Tx, site, account, id uuid.UUID, actor audit.Actor) error {
	row, credential, err := a.byokRow(ctx, tx, site, account, id)
	if err != nil {
		return err
	}
	if err := a.lockByok(ctx, tx, id, credential); err != nil {
		return err
	}
	if err := store.New().SetSupplierStatusBumpingConfigGen(ctx, tx, store.SetSupplierStatusBumpingConfigGenParams{
		ID: id, Status: "removed", StatusReason: new("the account removed its provider key"),
	}); err != nil {
		return fmt.Errorf("remove the provider key: %w", err)
	}
	if err := store.New().EraseCredentialsOf(ctx, tx, id); err != nil {
		return fmt.Errorf("erase the provider key: %w", err)
	}
	return a.recordByok(ctx, tx, actor, "supplier.byok_remove", site, account, id, "the account removed its provider key", map[string]any{"hint": hint(row)})
}

// byokRow reads one key of the account, with the credential the console acts on.
func (*Admin) byokRow(ctx context.Context, tx pg.Tx, site, account, id uuid.UUID) (store.ListByokKeysRow, uuid.UUID, error) {
	rows, err := store.New().ListByokKeys(ctx, tx, store.ListByokKeysParams{SiteID: &site, AccountID: &account})
	if err != nil {
		return store.ListByokKeysRow{}, uuid.UUID{}, fmt.Errorf("read the provider key: %w", err)
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		credential, err := store.New().CredentialOfByok(ctx, tx, id)
		if err != nil {
			return store.ListByokKeysRow{}, uuid.UUID{}, fmt.Errorf("read the provider key's credential: %w", err)
		}
		return row, credential, nil
	}
	return store.ListByokKeysRow{}, uuid.UUID{}, ErrByokNotFound
}

// lockByok serializes the console's writes on one key's credential. A test's answer and the switch must not cross:
// without the lock, a switch that read "disabled" could write "active" over a test that just found the key refused.
func (*Admin) lockByok(ctx context.Context, tx pg.Tx, id, credential uuid.UUID) error {
	rows, err := store.New().LockByokCredential(ctx, tx, store.LockByokCredentialParams{
		SupplierID: id, CredentialID: credential,
	})
	if err != nil {
		return fmt.Errorf("lock the provider key: %w", err)
	}
	if rows == 0 {
		return ErrByokNotFound
	}
	return nil
}

func hint(r store.ListByokKeysRow) string {
	if r.Hint == nil {
		return ""
	}
	return *r.Hint
}

// RevealByok opens the sealed key for a test. The caller must not keep it, log it or return it: it goes into one
// Authorization header and no further. The credential is the account's own, read through the account's row.
func (a *Admin) RevealByok(ctx context.Context, tx pg.Tx, site, account, id uuid.UUID) (ByokKey, uuid.UUID, secret.Secret, error) {
	row, credential, err := a.byokRow(ctx, tx, site, account, id)
	if err != nil {
		return ByokKey{}, uuid.UUID{}, secret.Secret{}, err
	}
	sealed, err := store.New().GetCredentialSecret(ctx, tx, credential)
	if err != nil {
		return ByokKey{}, uuid.UUID{}, secret.Secret{}, fmt.Errorf("read the provider key's secret: %w", err)
	}
	if sealed.KeyVersion == nil {
		return ByokKey{}, uuid.UUID{}, secret.Secret{}, fmt.Errorf("%w: the key holds no secret", ErrByokNotFound)
	}
	plain, err := a.keys.Open(secret.Sealed{
		Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDEK: sealed.WrappedDek,
		KeyVersion: uint32(*sealed.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
	}, secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	if err != nil {
		return ByokKey{}, uuid.UUID{}, secret.Secret{}, fmt.Errorf("open the provider key: %w", err)
	}
	return byokOf(row), credential, plain, nil
}

// SetByokTestResult records what a test found: a key the provider accepts becomes active, one it refuses becomes
// rejected and stays out of routing until a later test succeeds. Nothing else changes it back.
func (a *Admin) SetByokTestResult(ctx context.Context, tx pg.Tx, site, account, id uuid.UUID, ok bool, actor audit.Actor) (ByokKey, error) {
	_, current, err := a.byokRow(ctx, tx, site, account, id)
	if err != nil {
		return ByokKey{}, err
	}
	if err := a.lockByok(ctx, tx, id, current); err != nil {
		return ByokKey{}, err
	}
	row, _, err := a.byokRow(ctx, tx, site, account, id)
	if err != nil {
		return ByokKey{}, err
	}
	status := "rejected"
	if ok {
		status = "active"
	}
	if err := store.New().SetByokCredentialStatus(ctx, tx, store.SetByokCredentialStatusParams{
		SupplierID: id, CredentialID: current, Status: status,
	}); err != nil {
		return ByokKey{}, fmt.Errorf("record the test: %w", err)
	}
	if err := a.recordByok(ctx, tx, actor, "supplier.byok_test", site, account, id, "the account tested its provider key", map[string]any{
		"ok": ok, "hint": hint(row),
	}); err != nil {
		return ByokKey{}, err
	}
	row, _, err = a.byokRow(ctx, tx, site, account, id)
	return byokOf(row), err
}
