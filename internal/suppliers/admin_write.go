package suppliers

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// originPattern is the origin a supplier is connected at: https, a host in lower case, a port if it is not the default.
var originPattern = regexp.MustCompile(`^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizeOrigin is the origin as it is stored: lower case, with the port a decimal number from 1 to 65535 and left out
// when it is the one https has by default. A supplier is identified by its origin and account, and its credentials go
// only there, so the same site written two ways must not be two suppliers. It reads the text only; CheckOrigin asks the
// network.
func NormalizeOrigin(raw string) (string, error) {
	raw = strings.ToLower(raw)
	u, err := url.Parse(raw)
	if err != nil || !originPattern.MatchString(raw) || u.Hostname() == "" {
		return "", ErrBadOrigin
	}
	origin := "https://" + u.Hostname()
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return "", ErrBadOrigin
		}
		if port != 443 {
			origin += ":" + strconv.Itoa(port)
		}
	}
	return origin, nil
}

// CheckOrigin is whether the outbound client lets a connection through to the origin's host. It resolves the name, so it
// answers for the network as it is now: a request that has been carried out does not ask it again.
func (a *Admin) CheckOrigin(ctx context.Context, origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return ErrBadOrigin
	}
	if err := a.egress.Check(ctx, u.Hostname()); errors.Is(err, egress.ErrAddressDenied) {
		return fmt.Errorf("%w: %w", ErrAddressRefused, err)
	} else if err != nil {
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return nil
}

// checkedOrigin is NormalizeOrigin and CheckOrigin together.
func (a *Admin) checkedOrigin(ctx context.Context, raw string) (string, error) {
	origin, err := NormalizeOrigin(raw)
	if err != nil {
		return "", err
	}
	if err := a.CheckOrigin(ctx, origin); err != nil {
		return "", err
	}
	return origin, nil
}

// Detection is what a probe found out about a site.
type Detection struct {
	Origin string
	// Kind is the adapter that fits; empty when none recognised the site.
	Kind, Generation, Version string
	LoginFields               []string
}

// detectTimeout bounds the probes of one detection, which are requests to a third party.
const detectTimeout = 45 * time.Second

// Detect asks each adapter in turn whether the site is a system of its kind, through the outbound client, which refuses
// private and internal addresses; nothing is stored. A site no adapter recognises is a Detection with no Kind.
func (a *Admin) Detect(ctx context.Context, raw string) (Detection, error) {
	origin, err := a.checkedOrigin(ctx, raw)
	if err != nil {
		return Detection{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	var answered bool
	for _, kind := range a.registry.Kinds() {
		ad, _ := a.registry.Get(kind)
		onboarding, ok := ad.(adapter.Onboarding)
		if !ok {
			continue
		}
		c, err := client.New(origin, a.egress, client.NewLimiter(ad.Buckets(), a.now), a.now)
		if err != nil {
			return Detection{}, err
		}
		found, err := ad.Detect(ctx, c)
		switch {
		case errors.Is(err, adapter.ErrNotRecognised):
			answered = true
		case err != nil: // this adapter could not tell; another may
		default:
			return Detection{
				Origin: origin, Kind: string(kind), Generation: found.Generation, Version: found.Version,
				LoginFields: loginFields(onboarding.LoginFields(found.Captcha)),
			}, nil
		}
	}
	if !answered {
		return Detection{}, ErrUnreachable
	}
	return Detection{Origin: origin, LoginFields: []string{}}, nil
}

func loginFields(fields []adapter.LoginField) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f)
	}
	return names
}

// ConnectRequest is a supplier to connect. Origin is as Origin returned it.
type ConnectRequest struct {
	Name, Kind, Origin, UpstreamAccount string
	SettlementCurrency                  platform.Currency
	Login                               Credentials
	Reason                              string
}

// loginAt is where a supplier's login is sealed to: the row it is stored in, so that it opens nowhere else.
func loginAt(supplier uuid.UUID) secret.Location {
	return secret.Location{Table: "suppliers.supplier_logins", RowID: supplier.String(), Purpose: "login"}
}

// newLogin checks the credentials against what the kind takes and makes the login to seal.
func newLogin(o adapter.Onboarding, c Credentials) (adapter.Login, error) {
	takes := o.LoginFields(false)
	for _, field := range c.Present() {
		if !slices.Contains(takes, field) {
			return adapter.Login{}, &LoginError{Field: string(field), Reason: "is not used by this kind of system"}
		}
	}
	login, err := o.NewLogin(c)
	if loginErr, ok := errors.AsType[*adapter.LoginError](err); ok {
		return adapter.Login{}, &LoginError{Field: string(loginErr.Field), Reason: loginErr.Reason}
	}
	return login, err
}

// Connect makes a supplier in `connecting` with its login sealed. The sync logs in, reads the groups, keys and models
// and moves it on; until then it takes no traffic. An origin and account already connected is ErrSupplierExists.
func (a *Admin) Connect(ctx context.Context, tx pg.Tx, actor audit.Actor, r ConnectRequest) (Supplier, error) {
	o, err := a.onboarding(r.Kind)
	if err != nil {
		return Supplier{}, err
	}
	login, err := newLogin(o, r.Login)
	if err != nil {
		return Supplier{}, err
	}
	id, err := store.New().InsertSupplier(ctx, tx, store.InsertSupplierParams{
		OwnerScope: "platform", Name: r.Name, Kind: r.Kind, Origin: r.Origin, UpstreamAccount: r.UpstreamAccount,
		NativeUnit: o.NativeUnit(), SettlementCurrency: r.SettlementCurrency.String(), MgmtEgressID: string(egress.Direct),
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Supplier{}, ErrSupplierExists
	}
	if err != nil {
		return Supplier{}, fmt.Errorf("connect the supplier: %w", err)
	}
	sealed := a.keys.Seal(secret.New(string(login.Plaintext)), loginAt(id))
	var expires *time.Time
	if !login.ExpiresAt.IsZero() {
		expires = &login.ExpiresAt
	}
	if err := store.New().InsertSupplierLogin(ctx, tx, store.InsertSupplierLoginParams{
		SupplierID: id, Kind: login.Kind, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK,
		KeyVersion: int64(sealed.KeyVersion), ExpiresAt: expires,
	}); err != nil {
		return Supplier{}, fmt.Errorf("seal the login: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.connect", id, r.Reason, map[string]any{
		"kind": r.Kind, "origin": r.Origin, "native_unit": o.NativeUnit(), "settlement_currency": r.SettlementCurrency.String(),
	}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}

// ReplaceLogin seals a new login in place of the old one and has the sync log in with it: the supplier is `connecting`
// again, and the state the old login left behind — a supplier it stopped, a task it halted — is put back to work
// (persist.Wake). The address and the account do not change. The audit entry names the actor and the reason, never the
// login.
func (a *Admin) ReplaceLogin(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, c Credentials, reason string) (Supplier, error) {
	// The state is woken first, so that its row is taken before the supplier's, the order every writer of the two takes
	// them in (persist.Wake). A change refused below rolls the wake back with the rest.
	if err := persist.Wake(ctx, tx, id, a.now()); err != nil {
		return Supplier{}, fmt.Errorf("wake the sync: %w", err)
	}
	row, err := a.locked(ctx, tx, id)
	if err != nil {
		return Supplier{}, err
	}
	o, err := a.onboarding(row.Kind)
	if err != nil {
		return Supplier{}, err
	}
	login, err := newLogin(o, c)
	if err != nil {
		return Supplier{}, err
	}
	sealed := a.keys.Seal(secret.New(string(login.Plaintext)), loginAt(id))
	var expires *time.Time
	if !login.ExpiresAt.IsZero() {
		expires = &login.ExpiresAt
	}
	q := store.New()
	if err := q.ReplaceSupplierLogin(ctx, tx, store.ReplaceSupplierLoginParams{
		SupplierID: id, Kind: login.Kind, Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK,
		KeyVersion: int64(sealed.KeyVersion), ExpiresAt: expires,
	}); err != nil {
		return Supplier{}, fmt.Errorf("replace the login: %w", err)
	}
	if err := q.SetSupplierStatusBumpingConfigGen(ctx, tx, store.SetSupplierStatusBumpingConfigGenParams{ID: id, Status: string(Connecting)}); err != nil {
		return Supplier{}, fmt.Errorf("set the status: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.replace_login", id, reason, map[string]any{"from": row.Status}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}

var unitValue = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,15})?$`)

