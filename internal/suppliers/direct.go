package suppliers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
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
// keys list that the catalog has on sale, and the catalog's official price as each endpoint's cost. It has no login,
// no balance and no sync.

// The ways a direct API is refused before anything is asked of the provider.
var (
	// ErrDirectProvider is a kind this node has no direct API adapter for.
	ErrDirectProvider = errors.New("suppliers: no direct API of that kind")
	// ErrDirectOrigin is an address given for a provider whose address is its own, or none for one whose is not.
	ErrDirectOrigin = errors.New("suppliers: a built-in provider's address is its own, and any other API needs one")
	// ErrDirectFormat is a family given for a provider whose family is its own, or none for one whose is not.
	ErrDirectFormat = errors.New("suppliers: a built-in provider's call format is its own, and any other API needs one")
	// ErrDirectGemini is an API that speaks only Gemini's generateContent, which the gateway does not send yet.
	ErrDirectGemini = errors.New("suppliers: Gemini's own call format is not served yet")
	// ErrDirectCurrency is an API with no official price list of Axiom's knowing and no settlement currency given.
	ErrDirectCurrency = errors.New("suppliers: an API without a built-in price list needs a settlement currency")
	// ErrDirectName is a name outside 1 to 200 characters.
	ErrDirectName = errors.New("suppliers: a name is 1 to 200 characters")
)

// KeyError is a pasted key that is not a plausible bearer token. Index is its position in the request; the key itself is
// not in the error.
type KeyError struct{ Index int }

func (e *KeyError) Error() string {
	return fmt.Sprintf("suppliers: key %d is not a bearer token", e.Index)
}

// DirectRequest is a direct API to connect. Origin is as NormalizeOrigin returns it and only for an API whose address
// is not built in, as Family is; a zero Currency is the provider's own. Keys are plaintext and are only sealed.
type DirectRequest struct {
	Kind, Name, Origin, Family string
	Currency                   platform.Currency
	Keys                       []string
	MaxConcurrency             int32
	RequestsPerMinute          *int32
	Reason                     string
}

// The outcomes of a key's check, as the admin API names them.
const (
	DirectKeyPassed       = "passed"
	DirectKeyFailed       = "failed"
	DirectKeyInconclusive = "inconclusive"
	DirectKeyDuplicate    = "duplicate"
)

// DirectKeyCheck is what one pasted key came to. Credential is the credential it was stored as, the zero ID when it was not;
// Status is the provider's HTTP status, 0 when it answered nothing; CheckedAt is zero for a key that was not checked.
// The key itself is never in it.
type DirectKeyCheck struct {
	Index      int
	Last4      string
	Outcome    string
	Cause      string
	Status     int
	Credential uuid.UUID
	CheckedAt  time.Time
	key        secret.Secret
	ref        string
	models     []string
}

// DirectPlan is a direct API about to be connected: the request as checked, and each key's check once CheckDirect has
// asked the provider. It is made by PlanDirect and holds the keys' texts in memory only.
type DirectPlan struct {
	r      DirectRequest
	family adapter.Family
	origin string
	direct adapter.Direct
	Checks []DirectKeyCheck
}

// Currency is the settlement currency the supplier will have: the request's, or the provider's own.
func (p *DirectPlan) Currency() platform.Currency { return p.r.Currency }

// Last4 is what is shown and kept of a pasted key: its last four characters once trimmed.
func Last4(key string) string { return hintOf(strings.TrimSpace(key)) }

// Connected is a direct API as CreateDirect left it: the supplier, or none when no key passed and nothing was stored,
// and every key's check in the order sent.
type Connected struct {
	Supplier *Supplier
	Checks   []DirectKeyCheck
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
		return nil, ErrDirectProvider
	}
	if n := len([]rune(strings.TrimSpace(r.Name))); n == 0 || n > 200 {
		return nil, ErrDirectName
	}
	if r.MaxConcurrency < 1 || r.RequestsPerMinute != nil && (*r.RequestsPerMinute < 1 || *r.RequestsPerMinute > MaxRequestsPerMinute) {
		return nil, ErrLimits
	}
	p := &DirectPlan{r: r, family: d.Family(), origin: d.Origin(), direct: d}
	if err := p.resolve(d.Currency()); err != nil {
		return nil, err
	}
	checks, err := pastedKeys(r.Keys)
	if err != nil {
		return nil, err
	}
	p.Checks = checks
	return p, nil
}

