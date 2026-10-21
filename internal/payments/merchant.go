package payments

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

var (
	// ErrNotFound is a merchant the scope does not see.
	ErrNotFound = errors.New("payments: no such merchant account")
	// ErrStale refuses a change decided against a superseded revision.
	ErrStale = errors.New("payments: the merchant account changed since it was read")
	// ErrTampered is a merchant whose stored settings are not the ones sealed with its secrets: they were changed
	// outside a save, so the merchant is unusable until its secrets are entered again.
	ErrTampered = errors.New("payments: the merchant account's settings do not match its sealed hash")
)

// FieldError is invalid input, pointing at its field by JSON pointer without echoing the value.
type FieldError struct{ Field, Reason string }

func (e *FieldError) Error() string { return "payments: " + e.Field + " " + e.Reason }

// MerchantInput is a merchant account as an administrator saves it.
type MerchantInput struct {
	ID            MerchantID // zero creates one
	Rev           int64      // the revision a change was decided against; ignored when creating
	Site          uuid.UUID
	Kind          Kind
	Name          string
	Enabled       bool
	Currency      platform.Currency
	TTLSeconds    int32
	RefundEnabled bool
	UserRefund    bool
	Sort          int32
	Settings      []byte                   // the plugin's JSON; stored canonical
	Secrets       map[string]secret.Secret // nil keeps the sealed ones; required when creating
	Methods       []MethodInput
}

// MethodInput is one payment method of a merchant. Limits are totals in minor units of the merchant's currency; nil
// is no limit.
type MethodInput struct {
	Method                           Method
	Upstream                         string // the gateway's code for the method
	Fee                              Fee
	SingleMin, SingleMax, DailyLimit *int64
	Enabled                          bool
}

// Merchant is a saved merchant account, without its secrets.
type Merchant struct {
	PaymentsMerchant
	Settings string
	Methods  []PaymentsMerchantMethod
}

var methodPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,31}$`)

// sealedConfig is what a merchant's ciphertext holds: the secrets, and the hash of the settings they were saved with,
// so that settings changed without the secrets, a gateway address above all, fail to open (README.md「不变量」).
type sealedConfig struct {
	SettingsSHA256 []byte            `json:"settings_sha256"`
	Secrets        map[string]string `json:"secrets"`
}

func configAt(id MerchantID) secret.Location {
	return secret.Location{Table: "payments.merchant_configs", RowID: uuid.UUID(id).String(), Purpose: "config"}
}

func (in MerchantInput) check() error {
	switch {
	case utf8.RuneCountInString(strings.TrimSpace(in.Name)) == 0 || utf8.RuneCountInString(in.Name) > 100:
		return &FieldError{"/name", "must have 1 to 100 characters"}
	case in.Currency == platform.Currency{}:
		return &FieldError{"/currency", "is required"}
	case in.TTLSeconds < 60 || in.TTLSeconds > 86400:
		return &FieldError{"/ttl_seconds", "must be from 60 to 86400"}
	case len(in.Methods) == 0 || len(in.Methods) > 32:
		return &FieldError{"/methods", "must have 1 to 32 methods"}
	}
	for i, m := range in.Methods {
		if err := m.check(); err != nil {
			err.Field = fmt.Sprintf("/methods/%d/%s", i, err.Field)
			return err
		}
		if slices.ContainsFunc(in.Methods[:i], func(o MethodInput) bool { return o.Method == m.Method }) {
			return &FieldError{fmt.Sprintf("/methods/%d/method", i), "is given twice"}
		}
	}
	return nil
}

func (m MethodInput) check() *FieldError {
	switch {
	case !methodPattern.MatchString(string(m.Method)):
		return &FieldError{"method", "must be a lower-case code such as alipay or usdt.tron"}
	case m.Upstream == "" || utf8.RuneCountInString(m.Upstream) > 64:
		return &FieldError{"upstream", "must have 1 to 64 characters"}
	case m.Fee.PPM < 0 || m.Fee.PPM > maxFeePPM:
		return &FieldError{"fee_ppm", "must be from 0 to 500000"}
	case m.Fee.Fixed < 0:
		return &FieldError{"fee_fixed", "must not be negative"}
	}
	return m.checkLimits()
}

func (m MethodInput) checkLimits() *FieldError {
	positive := func(v *int64) bool { return v == nil || *v > 0 }
	switch {
	case !positive(m.SingleMin) || !positive(m.SingleMax) || !positive(m.DailyLimit):
		return &FieldError{"single_min", "limits must be positive"}
	case m.SingleMin != nil && m.SingleMax != nil && *m.SingleMin > *m.SingleMax:
		return &FieldError{"single_max", "must not be below single_min"}
	}
	return nil
}

// SaveMerchant creates or changes a merchant account in the caller's site- or platform-scoped transaction: the plugin
// validates the configuration, the secrets are sealed with the hash of the settings, and the change is audited.
// Refusing changes to the identity fields while orders are open comes with orders (README.md「实现拆分」E1-2b).
func (p *Payments) SaveMerchant(ctx context.Context, tx pg.Tx, actor audit.Actor, in MerchantInput) (Merchant, error) {
	if err := in.check(); err != nil {
		return Merchant{}, err
	}
	plugin, ok := p.plugins[in.Kind]
	if !ok {
		return Merchant{}, &FieldError{"/kind", "is not a channel this program has"}
	}
	var raw jsonx.Raw
	if err := jsonx.Unmarshal(in.Settings, &raw); err != nil || raw.Kind() != '{' {
		return Merchant{}, &FieldError{"/settings", "must be a JSON object"}
	}
	settings, err := jsonx.MarshalCanonical(raw)
	if err != nil {
		return Merchant{}, &FieldError{"/settings", "must be a JSON object"}
	}
	created := in.ID == MerchantID{}
	rev := int64(1)
	if created {
		in.ID = MerchantID(uuid.NewV7())
		if in.Secrets == nil {
			return Merchant{}, &FieldError{"/secrets", "are required"}
		}
	} else if rev, err = p.lockForSave(ctx, tx, &in, settings); err != nil {
		return Merchant{}, err
	}
	if err = p.validate(plugin, in, settings); err != nil {
		return Merchant{}, err
	}
	if err = p.write(ctx, tx, in, settings, rev, created); err != nil {
		return Merchant{}, err
	}
	return recordSave(ctx, tx, actor, in, created)
}

// lockForSave locks the merchant, checks the revision, refuses a change of its identity while an order of it is still
// in flight and, when no secrets are given, takes the sealed ones: their hash must match the stored settings, or a
// tampered gateway address would be sealed in as genuine.
func (p *Payments) lockForSave(ctx context.Context, tx pg.Tx, in *MerchantInput, settings []byte) (int64, error) {
	rows, err := queries.LockMerchant(ctx, tx, uuid.UUID(in.ID))
	if err != nil {
		return 0, fmt.Errorf("payments: lock merchant: %w", err)
	}
	if len(rows) == 0 {
		return 0, ErrNotFound
	}
	r := rows[0]
	if r.Rev != in.Rev {
		return 0, ErrStale
	}
	if Kind(r.Kind) != in.Kind {
		return 0, &FieldError{"/kind", "cannot change"}
	}
	in.Site = r.SiteID
	if err = checkIdentity(ctx, tx, r, *in, settings); err != nil {
		return 0, err
	}
	if in.Secrets == nil {
		if in.Secrets, err = p.unseal(in.ID, sealedOf(r.Ciphertext, r.Nonce, r.WrappedDek, r.KeyVersion), r.Settings); err != nil {
			return 0, err
		}
	}
	return r.Rev + 1, nil
}

// checkIdentity refuses a change of what a merchant account is while one of its orders is still in flight: the keys,
// the gateway address and the currency it is paid in must keep working for that order, and the same account must be
// what the callback and the query reach (docs/domains/payments.md「后台配置」).
func checkIdentity(ctx context.Context, tx pg.Tx, stored LockMerchantRow, in MerchantInput, settings []byte) error {
	open, err := queries.OpenMerchantOrders(ctx, tx, uuid.UUID(in.ID))
	if err != nil {
		return fmt.Errorf("payments: read the merchant account's orders in flight: %w", err)
	}
	if !open {
		return nil
	}
	var field string
	switch {
	case stored.Currency != in.Currency.String():
		field = "/currency"
	case stored.Settings != string(settings):
		field = "/settings"
	case in.Secrets != nil:
		field = "/secrets"
	default:
		return nil
	}
	return &FieldError{field, "cannot change while the merchant account has orders still in flight"}
}

// validate has the plugin open the configuration it would run with; every method must be one the channel offers.
func (p *Payments) validate(plugin Plugin, in MerchantInput, settings []byte) error {
	ch, err := plugin.Open(MerchantConfig{ID: in.ID, Currency: in.Currency, Settings: settings, Secrets: in.Secrets, Client: p.client})
	if err != nil {
		return &FieldError{"/settings", err.Error()}
	}
	for i, m := range in.Methods {
		if !slices.Contains(ch.Methods(), m.Method) {
			return &FieldError{fmt.Sprintf("/methods/%d/method", i), "is not offered by this channel"}
		}
	}
	return nil
}

func (p *Payments) write(ctx context.Context, tx pg.Tx, in MerchantInput, settings []byte, rev int64, created bool) error {
	m := InsertMerchantParams{
		ID: uuid.UUID(in.ID), SiteID: in.Site, Kind: string(in.Kind), Name: in.Name, Enabled: in.Enabled,
		Currency: in.Currency.String(), TtlSeconds: in.TTLSeconds, RefundEnabled: in.RefundEnabled, UserRefund: in.UserRefund, Sort: in.Sort,
	}
	var err error
	if created {
		err = queries.InsertMerchant(ctx, tx, m)
	} else {
		err = queries.UpdateMerchant(ctx, tx, UpdateMerchantParams{
			ID: m.ID, Name: m.Name, Enabled: m.Enabled, Currency: m.Currency,
			TtlSeconds: m.TtlSeconds, RefundEnabled: m.RefundEnabled, UserRefund: m.UserRefund, Sort: m.Sort,
		})
	}
	if err != nil {
		return fmt.Errorf("payments: save merchant: %w", err)
	}
	sealed, err := p.seal(in.ID, in.Secrets, settings)
	if err != nil {
		return err
	}
	err = queries.SaveMerchantConfig(ctx, tx, SaveMerchantConfigParams{
		MerchantID: m.ID, SiteID: in.Site, Settings: string(settings),
		Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: int64(sealed.KeyVersion), Rev: rev,
	})
	if err != nil {
		return fmt.Errorf("payments: save merchant config: %w", err)
	}
	keep := make([]string, len(in.Methods))
	for i, x := range in.Methods {
		keep[i] = string(x.Method)
		err = queries.SaveMethod(ctx, tx, SaveMethodParams{
			MerchantID: m.ID, SiteID: in.Site, Method: keep[i], Upstream: x.Upstream,
			FeePpm: x.Fee.PPM, FeeFixed: x.Fee.Fixed, SingleMin: x.SingleMin, SingleMax: x.SingleMax, DailyLimit: x.DailyLimit, Enabled: x.Enabled,
		})
		if err != nil {
			return fmt.Errorf("payments: save merchant method: %w", err)
		}
	}
	if err = queries.DeleteOtherMethods(ctx, tx, DeleteOtherMethodsParams{MerchantID: m.ID, Keep: keep}); err != nil {
		return fmt.Errorf("payments: save merchant methods: %w", err)
	}
	return nil
}

// recordSave audits the change, never the secrets, and reads back what was saved.
func recordSave(ctx context.Context, tx pg.Tx, actor audit.Actor, in MerchantInput, created bool) (Merchant, error) {
	action, methods := "merchant.update", make([]string, len(in.Methods))
	if created {
		action = "merchant.create"
	}
	for i, m := range in.Methods {
		methods[i] = string(m.Method)
	}
	err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action,
		Target: audit.Target{Module: "payments", Kind: "merchant", ID: in.ID.TypeID(), Site: in.Site},
		Detail: map[string]any{"name": in.Name, "kind": in.Kind, "currency": in.Currency.String(), "enabled": in.Enabled, "methods": methods},
	})
	if err != nil {
		return Merchant{}, err
	}
	return ReadMerchant(ctx, tx, in.ID)
}

// ReadMerchant reads a merchant account of the scope without its secrets.
func ReadMerchant(ctx context.Context, tx pg.Tx, id MerchantID) (Merchant, error) {
	rows, err := queries.ReadMerchantConfig(ctx, tx, uuid.UUID(id))
	if err != nil {
		return Merchant{}, fmt.Errorf("payments: read merchant: %w", err)
	}
	if len(rows) == 0 {
		return Merchant{}, ErrNotFound
	}
	r := rows[0]
	methods, err := queries.ListMethods(ctx, tx, []uuid.UUID{r.ID})
	if err != nil {
		return Merchant{}, fmt.Errorf("payments: read merchant methods: %w", err)
	}
	m := PaymentsMerchant{
		ID: r.ID, SiteID: r.SiteID, Kind: r.Kind, Name: r.Name, Enabled: r.Enabled, Currency: r.Currency, TtlSeconds: r.TtlSeconds,
		RefundEnabled: r.RefundEnabled, UserRefund: r.UserRefund, Sort: r.Sort, Rev: r.Rev, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	return Merchant{PaymentsMerchant: m, Settings: r.Settings, Methods: methods}, nil
}

func sealedOf(ciphertext, nonce, wrapped []byte, version int64) secret.Sealed {
	return secret.Sealed{Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrapped, KeyVersion: uint32(version)} //nolint:gosec // the column holds 1 to 2³²-1
}

func (p *Payments) seal(id MerchantID, secrets map[string]secret.Secret, settings []byte) (secret.Sealed, error) {
	sum := sha256.Sum256(settings)
	c := sealedConfig{SettingsSHA256: sum[:], Secrets: make(map[string]string, len(secrets))}
	for name, s := range secrets {
		c.Secrets[name] = s.Reveal()
	}
	plain, err := jsonx.MarshalCanonical(c)
	if err != nil {
		return secret.Sealed{}, fmt.Errorf("payments: encode merchant secrets: %w", err)
	}
	return p.keyring.Seal(secret.New(string(plain)), configAt(id)), nil
}

// unseal opens a merchant's secrets and refuses them unless the settings beside them are the ones they were sealed with.
func (p *Payments) unseal(id MerchantID, sealed secret.Sealed, settings string) (map[string]secret.Secret, error) {
	plain, err := p.keyring.Open(sealed, configAt(id))
	if err != nil {
		p.logger.Error("payments: a merchant account's secrets do not open", "merchant", id.TypeID())
		return nil, fmt.Errorf("payments: open merchant secrets: %w", err)
	}
	var c sealedConfig
	if err = jsonx.UnmarshalKnown([]byte(plain.Reveal()), &c); err != nil {
		return nil, fmt.Errorf("payments: decode merchant secrets: %w", err)
	}
	sum := sha256.Sum256([]byte(settings))
	if subtle.ConstantTimeCompare(sum[:], c.SettingsSHA256) != 1 {
		p.logger.Error("payments: a merchant account's settings were changed outside a save", "merchant", id.TypeID())
		return nil, ErrTampered
	}
	secrets := make(map[string]secret.Secret, len(c.Secrets))
	for name, s := range c.Secrets {
		secrets[name] = secret.New(s)
	}
	return secrets, nil
}

// channel opens a merchant's channel in tx, which must see merchant_configs: the site or the platform scope.
func (p *Payments) channel(ctx context.Context, tx pg.Tx, id MerchantID) (Channel, error) {
	rows, err := queries.MerchantConfigs(ctx, tx, []uuid.UUID{uuid.UUID(id)})
	if err != nil {
		return nil, fmt.Errorf("payments: read merchant config: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return p.open(rows[0])
}

// open binds a merchant's plugin to its configuration: the plugin must exist, the secrets must open and match the
// settings, and the plugin must accept them.
func (p *Payments) open(r MerchantConfigsRow) (Channel, error) {
	id := MerchantID(r.ID)
	plugin, ok := p.plugins[Kind(r.Kind)]
	if !ok {
		return nil, fmt.Errorf("payments: merchant %s: no plugin of kind %q", id.TypeID(), r.Kind)
	}
	currency, err := platform.ParseCurrency(r.Currency)
	if err != nil {
		return nil, err
	}
	secrets, err := p.unseal(id, sealedOf(r.Ciphertext, r.Nonce, r.WrappedDek, r.KeyVersion), r.Settings)
	if err != nil {
		return nil, err
	}
	return plugin.Open(MerchantConfig{ID: id, Currency: currency, Settings: []byte(r.Settings), Secrets: secrets, Client: p.client})
}

// validity is whether a merchant's configuration opened at a revision, and when that was checked.
type validity struct {
	rev int64
	ok  bool
	at  time.Time
}

// validFor is how long a check stands: a save changes the revision and is seen at once, while a configuration
// changed outside a save keeps the revision and is caught at the next check (docs/domains/payments.md「选商户账户」).
const validFor = time.Minute

// usable keeps the offers whose merchant's configuration opens, in tx, a scope that reads merchant_configs: the
// account scope a top-up page or an order is quoted in never reads them, so the platform scope checks for it. A
// merchant that does not open is left out and alerted on, without affecting the others.
func (p *Payments) usable(ctx context.Context, tx pg.Tx, offers []OffersRow) ([]OffersRow, error) {
	now := time.Now()
	var stale []uuid.UUID
	p.mu.Lock()
	for _, o := range offers {
		if v, ok := p.valid[o.MerchantID]; !ok || v.rev != o.Rev || now.Sub(v.at) > validFor {
			stale = append(stale, o.MerchantID)
		}
	}
	p.mu.Unlock()
	if len(stale) > 0 {
		rows, err := queries.MerchantConfigs(ctx, tx, stale)
		if err != nil {
			return nil, fmt.Errorf("payments: read merchant configs: %w", err)
		}
		checked := make([]validity, len(rows))
		for i, r := range rows {
			_, err := p.open(r)
			if err != nil {
				p.logger.Error("payments: a merchant account cannot be used", "merchant", MerchantID(r.ID).TypeID(), "error", err)
			}
			checked[i] = validity{r.Rev, err == nil, now}
		}
		p.mu.Lock()
		for i, r := range rows {
			p.valid[r.ID] = checked[i]
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.DeleteFunc(offers, func(o OffersRow) bool {
		v, ok := p.valid[o.MerchantID]
		return !ok || !v.ok || v.rev != o.Rev
	}), nil
}