// ConfirmUnitValue records what one unit of the supplier's balance is worth, in its settlement currency. A later value
// applies from now: the earlier ones stay, and what was costed keeps its own.
func (a *Admin) ConfirmUnitValue(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, value, reason string) (Supplier, error) {
	if !unitValue.MatchString(value) {
		return Supplier{}, ErrUnitValue
	}
	if v, _ := new(big.Rat).SetString(value); v.Sign() <= 0 || v.Cmp(big.NewRat(1000, 1)) > 0 {
		return Supplier{}, ErrUnitValue
	}
	row, err := a.locked(ctx, tx, id)
	if err != nil {
		return Supplier{}, err
	}
	if err := store.New().InsertUnitValue(ctx, tx, store.InsertUnitValueParams{SupplierID: id, Value: value, Actor: actor.String(), Reason: reason}); err != nil {
		return Supplier{}, fmt.Errorf("keep the unit value: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.unit_value", id, reason, map[string]any{"value": value, "status": row.Status}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}

// codename is a name for a group that tells nothing of the supplier: a letter and eight characters from the alphabet
// of base 32, which a person can say aloud. The format is a decision of this code until the product names one.
func codename() string {
	b := make([]byte, 5)
	rand.Read(b)                                                       // Since Go 1.24 crypto/rand.Read never returns an error; it crashes the program instead.
	return "g" + strings.ToLower(base32.StdEncoding.EncodeToString(b)) // five bytes are eight characters, unpadded
}

// SelectGroup chooses whether a supplier's group is used. The first time it is selected it gets its code name, which
// never changes; unselecting takes it out of routing and keeps the name. The sync makes the endpoints of the models.
func (a *Admin) SelectGroup(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, selected bool, reason string) (Group, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Group{}, err
	}
	q := store.New()
	rows, err := q.LockGroup(ctx, tx, store.LockGroupParams{ID: group, SupplierID: supplier})
	switch {
	case err != nil:
		return Group{}, fmt.Errorf("lock the group: %w", err)
	case len(rows) == 0:
		return Group{}, ErrNoGroup
	case selected && !rows[0].Present:
		return Group{}, ErrGroupGone
	}
	if rows[0].Selected != selected {
		if err := a.chooseGroup(ctx, tx, group, selected, rows[0].Codename == nil); err != nil {
			return Group{}, err
		}
		if err := q.BumpConfigGen(ctx, tx, supplier); err != nil {
			return Group{}, fmt.Errorf("raise the generation: %w", err)
		}
		action := "supplier_group.unselect"
		if selected {
			action = "supplier_group.select"
		}
		if err := a.record(ctx, tx, actor, action, supplier, reason, map[string]any{"group": group.String()}); err != nil {
			return Group{}, err
		}
	}
	return a.group(ctx, tx, supplier, group)
}

// chooseGroup writes the selection; a group selected for the first time gets a code name. A name is 40 random bits, so a
// name another group has is not to be expected, and if it happens the table refuses it, the change fails whole, and
// asking again makes another.
func (*Admin) chooseGroup(ctx context.Context, tx pg.Tx, group uuid.UUID, selected, nameless bool) error {
	params := store.SelectGroupParams{ID: group, Selected: selected}
	if selected && nameless {
		name := codename()
		params.Codename = &name
	}
	if err := store.New().SelectGroup(ctx, tx, params); err != nil {
		return fmt.Errorf("select the group: %w", err)
	}
	return nil
}

// group reads one group of a supplier.
func (*Admin) group(ctx context.Context, tx pg.Tx, supplier, id uuid.UUID) (Group, error) {
	rows, err := store.New().GetGroupOf(ctx, tx, store.GetGroupOfParams{SupplierID: supplier, ID: id})
	if err != nil {
		return Group{}, fmt.Errorf("read the group: %w", err)
	}
	if len(rows) == 0 {
		return Group{}, ErrNoGroup
	}
	g := rows[0]
	return groupOf(g.ID, g.UpstreamRef, g.Name, g.Ratio, g.Facts, g.Selected, g.Codename, g.Present)
}

// defaultMaxConcurrency is how many calls a key serves at once until a person sets another number.
const defaultMaxConcurrency = 4

// BindKey binds one of the supplier's keys to a selected group, by the supplier's own ID of it. The credential is
// `binding` until the sync has read the key's text from the supplier and sealed it. A key is bound once.
func (a *Admin) BindKey(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, keyRef, reason string) (Credential, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Credential{}, err
	}
	g, err := a.group(ctx, tx, supplier, group)
	switch {
	case err != nil:
		return Credential{}, err
	case !g.Selected:
		return Credential{}, ErrGroupNotSelected
	}
	id := uuid.NewV7()
	err = store.New().InsertBindingCredential(ctx, tx, store.InsertBindingCredentialParams{
		ID: id, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: keyRef, MaxConcurrency: defaultMaxConcurrency,
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Credential{}, ErrKeyBound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("bind the key: %w", err)
	}
	if err := store.New().BumpConfigGen(ctx, tx, supplier); err != nil {
		return Credential{}, fmt.Errorf("raise the generation: %w", err)
	}
	if err := a.record(ctx, tx, actor, "credential.bind", supplier, reason, map[string]any{"group": group.String(), "credential": id.String(), "upstream_key_ref": keyRef}); err != nil {
		return Credential{}, err
	}
	return Credential{ID: id, Group: group, UpstreamKeyRef: keyRef, Status: "binding", MaxConcurrency: defaultMaxConcurrency}, nil
}

// Limits are a credential's concurrency and its ceiling on the upstream account's shared rolling minute in this node.
type Limits struct {
	// MaxConcurrency is the calls in flight at once; at least one, or the key would serve nothing.
	MaxConcurrency int
	// RequestsPerMinute is the account RPM ceiling for selecting this credential; nil adds no ceiling.
	RequestsPerMinute *int
}

// maxLimit is the largest concurrency the int4 column can hold; a number above it would fail there.
const maxLimit = math.MaxInt32

// MaxRequestsPerMinute is the largest per-minute ceiling a credential may have: finite so the rolling window's counts
// and the memory of unlimited traffic stay bounded (issue #382).
const MaxRequestsPerMinute = 100_000

// valid is whether the columns can hold these limits: concurrency 1 to 2³¹-1, and requests a minute 1 to
// MaxRequestsPerMinute or none.
func (l Limits) valid() bool {
	if l.MaxConcurrency < 1 || l.MaxConcurrency > maxLimit {
		return false
	}
	return l.RequestsPerMinute == nil || *l.RequestsPerMinute >= 1 && *l.RequestsPerMinute <= MaxRequestsPerMinute
}

// SetLimits sets how much of a credential this node gives out, and records it. A credential already as asked changes
// nothing and records nothing. The counts themselves are each node's own, in memory (README.md「并发与速率」), so the
// change reaches this node at its next Reload and the others at theirs.
func (a *Admin) SetLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, credentialID uuid.UUID, limits Limits, reason string) (Credential, error) {
	if !limits.valid() {
		return Credential{}, ErrLimits
	}
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Credential{}, err
	}
	concurrency := int32(limits.MaxConcurrency) //nolint:gosec // valid checked it is 1 to 2³¹-1
	var perMinute *int32
	if limits.RequestsPerMinute != nil {
		n := int32(*limits.RequestsPerMinute) //nolint:gosec // valid checked it is 1 to MaxRequestsPerMinute
		perMinute = &n
	}
	q := store.New()
	rows, err := q.GetCredentialOf(ctx, tx, store.GetCredentialOfParams{SupplierID: supplier, ID: credentialID})
	if err != nil {
		return Credential{}, fmt.Errorf("read the credential: %w", err)
	}
	if len(rows) == 0 {
		return Credential{}, ErrNoCredential
	}
	row := rows[0]
	if row.Status == "erased" {
		return Credential{}, ErrStatusLocked
	}
	if row.MaxConcurrency == concurrency && sameLimit(row.RequestsPerMinute, perMinute) {
		return credentialOf(row.ID, row.SupplierGroupID, row.UpstreamKeyRef, row.Status, row.CooldownUntil, row.ExpiresAt, row.MaxConcurrency, row.RequestsPerMinute), nil
	}
	changed, err := q.SetCredentialLimits(ctx, tx, store.SetCredentialLimitsParams{
		SupplierID: supplier, ID: credentialID, MaxConcurrency: concurrency, RequestsPerMinute: perMinute,
	})
	if err != nil {
		return Credential{}, fmt.Errorf("set the limits: %w", err)
	}
	if changed == 0 { // the row was there a moment ago, and the supplier's lock is held
		return Credential{}, ErrNoCredential
	}
	if err := a.record(ctx, tx, actor, "credential.limits", supplier, reason, map[string]any{
		"credential":               row.ID.String(),
		"max_concurrency_from":     int(row.MaxConcurrency),
		"max_concurrency_to":       int(concurrency),
		"requests_per_minute_from": limited(row.RequestsPerMinute),
		"requests_per_minute_to":   limited(perMinute),
	}); err != nil {
		return Credential{}, err
	}
	return credentialOf(row.ID, row.SupplierGroupID, row.UpstreamKeyRef, row.Status, row.CooldownUntil, row.ExpiresAt, concurrency, perMinute), nil
}