// pastedKeys is a check for each key, in order; one that came earlier in the list is a duplicate.
func pastedKeys(keys []string) ([]DirectKeyCheck, error) {
	checks := make([]DirectKeyCheck, 0, len(keys))
	seen := map[string]bool{}
	for i, raw := range keys {
		key := strings.TrimSpace(raw)
		if !bearerToken(key) {
			return nil, &KeyError{Index: i}
		}
		c := DirectKeyCheck{Index: i, Last4: Last4(key), key: secret.New(key), ref: keyRef(key)}
		if seen[c.ref] {
			c.Outcome = DirectKeyDuplicate
		}
		seen[c.ref] = true
		checks = append(checks, c)
	}
	return checks, nil
}

// resolve fills in what the request leaves to the provider: a built-in provider's address, family and currency are its
// own and may not be given; any other API's must be.
func (p *DirectPlan) resolve(currency string) error {
	if (p.origin == "") == (p.r.Origin == "") {
		return ErrDirectOrigin
	}
	if (p.family == "") == (p.r.Family == "") {
		return ErrDirectFormat
	}
	if p.origin == "" {
		p.origin, p.family = p.r.Origin, adapter.Family(p.r.Family)
	}
	switch p.family {
	case adapter.FamilyOpenAI, adapter.FamilyAnthropic:
	case adapter.FamilyGemini:
		return ErrDirectGemini
	default:
		return ErrDirectFormat
	}
	if p.r.Currency == (platform.Currency{}) {
		parsed, err := platform.ParseCurrency(currency)
		if err != nil {
			return ErrDirectCurrency
		}
		p.r.Currency = parsed
	}
	return nil
}

// skipStored marks the keys a credential holds now as duplicates.
func (p *DirectPlan) skipStored(ctx context.Context, tx pg.Tx) error {
	refs := make([]string, 0, len(p.Checks))
	for _, c := range p.Checks {
		refs = append(refs, c.ref)
	}
	stored, err := store.New().StoredKeyRefs(ctx, tx, refs)
	if err != nil {
		return fmt.Errorf("read the keys already stored: %w", err)
	}
	for i := range p.Checks {
		if _, found := slices.BinarySearch(stored, p.Checks[i].ref); found {
			p.Checks[i].Outcome, p.Checks[i].Cause, p.Checks[i].Status = DirectKeyDuplicate, "", 0
			p.Checks[i].CheckedAt, p.Checks[i].models = time.Time{}, nil
		}
	}
	return nil
}

// checkParallel is how many keys of one request are checked at once: each check is one request of its own key, which
// the provider limits by that key's account, and a person waits for all of them.
const checkParallel = 16

// CheckDirect asks the provider about every key that is not a duplicate, a few at once, through the outbound client,
// which refuses private and internal addresses; an address it refuses fails the whole request. It writes nothing.
func (a *Admin) CheckDirect(ctx context.Context, p *DirectPlan) error {
	if p.r.Origin != "" {
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
		if p.Checks[i].Outcome == DirectKeyDuplicate {
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			check := &p.Checks[i] // each goroutine writes only its own element
			got := p.direct.CheckKey(ctx, c, p.family, check.key)
			check.Outcome, check.Cause, check.Status, check.CheckedAt = outcomeOf(got.Outcome), string(got.Cause), got.Status, a.now()
			check.models = got.Models
		})
	}
	wg.Wait()
	return nil
}

func outcomeOf(o adapter.KeyOutcome) string {
	switch o {
	case adapter.KeyPassed:
		return DirectKeyPassed
	case adapter.KeyFailed:
		return DirectKeyFailed
	case adapter.KeyInconclusive:
	}
	return DirectKeyInconclusive
}

