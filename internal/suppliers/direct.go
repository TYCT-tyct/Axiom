package suppliers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// Direct APIs (docs/domains/suppliers.md「直连接口」): a provider's own API, or one that speaks its formats, reached with
// keys a person pastes in. A direct supplier is the rows every supplier has, made in one transaction like an account's
// own key: one selected group (its one line), a credential per key that passed its check, an endpoint per model the
// keys list that the catalog has on sale, and the catalog's official price times the line's ratio as each endpoint's
// cost. It has no login and no sync, and no balance but its keys' own, for a type that reads them (wallets.go).

// The ways a direct API or a cloud platform is refused before anything is asked of the provider.
var (
	// ErrNoType is a kind this node has no direct or cloud adapter for: a relay's, or none.
	ErrNoType = errors.New("suppliers: no direct or cloud channel type of that kind")
	// ErrTypeOrigin is a price entry that is not the type's, or an address or settlement currency given for a type with
	// price entries; or none given for a type without them.
	ErrTypeOrigin = errors.New("suppliers: a type with price entries is connected at one of them, and any other needs an address")
	// ErrDirectFormat is a family given for a provider whose family is its own, or none for one whose is not.
	ErrDirectFormat = errors.New("suppliers: a built-in provider's call format is its own, and any other API needs one")
	// ErrDirectGemini is an API that speaks only Gemini's generateContent, which the gateway does not send yet.
	ErrDirectGemini = errors.New("suppliers: Gemini's own call format is not served yet")
	// ErrDirectCurrency is an API with no price entries and no settlement currency given.
	ErrDirectCurrency = errors.New("suppliers: an API without a built-in price list needs a settlement currency")
	// ErrDirectName is a name outside 1 to 200 characters.
	ErrDirectName = errors.New("suppliers: a name is 1 to 200 characters")
	// ErrBalanceCurrency is a key whose provider tells its balance in another currency than the price entry's: its
	// wallet is the other price list's.
	ErrBalanceCurrency = errors.New("suppliers: a key's balance is in another currency than its price entry's")
)

// KeyError is a pasted key that is not a plausible bearer token. Index is its position in the request; the key itself is
// not in the error.
type KeyError struct{ Index int }

func (e *KeyError) Error() string {
	return fmt.Sprintf("suppliers: key %d is not a bearer token", e.Index)
}

// DirectRequest is a direct API to connect. Entry is the price entry of a type with them, empty for its first; Origin is
// as NormalizeOrigin returns it, and it, Family and Currency are only for a type without entries. Fields are the keys'
// settings, by the type's field names. Keys are plaintext and are only sealed. Ratio is the line's ratio, empty for 1.
type DirectRequest struct {
	Kind, Name, Entry, Origin, Family string
	Ratio                             string
	Currency                          platform.Currency
	Fields                            map[string]string
	Keys                              []string
	MaxConcurrency                    int32
	RequestsPerMinute                 *int32
	Reason                            string
}

// pastedKey is what a plan holds of one pasted key beside its check: the text, which is only sealed, the ref it is
// stored and found by, the models its check listed, and for a type that reads balances, the balance read with it
// (nil when none was read).
type pastedKey struct {
	key     secret.Secret
	ref     string
	models  []string
	balance *adapter.Money
}

// DirectPlan is a direct API about to be connected: the request as checked, and each key's check once CheckDirect has
// asked the provider. It is made by PlanDirect and holds the keys' texts in memory only.
type DirectPlan struct {
	r        DirectRequest
	family   adapter.Family
	origin   string
	direct   adapter.Direct
	settings adapter.Settings
	ratio    *big.Rat // the line's, for a new supplier
	Checks   []KeyCheck
	keys     []pastedKey // one for each of Checks
}

// Currency is the settlement currency the supplier will have: the request's, or the provider's own.
func (p *DirectPlan) Currency() platform.Currency { return p.r.Currency }

