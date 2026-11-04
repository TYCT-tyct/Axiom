package suppliers

import (
	"context"
	"errors"
	"fmt"
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
	// A new login is a new chance to make the keys a narrow token could not: the groups wait for nothing but the sync.
	if err := q.ClearKeyErrors(ctx, tx, id); err != nil {
		return Supplier{}, fmt.Errorf("clear the key errors: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.replace_login", id, reason, map[string]any{"from": row.Status}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, id)
}