// CreateDirect stores what the checks let in, in the caller's transaction (docs/domains/suppliers.md「直连接口」): when no
// key passed, nothing, and Connected has no supplier. A key another transaction stored since PlanDirect read is a
// duplicate here too. snap is the catalog the keys' model names are mapped by and the official prices come from.
func (a *Admin) CreateDirect(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, p *DirectPlan) (Connected, error) {
	if err := p.skipStored(ctx, tx); err != nil {
		return Connected{}, err
	}
	passed := slices.ContainsFunc(p.Checks, func(c DirectKeyCheck) bool { return c.Outcome == DirectKeyPassed })
	if !passed {
		return Connected{Checks: p.Checks}, nil
	}
	q := store.New()
	var family *string
	if p.r.Family != "" {
		family = &p.r.Family
	}
	id, err := q.InsertDirectSupplier(ctx, tx, store.InsertDirectSupplierParams{
		Name: strings.TrimSpace(p.r.Name), Kind: p.r.Kind, Origin: p.origin, UpstreamAccount: uuid.NewV7().String(),
		SettlementCurrency: p.r.Currency.String(), ApiFormat: family,
	})
	if err != nil {
		return Connected{}, fmt.Errorf("connect the direct API: %w", err)
	}
	group, err := insertLine(ctx, tx, q, id, p.r.Kind, p.origin)
	if err != nil {
		return Connected{}, err
	}
	if err := a.insertDirectKeys(ctx, tx, q, id, group, p); err != nil {
		return Connected{}, err
	}
	if err := q.InsertUnitValue(ctx, tx, store.InsertUnitValueParams{
		SupplierID: id, Value: "1", Actor: "direct", Reason: "a direct API's costs are its official prices in the settlement currency",
	}); err != nil {
		return Connected{}, fmt.Errorf("set the direct API's unit value: %w", err)
	}
	models, priced, err := insertDirectModels(ctx, tx, q, id, group, snap, p)
	if err != nil {
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

// insertLine makes a direct API's one line: a group, selected, with its codename.
func insertLine(ctx context.Context, tx pg.Tx, q *store.Queries, id uuid.UUID, kind, origin string) (uuid.UUID, error) {
	group, err := q.InsertGroup(ctx, tx, store.InsertGroupParams{SupplierID: id, UpstreamRef: origin, Name: kind, Ratio: "1"})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("make the direct API's line: %w", err)
	}
	code := store.Codename()
	if err := q.SelectGroup(ctx, tx, store.SelectGroupParams{ID: group, Selected: true, Codename: &code}); err != nil {
		return uuid.UUID{}, fmt.Errorf("select the direct API's line: %w", err)
	}
	return group, nil
}

// insertDirectKeys seals each key that passed at its own credential, with the request's limits and the models the key
// itself lists: keys of one line take turns, and one key is never sent a model its account was not shown.
func (a *Admin) insertDirectKeys(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, p *DirectPlan) error {
	for i := range p.Checks {
		c := &p.Checks[i]
		if c.Outcome != DirectKeyPassed {
			continue
		}
		c.Credential = uuid.NewV7()
		sealed := a.keys.Seal(c.key, secret.Location{Table: "suppliers.credentials", RowID: c.Credential.String(), Purpose: "key"})
		keyVersion := int64(sealed.KeyVersion)
		if err := q.InsertDirectCredential(ctx, tx, store.InsertDirectCredentialParams{
			ID: c.Credential, SupplierID: id, SupplierGroupID: group, UpstreamKeyRef: c.ref,
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &keyVersion,
			MaxConcurrency: p.r.MaxConcurrency, RequestsPerMinute: p.r.RequestsPerMinute, AllowedModels: c.models, Hint: &c.Last4,
		}); err != nil {
			return fmt.Errorf("store key %d: %w", c.Index, err)
		}
	}
	return nil
}

// insertDirectModels writes every name the passed keys list on the line. A name the catalog maps to a model on sale
// gets an endpoint in the formats the API serves the model in, costed at the model's official price in the settlement
// currency (cost_schedules basis published: the provider's own published price); a name with no such price keeps an
// endpoint whose cost is unknown, out of routing. A name the catalog does not map waits for a person, as a relay's
// does. Two names of one model are one endpoint: the name that is evidence of the version, else the first.
func insertDirectModels(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, p *DirectPlan) (models, priced int, err error) {
	fallback, err := store.FormatNames(familyFormats(p.family))
	if err != nil {
		return 0, 0, err
	}
	endpoints := map[catalog.ModelID]string{}
	exact := map[catalog.ModelID]bool{}
	names := directNames(p.Checks)
	rows := make(map[string]store.UpsertProjectedModelParams, len(names))
	for _, name := range names {
		row := store.UpsertProjectedModelParams{SupplierID: id, SupplierGroupID: group, UpstreamName: name, Formats: fallback}
		m, serves := mapDirectName(snap, p, name, &row)
		if _, taken := endpoints[m.Model]; serves && (!taken || !m.Unverified && !exact[m.Model]) {
			endpoints[m.Model], exact[m.Model] = name, !m.Unverified
		}
		rows[name] = row
		if err := q.UpsertProjectedModel(ctx, tx, row); err != nil {
			return 0, 0, fmt.Errorf("list %s: %w", name, err)
		}
	}
	for _, model := range slices.SortedFunc(maps.Keys(endpoints), func(a, b catalog.ModelID) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }) {
		name := endpoints[model]
		ok, err := insertDirectEndpoint(ctx, tx, q, id, group, snap, p.r.Currency, model, name, rows[name].Formats)
		if err != nil {
			return 0, 0, err
		}
		if ok {
			priced++
		}
	}
	return len(names), priced, nil
}