// Last4 is what is shown and kept of a pasted key: its last four characters once trimmed.
func Last4(key string) string { return hintOf(strings.TrimSpace(key)) }

// Connected is a direct API as CreateDirect left it: the supplier, or none when no key passed and nothing was stored,
// and every key's check in the order sent.
type Connected struct {
	Supplier *Supplier
	Checks   []KeyCheck
}

// keyRef is what a pasted key is stored and found by: the first 128 bits of its SHA-256, as a key CPA lists is. It
// tells a key already stored from a new one without keeping or comparing the text.
func keyRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// PlanDirect checks a request against the provider it names and against itself and the keys already stored: a key
// sent twice, or one any credential holds now, is a duplicate and is not checked. Nothing is asked of the provider and
// nothing is written.
func (a *Admin) PlanDirect(ctx context.Context, tx pg.Tx, r DirectRequest) (*DirectPlan, error) {
	p, err := a.planDirect(r)
	if err != nil {
		return nil, err
	}
	return p, p.skipStored(ctx, tx)
}

func (a *Admin) planDirect(r DirectRequest) (*DirectPlan, error) {
	ad, ok := a.registry.Get(adapter.Kind(r.Kind))
	d, direct := ad.(adapter.Direct)
	if !ok || !direct {
		return nil, ErrNoType
	}
	if n := len([]rune(strings.TrimSpace(r.Name))); n == 0 || n > 200 {
		return nil, ErrDirectName
	}
	if r.MaxConcurrency < 1 || r.RequestsPerMinute != nil && (*r.RequestsPerMinute < 1 || *r.RequestsPerMinute > MaxRequestsPerMinute) {
		return nil, ErrLimits
	}
	t := d.Type()
	_, secrets, settings, err := t.Read("", r.Fields)
	if err != nil {
		return nil, err
	}
	if len(secrets) > 0 { // a direct API's secrets are its keys
		return nil, &adapter.FieldError{Field: "fields", Reason: "holds no secret for a direct API: its keys are the keys"}
	}
	ratio, err := connectRatio(r.Ratio)
	if err != nil {
		return nil, err
	}
	p := &DirectPlan{r: r, direct: d, settings: settings, ratio: ratio}
	if err := p.resolve(t); err != nil {
		return nil, err
	}
	return p, p.paste(r.Keys)
}

// paste is a check for each key, in order; one that came earlier in the list is a duplicate.
func (p *DirectPlan) paste(keys []string) error {
	p.Checks, p.keys = make([]KeyCheck, 0, len(keys)), make([]pastedKey, 0, len(keys))
	seen := map[string]bool{}
	for i, raw := range keys {
		key := strings.TrimSpace(raw)
		if !bearerToken(key) {
			return &KeyError{Index: i}
		}
		c, k := KeyCheck{Index: i, Last4: Last4(key)}, pastedKey{key: secret.New(key), ref: keyRef(key)}
		if seen[k.ref] {
			c.Outcome = CheckDuplicate
		}
		seen[k.ref] = true
		p.Checks, p.keys = append(p.Checks, c), append(p.keys, k)
	}
	return nil
}

// resolve fills in what the request leaves to the type: a type with price entries is connected at one, whose address,
// family and currency are its own and may not be given; any other API's must be.
func (p *DirectPlan) resolve(t adapter.Type) error {
	if len(t.Entries) == 0 && adapter.Family(p.r.Family) == adapter.FamilyGemini {
		return ErrDirectGemini // a family the gateway does not send yet, not one the type refuses
	}
	family, ok := adapter.FamilyOf(t, adapter.Family(p.r.Family))
	if !ok {
		return ErrDirectFormat
	}
	p.family = family
	if len(t.Entries) > 0 {
		e, ok := t.Entry(p.r.Entry)
		if !ok || p.r.Origin != "" || p.r.Currency != (platform.Currency{}) {
			return ErrTypeOrigin
		}
		currency, err := platform.ParseCurrency(e.Currency)
		if err != nil {
			return err
		}
		p.origin, p.r.Currency = e.Origin, currency
		return nil
	}
	if p.r.Origin == "" || p.r.Entry != "" {
		return ErrTypeOrigin
	}
	if p.r.Currency == (platform.Currency{}) {
		return ErrDirectCurrency
	}
	p.origin = p.r.Origin
	return nil
}