// sameLimit is whether two per-minute limits are the same, absent counting as a value.
func sameLimit(a, b *int32) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Mapping is an alias: the supplier's name for a model, in a group or in the whole supplier.
type Mapping struct {
	ID, Supplier uuid.UUID
	Group        *uuid.UUID
	UpstreamName string
	Model        uuid.UUID
}

// MapModel makes an alias: in the scope, the name means the model. An alias is never changed; a wrong one is corrected
// by a new alias in the narrower scope. A name that has an alias in that scope is ErrAliasExists. The model is the
// catalog's, which the caller has checked.
func (a *Admin) MapModel(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, group *uuid.UUID, name string, model uuid.UUID, reason string) (Mapping, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Mapping{}, err
	}
	if group != nil {
		if _, err := a.group(ctx, tx, supplier, *group); err != nil {
			return Mapping{}, err
		}
	}
	id, err := store.New().InsertAlias(ctx, tx, store.InsertAliasParams{SupplierID: supplier, SupplierGroupID: group, UpstreamName: name, ModelID: model})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Mapping{}, ErrAliasExists
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("make the alias: %w", err)
	}
	detail := map[string]any{"alias": id.String(), "upstream_name": name, "model": model.String()}
	if group != nil {
		detail["group"] = group.String()
	}
	if err := a.record(ctx, tx, actor, "supplier_alias.create", supplier, reason, detail); err != nil {
		return Mapping{}, err
	}
	return Mapping{ID: id, Supplier: supplier, Group: group, UpstreamName: name, Model: model}, nil
}