// mapDirectName maps one name of the list into row: the model on sale it stands for, and the formats the API serves
// that model in. It reports whether an endpoint can serve the name; one the catalog does not map keeps no model.
func mapDirectName(snap *catalog.Snapshot, p *DirectPlan, name string, row *store.UpsertProjectedModelParams) (syncer.Mapping, bool) {
	m := syncer.MapUpstream(snap, nil, name)
	model, ok := snap.Model(m.Model)
	if !m.Mapped || !ok {
		return m, false
	}
	mapped := uuid.UUID(m.Model)
	row.ModelID = &mapped
	formats, err := store.FormatNames(p.direct.Formats(p.family, model))
	if err != nil || len(formats) == 0 {
		return m, false
	}
	row.Formats = formats
	return m, true
}

// insertDirectEndpoint makes one endpoint and, when the catalog has the model's official price in the currency, its
// cost schedule; it reports whether it priced it.
func insertDirectEndpoint(ctx context.Context, tx pg.Tx, q *store.Queries, id, group uuid.UUID, snap *catalog.Snapshot, currency platform.Currency, model catalog.ModelID, name string, formats []string) (bool, error) {
	endpoint, err := q.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
		SupplierID: id, SupplierGroupID: group, ModelID: uuid.UUID(model), UpstreamName: name, Formats: formats,
	})
	if err != nil {
		return false, fmt.Errorf("make the endpoint for %s: %w", name, err)
	}
	ref, ok := snap.ReferencePrice(model, currency.String())
	if !ok {
		return false, nil
	}
	schedule, err := ref.Schedule.MarshalJSON()
	if err != nil {
		return false, fmt.Errorf("price the endpoint for %s: %w", name, err)
	}
	cost, err := q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
		SupplierID: id, EndpointID: endpoint, NativeUnit: "usd", Schedule: schedule, Basis: "published",
		Fingerprint: "price_list:" + ref.ID.String(),
	})
	if err != nil {
		return false, fmt.Errorf("price the endpoint for %s: %w", name, err)
	}
	if err := q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: endpoint, CostScheduleID: &cost}); err != nil {
		return false, fmt.Errorf("put the endpoint for %s in service: %w", name, err)
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

// directNames is every name a passed key listed, sorted and once each.
func directNames(checks []DirectKeyCheck) []string {
	var names []string
	for _, c := range checks {
		if c.Outcome == DirectKeyPassed {
			names = append(names, c.models...)
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
		"keys": outcomes, "models": models, "priced_endpoints": priced,
	}
	if p.r.Family != "" {
		detail["api_format"] = p.r.Family
	}
	if p.r.RequestsPerMinute != nil {
		detail["requests_per_minute"] = *p.r.RequestsPerMinute
	}
	return detail
}