// takeStoreTurn makes the transactions that store a pasted key or cloud credential take turns until they commit, so
// the skipStored each runs next sees what the one before stored; the unique constraint is only per supplier.
func takeStoreTurn(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('suppliers.pasted_credentials'))`); err != nil {
		return fmt.Errorf("wait for the credentials being stored: %w", err)
	}
	return nil
}

// skipStored marks the keys a credential holds now as duplicates.
func (p *DirectPlan) skipStored(ctx context.Context, tx pg.Tx) error {
	refs := make([]string, 0, len(p.keys))
	for _, k := range p.keys {
		refs = append(refs, k.ref)
	}
	stored, err := store.New().StoredKeyRefs(ctx, tx, refs)
	if err != nil {
		return fmt.Errorf("read the keys already stored: %w", err)
	}
	for i := range p.Checks {
		if _, found := slices.BinarySearch(stored, p.keys[i].ref); found {
			p.Checks[i] = KeyCheck{Index: p.Checks[i].Index, Last4: p.Checks[i].Last4, Outcome: CheckDuplicate}
			p.keys[i].models = nil
		}
	}
	return nil
}

// checkParallel is how many keys of one request are checked at once: each check is one request of its own key, which
// the provider limits by that key's account, and a person waits for all of them.
const checkParallel = 16

// CheckDirect asks the provider about every key that is not a duplicate, a few at once, through the outbound client,
// which refuses private and internal addresses; an address it refuses fails the whole request. A type that reads
// balances reads each passed key's too: a balance in another currency than the price entry's is ErrBalanceCurrency for
// the whole request, since the keys' wallet is not the price list's, and one that does not read is left unread. It
// writes nothing.
func (a *Admin) CheckDirect(ctx context.Context, p *DirectPlan) error {
	if len(p.direct.Type().Entries) == 0 {
		if err := a.CheckOrigin(ctx, p.origin); err != nil {
			return err
		}
	}
	c, err := client.New(p.origin, a.egress, client.NewLimiter(nil, a.now), a.now)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, checkParallel)
	for i := range p.Checks {
		if p.Checks[i].Outcome == CheckDuplicate {
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			a.checkPasted(ctx, c, p, i) // each goroutine writes only its own elements
		})
	}
	wg.Wait()
	for _, k := range p.keys {
		if k.balance != nil && k.balance.Currency != p.r.Currency {
			return ErrBalanceCurrency
		}
	}
	return nil
}

// checkPasted checks key i of the plan and, for a type that reads balances, reads the balance of one that passed.
func (a *Admin) checkPasted(ctx context.Context, c adapter.Client, p *DirectPlan, i int) {
	got := p.direct.CheckKey(ctx, c, p.family, p.keys[i].key, p.settings)
	p.Checks[i] = checkOf(got, p.Checks[i], a.now())
	p.keys[i].models = got.Models
	b, reads := p.direct.(adapter.Balancer)
	if !reads || !p.direct.Type().Balance || got.Outcome != adapter.KeyPassed {
		return
	}
	if m, err := b.ReadBalance(ctx, c, p.keys[i].key, p.settings, p.r.Currency); err == nil { // else unread until the re-list
		p.keys[i].balance = &m
	}
}

// checkOf is what an adapter's answer to a check makes of the check c, at now.
func checkOf(got adapter.KeyCheck, c KeyCheck, now time.Time) KeyCheck {
	c.Outcome, c.Cause, c.UpstreamStatus, c.CheckedAt = outcomeOf(got.Outcome), CheckCause(got.Cause), got.Status, now
	return c
}

func outcomeOf(o adapter.KeyOutcome) CheckOutcome {
	switch o {
	case adapter.KeyPassed:
		return CheckPassed
	case adapter.KeyFailed:
		return CheckFailed
	case adapter.KeyInconclusive:
	}
	return CheckInconclusive
}

// CreateDirect stores what the checks let in, in the caller's transaction (docs/domains/suppliers.md「直连接口」): when no
// key passed, nothing, and Connected has no supplier. A key another transaction stored since PlanDirect read, or is
// storing now, is a duplicate here too. snap is the catalog the keys' model names are mapped by and the official
// prices come from.
func (a *Admin) CreateDirect(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *DirectPlan) (Connected, error) {
	if err := p.recheck(ctx, tx); err != nil {
		return Connected{}, err
	}
	if !slices.ContainsFunc(p.Checks, func(c KeyCheck) bool { return c.Outcome == CheckPassed }) {
		return Connected{Checks: p.Checks}, nil
	}
	q := store.New()
	var family *string
	if len(p.direct.Type().Entries) == 0 {
		family = new(string(p.family))
	}
	id, err := q.InsertDirectSupplier(ctx, tx, store.InsertDirectSupplierParams{
		Name: strings.TrimSpace(p.r.Name), Kind: p.r.Kind, Origin: p.origin, UpstreamAccount: uuid.NewV7().String(),
		SettlementCurrency: p.r.Currency.String(), ApiFormat: family,
	})
	if err != nil {
		return Connected{}, fmt.Errorf("connect the direct API: %w", err)
	}
	group, err := insertLine(ctx, tx, q, id, p.r.Kind, p.origin, p.ratio)
	if err != nil {
		return Connected{}, err
	}
	if err := q.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{
		SupplierID: id, Value: "1", Actor: "direct", Reason: "a direct API's costs are its official prices in the settlement currency",
	}); err != nil {
		return Connected{}, fmt.Errorf("set the direct API's unit value: %w", err)
	}
	models, priced, err := insertLineModels(ctx, tx, q, id, group, snap, p.r.Currency, p.offer(), nil, p.ratio)
	if err != nil {
		return Connected{}, err
	}
	if err := a.insertDirectKeys(ctx, tx, q, id, group, snap, p); err != nil {
		return Connected{}, err
	}
	if err := a.record(ctx, tx, actor, "supplier.direct_connect", id, p.r.Reason, directAudit(p, models, priced)); err != nil {
		return Connected{}, err
	}
	s, err := a.view(ctx, tx, id, EveryOwner)
	if err != nil {
		return Connected{}, err
	}
	return Connected{Supplier: &s, Checks: p.Checks}, nil
}

// insertLine makes a direct API's one line: a group at the ratio, selected, with its codename.
func insertLine(ctx context.Context, tx pg.Tx, q *store.Queries, id uuid.UUID, kind, origin string, ratio *big.Rat) (uuid.UUID, error) {
	group, err := q.InsertGroup(ctx, tx, store.InsertGroupParams{SupplierID: id, UpstreamRef: origin, Name: kind, Ratio: ratioText(ratio)})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("make the direct API's line: %w", err)
	}
	code := store.Codename()
	if err := q.SelectGroup(ctx, tx, store.SelectGroupParams{ID: group, Selected: true, Codename: &code}); err != nil {
		return uuid.UUID{}, fmt.Errorf("select the direct API's line: %w", err)
	}
	return group, nil
}

// insertDirectKeys seals each key that passed at its own credential, with the request's limits and what it lists on the
// written line (lineServes): keys of one line take turns, and one key is never sent a model its account was not shown.
func (a *Admin) insertDirectKeys(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, p *DirectPlan) error {
	line, err := lineNames(ctx, tx, q, group, snap, p.offer())
	if err != nil {
		return err
	}
	for i := range p.Checks {
		c, k := &p.Checks[i], p.keys[i]
		if c.Outcome != CheckPassed {
			continue
		}
		c.Credential = uuid.NewV7()
		allowed, calls, err := lineServes(k.models, nil, line)
		if err != nil {
			return err
		}
		sealed := a.keys.Seal(k.key, keyAt(c.Credential))
		keyVersion := int64(sealed.KeyVersion)
		row := store.InsertDirectCredentialParams{
			ID: c.Credential, SupplierID: id, SupplierGroupID: group, UpstreamKeyRef: k.ref,
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &keyVersion,
			MaxConcurrency: p.r.MaxConcurrency, RequestsPerMinute: p.r.RequestsPerMinute, AllowedModels: allowed, CallNames: calls, Hint: &c.Last4,
			Settings: settingsColumn(p.settings),
		}
		if k.balance != nil {
			row.BalanceNative, row.BalanceObservedAt = new(exactText(k.balance.Amount)), &c.CheckedAt
		}
		if err := q.InsertDirectCredential(ctx, tx, row); err != nil {
			return fmt.Errorf("store key %d: %w", c.Index, err)
		}
	}
	return nil
}

// lineOffer is what a connection lists on a line of one: each name, the name the catalog knows it by (known; nil for
// the name itself), the formats an endpoint of a catalog model serves there, what a name the catalog does not map yet
// is listed as speaking, and the catalog authors whose official prices are the line's own prices (serves; empty for
// any): a model of another author is sold there at a price of the platform's, which the catalog does not hold.
type lineOffer struct {
	names    []string
	known    func(name string) string
	formats  func(m catalog.Model, name string) []protocol.Format
	unmapped []protocol.Format
	serves   []string
}

// prices says the catalog's reference price of m is the line's own.
func (o lineOffer) prices(m catalog.Model) bool {
	return len(o.serves) == 0 || slices.Contains(o.serves, m.Author)
}

// offer is what the passed keys list.
func (p *DirectPlan) offer() lineOffer {
	return lineOffer{
		names: p.names(), formats: func(m catalog.Model, name string) []protocol.Format { return p.direct.Formats(p.family, m, name) },
		unmapped: familyFormats(p.family), serves: p.direct.Type().Serves,
	}
}

// insertLineModels writes every name of the offer on the line. A name the catalog maps to a model on sale gets an
// endpoint in the formats the line serves the model in, costed at the model's official price in the settlement
// currency times the line's ratio (cost_schedules basis published: the provider's own published price); a name with no
// such price keeps an endpoint whose cost is unknown, out of routing. A name the catalog does not map waits for a
// person, as a relay's does. Two names of one model are one endpoint: the name that is evidence of the version, else
// the first. A model in served has an endpoint already and keeps it.
func insertLineModels(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, currency platform.Currency, o lineOffer, served map[catalog.ModelID]bool, ratio *big.Rat) (models, priced int, err error) {
	fallback, err := store.FormatNames(o.unmapped)
	if err != nil {
		return 0, 0, err
	}
	endpoints := map[catalog.ModelID]string{}
	exact := map[catalog.ModelID]bool{}
	rows := make(map[string]store.UpsertProjectedModelParams, len(o.names))
	for _, name := range o.names {
		row := store.UpsertProjectedModelParams{SupplierID: id, SupplierGroupID: group, UpstreamName: name, Formats: fallback}
		m, serves := mapLineName(snap, o, name, &row)
		if _, taken := endpoints[m.Model]; serves && !served[m.Model] && (!taken || !m.Unverified && !exact[m.Model]) {
			endpoints[m.Model], exact[m.Model] = name, !m.Unverified
		}
		rows[name] = row
		if err := q.UpsertProjectedModel(ctx, tx, row); err != nil {
			return 0, 0, fmt.Errorf("list %s: %w", name, err)
		}
	}
	for _, model := range slices.SortedFunc(maps.Keys(endpoints), func(a, b catalog.ModelID) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }) {
		name := endpoints[model]
		m, _ := snap.Model(model) // mapLineName mapped the name to a model of the snapshot
		ok, err := insertDirectEndpoint(ctx, tx, q, id, group, snap, currency, model, name, rows[name].Formats, o.prices(m), ratio)
		if err != nil {
			return 0, 0, err
		}
		if ok {
			priced++
		}
	}
	return len(o.names), priced, nil
}

// mapLineName maps one name of the offer into row: the model on sale it stands for, and the formats the line serves
// that model in. It reports whether an endpoint can serve the name; one the catalog does not map keeps no model.
func mapLineName(snap *catalog.Snapshot, o lineOffer, name string, row *store.UpsertProjectedModelParams) (syncer.Mapping, bool) {
	m := syncer.MapUpstream(snap, nil, o.knownAs(name))
	model, ok := snap.Model(m.Model)
	if !m.Mapped || !ok {
		return m, false
	}
	mapped := uuid.UUID(m.Model)
	row.ModelID = &mapped
	formats, err := store.FormatNames(o.formats(model, name))
	if err != nil || len(formats) == 0 {
		return m, false
	}
	row.Formats = formats
	return m, true
}

// insertDirectEndpoint makes one endpoint and, when the catalog's official price in the currency is the line's own price
// (priced) and the catalog has one, its cost schedule at the line's ratio; it reports whether it priced it. An endpoint
// left unpriced has an unknown cost and is not routed.
func insertDirectEndpoint(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, currency platform.Currency, model catalog.ModelID, name string, formats []string, priced bool, ratio *big.Rat) (bool, error) {
	endpoint, err := q.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
		SupplierID: id, SupplierGroupID: group, ModelID: uuid.UUID(model), UpstreamName: name, Formats: formats,
	})
	if err != nil {
		return false, fmt.Errorf("make the endpoint for %s: %w", name, err)
	}
	ref, ok := snap.ReferencePrice(model, currency.String())
	if !ok || !priced {
		return false, nil
	}
	if err := priceEndpoint(ctx, tx, q, id, endpoint, ref, ratio); err != nil {
		return false, fmt.Errorf("price the endpoint for %s: %w", name, err)
	}
	return true, nil
}

// familyFormats is what a name the catalog cannot map yet is listed as speaking: what every API of the family serves.
func familyFormats(f adapter.Family) []protocol.Format {
	if f == adapter.FamilyAnthropic {
		return []protocol.Format{protocol.Messages}
	}
	return []protocol.Format{protocol.Chat}
}

// names is every name a passed key listed, sorted and once each.
func (p *DirectPlan) names() []string {
	var names []string
	for i, c := range p.Checks {
		if c.Outcome == CheckPassed {
			names = append(names, p.keys[i].models...)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// directAudit is what the audit entry says: the API, its limits and what its keys came to, by their last four
// characters. No key is in it.
func directAudit(p *DirectPlan, models, priced int) map[string]any {
	outcomes := make([]map[string]any, len(p.Checks))
	for i, c := range p.Checks {
		outcomes[i] = map[string]any{"last4": c.Last4, "outcome": c.Outcome}
	}
	detail := map[string]any{
		"kind": p.r.Kind, "origin": p.origin, "settlement_currency": p.r.Currency.String(), "max_concurrency": p.r.MaxConcurrency,
		"keys": outcomes, "models": models, "priced_endpoints": priced, "ratio": ratioText(p.ratio),
	}
	if p.r.Family != "" {
		detail["api_format"] = p.r.Family
	}
	if p.r.RequestsPerMinute != nil {
		detail["requests_per_minute"] = *p.r.RequestsPerMinute
	}
	return detail
}