// PendingMapping is an upstream name waiting to be mapped.
type PendingMapping struct {
	Supplier, Group uuid.UUID
	GroupCodename   string
	UpstreamName    string
	Formats         []string
	FirstSeen       time.Time
}

// PendingQuery chooses the names to list, oldest first. A zero Supplier is every supplier; After is the last name of
// the page before, nil for the first page.
type PendingQuery struct {
	Supplier uuid.UUID
	After    *PendingMapping
	PageSize int
}

// PendingMappings lists a page of names that map to no model; more tells whether names remain beyond it.
func (*Admin) PendingMappings(ctx context.Context, tx pg.Tx, q PendingQuery) (names []PendingMapping, more bool, err error) {
	params := store.PendingMappingsPageParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Supplier != (uuid.UUID{}) {
		params.SupplierID = &q.Supplier
	}
	if after := q.After; after != nil {
		params.AfterSeen, params.AfterGroup, params.AfterName = &after.FirstSeen, &after.Group, &after.UpstreamName
	}
	rows, err := store.New().PendingMappingsPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the names to map: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	names = make([]PendingMapping, len(rows))
	for i, r := range rows {
		names[i] = PendingMapping{
			Supplier: r.SupplierID, Group: r.SupplierGroupID, GroupCodename: deref(r.Codename), UpstreamName: r.UpstreamName,
			Formats: r.Formats, FirstSeen: r.FirstSeenAt,
		}
	}
	return names, more, nil
}
