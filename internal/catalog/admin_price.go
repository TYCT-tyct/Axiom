package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The ways publishing a price is refused.
var (
	ErrPriceChanged      = errors.New("the model's current price is not the one the request was decided on")
	ErrSourceMismatch    = errors.New("the model's price source is not the request's")
	ErrPriceLost         = errors.New("the new schedule has no price where the current one has")
	ErrNoEvidence        = errors.New("evidence, a non-empty JSON object, is required")
	ErrSourceUnsupported = errors.New("prices from this source cannot be selected yet")
	ErrIncomplete        = errors.New("the schedule lacks prices for what the model declares")
	ErrNoPrices          = errors.New("at least one price is required, and one per currency")
)

func (p PricePoint) String() string {
	return fmt.Sprintf("%s %s above %d", p.ServiceTier, p.Item, p.Above)
}

// price is the head of a model's history in one currency; a nil schedule means the currency was withdrawn.
type price struct {
	id       uuid.UUID
	schedule *Schedule
	at       time.Time // when it was published
}

// prices are a model's heads by currency: the current price is the row nothing replaces.
type prices map[string]price

func currentPrices(ctx context.Context, tx pg.Tx, model uuid.UUID) (prices, error) {
	rows, err := New().ListCurrentPricesOfModel(ctx, tx, model)
	if err != nil {
		return nil, err
	}
	heads := make(prices, len(rows))
	for _, row := range rows {
		p := price{id: row.ID, at: row.PublishedAt}
		if row.Schedule != nil {
			s, err := ParseSchedule(row.Schedule)
			if err != nil {
				return nil, fmt.Errorf("reference price %s: %w", row.ID, err)
			}
			p.schedule = &s
		}
		heads[row.Currency] = p
	}
	return heads, nil
}

// priced lists the currencies that have a current schedule.
func (p prices) priced() []string {
	var currencies []string
	for _, currency := range slices.Sorted(maps.Keys(p)) {
		if p[currency].schedule != nil {
			currencies = append(currencies, currency)
		}
	}
	return currencies
}

// cover refuses a current schedule that cannot price all of r, which is the listing gate: a listed model must never
// meet an item or a service tier its reference price lacks.
func (p prices) cover(r Reportable) error {
	for _, currency := range p.priced() {
		if err := covers(currency, *p[currency].schedule, r); err != nil {
			return err
		}
	}
	return nil
}

func covers(currency string, s Schedule, r Reportable) error {
	m := s.Missing(r)
	if m.Items == 0 && m.ServiceTiers == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s lacks items %v and service tiers %v", ErrIncomplete, currency,
		m.Items.names(itemNames[:]), m.ServiceTiers.names(serviceTierNames[:]))
}

// newPrice is a schedule submitted to be published, in the form that is stored: canonical, with its hash.
type newPrice struct {
	schedule  Schedule
	canonical []byte
	sha256    [sha256.Size]byte
}

func parsePrice(raw jsonx.Raw) (newPrice, error) {
	s, err := ParseSchedule(raw)
	if err != nil {
		return newPrice{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	canonical, err := s.MarshalJSON()
	if err != nil {
		return newPrice{}, err
	}
	return newPrice{schedule: s, canonical: canonical, sha256: sha256.Sum256(canonical)}, nil
}

func (n newPrice) currency() string { return n.schedule.currency.String() }

// requireEvidence refuses evidence that is not a non-empty JSON object. What the evidence says is the reviewer's to
// judge; the check of excerpts against a page's snapshot comes with page monitoring.
func requireEvidence(raw jsonx.Raw) error {
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(raw, &members); err != nil || len(members) == 0 {
		return ErrNoEvidence
	}
	return nil
}

// appendPrice adds a row to the currency's history after head, or starts it; a nil n withdraws the currency.
func appendPrice(ctx context.Context, tx pg.Tx, model uuid.UUID, currency string, source PriceSource, n *newPrice, head *price) (uuid.UUID, error) {
	arg := InsertReferencePriceParams{ModelID: model, Currency: currency, Source: string(source)}
	if n != nil {
		arg.Schedule, arg.ScheduleSha256 = n.canonical, n.sha256[:]
	}
	if head != nil {
		arg.Supersedes = &head.id
	}
	return New().InsertReferencePrice(ctx, tx, arg)
}

// noLoss refuses a schedule that has no price where the head has one. A changed basis of the thresholds cannot be
// compared; the operator must confirm that, and then the comparison is skipped for that currency.
func noLoss(n newPrice, head price, r Reportable, confirmBasisChange bool) error {
	if head.schedule == nil {
		return nil
	}
	lost, err := n.schedule.Lost(*head.schedule, r)
	switch {
	case errors.Is(err, ErrTierBasisChanged) && confirmBasisChange:
		return nil
	case err != nil:
		return fmt.Errorf("%s: %w", n.currency(), err)
	case len(lost) > 0:
		return fmt.Errorf("%w: %s would lose %v", ErrPriceLost, n.currency(), lost)
	}
	return nil
}

// PublishPriceRequest adds a version to a model's price history in one currency.
type PublishPriceRequest struct {
	Op
	Model ModelID `json:"model"`
	// Base is the current row the request was decided on, nil for a currency with no history.
	Base               *uuid.UUID  `json:"base"`
	Source             PriceSource `json:"source"`
	Schedule           jsonx.Raw   `json:"schedule"`
	Evidence           jsonx.Raw   `json:"evidence"`
	ConfirmBasisChange bool        `json:"confirm_basis_change"`
}

// PublishPrice appends a price version. It is refused when the price changed since the request was decided on, when
// the source is not the model's, when a listed model's schedule would not cover what it declares, and when the
// schedule loses a price the current one has. The history never forks and is never rewritten: a correction is another
// version.
func (c *Catalog) PublishPrice(ctx context.Context, r PublishPriceRequest) (Result, error) {
	n, err := parsePrice(r.Schedule)
	if err != nil {
		return Result{}, err
	}
	if err := requireEvidence(r.Evidence); err != nil {
		return Result{}, err
	}
	return c.apply(ctx, r.Op, "price.publish", func(tx pg.Tx) (outcome, error) { return publishIn(ctx, tx, r, n) })
}

func publishIn(ctx context.Context, tx pg.Tx, r PublishPriceRequest, n newPrice) (outcome, error) {
	row, err := lockedModel(ctx, tx, r.Model)
	if err != nil {
		return outcome{}, err
	}
	if PriceSource(row.PriceSource) != r.Source || Status(row.Status) == StatusRetired {
		return outcome{}, fmt.Errorf("%w: the model's is %s and it is %s", ErrSourceMismatch, row.PriceSource, row.Status)
	}
	spec, err := parseSpec(row.Spec)
	if err != nil {
		return outcome{}, err
	}
	heads, err := currentPrices(ctx, tx, row.ID)
	if err != nil {
		return outcome{}, err
	}
	head, hasHead := heads[n.currency()]
	if hasHead != (r.Base != nil) || hasHead && head.id != *r.Base {
		return outcome{}, fmt.Errorf("%w: it is %v", ErrPriceChanged, head.id)
	}
	if err := checkPublish(row, spec, n, head, r.ConfirmBasisChange); err != nil {
		return outcome{}, err
	}
	var supersedes *price
	if hasHead {
		supersedes = &head
	}
	id, err := appendPrice(ctx, tx, row.ID, n.currency(), r.Source, &n, supersedes)
	return outcome{
		targetKind: "model", subject: r.Model.String(), changesSnapshot: Status(row.Status) == StatusListed,
		before:   map[string]any{"price_id": r.Base},
		after:    map[string]any{"price_id": id.String(), "currency": n.currency(), "schedule": jsonx.Raw(n.canonical)},
		evidence: r.Evidence,
	}, err
}

// checkPublish applies the gates to a schedule about to follow head: a listed model's listing gate, and the loss check.
func checkPublish(row CatalogModel, spec spec, n newPrice, head price, confirmBasisChange bool) error {
	if Status(row.Status) == StatusListed {
		if err := covers(n.currency(), n.schedule, spec.reportable); err != nil {
			return err
		}
	}
	return noLoss(n, head, spec.reportable, confirmBasisChange)
}

// SelectPriceSourceRequest changes where a model's prices come from.
type SelectPriceSourceRequest struct {
	Op
	Model  ModelID     `json:"model"`
	Rev    int64       `json:"rev"`
	Source PriceSource `json:"source"`
	// Bases are the current price row of each currency that has any history, as the request was decided on: the
	// switch replaces or withdraws them all, so it is checked against all of them, as PublishPrice checks its base.
	Bases map[string]uuid.UUID `json:"bases"`
	// Prices are the new source's schedules, one per currency it has, each with its evidence.
	Prices             []SourcePrice `json:"prices"`
	ConfirmBasisChange bool          `json:"confirm_basis_change"`
}

// SourcePrice is a schedule and what supports it.
type SourcePrice struct {
	Schedule jsonx.Raw `json:"schedule"`
	Evidence jsonx.Raw `json:"evidence"`
}

// SelectPriceSource switches the model to another source in one transaction: the source column changes, every
// currency the new source has gets a new version (even an identical schedule, since the source differs), and every
// currency it lacks is withdrawn, so no price of the old source stays in force. No price may be lost on the way: each
// new schedule must keep every price the current one has, or the operator must confirm a changed threshold basis.
// Only the sources whose prices an operator supplies, official and manual, are selected with this: a dataset's prices
// are taken from its entry, by SelectDatasetSource.
func (c *Catalog) SelectPriceSource(ctx context.Context, r SelectPriceSourceRequest) (Result, error) {
	if r.Source != SourceOfficial && r.Source != SourceManual {
		return Result{}, fmt.Errorf("%w: %s", ErrSourceUnsupported, r.Source)
	}
	return c.selectSource(ctx, r)
}

func (c *Catalog) selectSource(ctx context.Context, r SelectPriceSourceRequest) (Result, error) {
	news, err := parseSourcePrices(r.Prices)
	if err != nil {
		return Result{}, err
	}
	return c.apply(ctx, r.Op, "model.select_price_source", func(tx pg.Tx) (outcome, error) { return switchIn(ctx, tx, r, news) })
}

func switchIn(ctx context.Context, tx pg.Tx, r SelectPriceSourceRequest, news map[string]pricedFrom) (outcome, error) {
	row, err := lockReviewed(ctx, tx, r.Model, r.Rev)
	if err != nil {
		return outcome{}, err
	}
	if Status(row.Status) == StatusRetired {
		return outcome{}, fmt.Errorf("%w: it is retired", ErrWrongStatus)
	}
	spec, err := parseSpec(row.Spec)
	if err != nil {
		return outcome{}, err
	}
	heads, err := currentPrices(ctx, tx, row.ID)
	if err != nil {
		return outcome{}, err
	}
	if err := checkBases(heads, r.Bases); err != nil {
		return outcome{}, err
	}
	if err := checkSwitch(row, spec, heads, news, r.ConfirmBasisChange); err != nil {
		return outcome{}, err
	}
	o := outcome{targetKind: "model", subject: r.Model.String(), before: summary(row), changesSnapshot: Status(row.Status) == StatusListed}
	row.PriceSource = string(r.Source)
	saved, err := saveModel(ctx, tx, row)
	if err != nil {
		return outcome{}, err
	}
	o.after, o.evidence, err = writeSwitch(ctx, tx, saved, r.Source, heads, news)
	return o, err
}

// pricedFrom is a new schedule with the evidence for it.
type pricedFrom struct {
	newPrice
	evidence jsonx.Raw
}

func parseSourcePrices(in []SourcePrice) (map[string]pricedFrom, error) {
	news := make(map[string]pricedFrom, len(in))
	for _, p := range in {
		n, err := parsePrice(p.Schedule)
		if err != nil {
			return nil, err
		}
		if err := requireEvidence(p.Evidence); err != nil {
			return nil, fmt.Errorf("%s: %w", n.currency(), err)
		}
		if _, dup := news[n.currency()]; dup {
			return nil, fmt.Errorf("%w: %s is given twice", ErrNoPrices, n.currency())
		}
		news[n.currency()] = pricedFrom{n, p.Evidence}
	}
	if len(news) == 0 {
		return nil, ErrNoPrices
	}
	return news, nil
}

// checkBases refuses a switch decided on other price heads than the ones now current, in any currency.
func checkBases(heads prices, bases map[string]uuid.UUID) error {
	for currency, head := range heads {
		if base, ok := bases[currency]; !ok || base != head.id {
			return fmt.Errorf("%w: %s is %v", ErrPriceChanged, currency, head.id)
		}
	}
	for currency := range bases {
		if _, ok := heads[currency]; !ok {
			return fmt.Errorf("%w: %s has no history", ErrPriceChanged, currency)
		}
	}
	return nil
}

// checkSwitch applies the gates to every new schedule. Each is compared with every currency that has a price now, not
// only its own: the currencies the new source lacks are withdrawn, so a price that only they had would be lost.
func checkSwitch(row CatalogModel, spec spec, heads prices, news map[string]pricedFrom, confirmBasisChange bool) error {
	for _, currency := range slices.Sorted(maps.Keys(news)) {
		n := news[currency].newPrice
		if Status(row.Status) == StatusListed {
			if err := covers(currency, n.schedule, spec.reportable); err != nil {
				return err
			}
		}
		for _, old := range slices.Sorted(maps.Keys(heads)) {
			if err := noLoss(n, heads[old], spec.reportable, confirmBasisChange); err != nil {
				return fmt.Errorf("against the current %s price: %w", old, err)
			}
		}
	}
	return nil
}

// writeSwitch appends the new versions and the withdrawals, and returns the change's after and evidence.
func writeSwitch(ctx context.Context, tx pg.Tx, row CatalogModel, source PriceSource, heads prices, news map[string]pricedFrom) (after, evidence map[string]any, err error) {
	after = map[string]any{"price_source": source, "rev": row.Rev}
	evidence = map[string]any{}
	for _, currency := range slices.Sorted(maps.Keys(heads)) {
		if _, kept := news[currency]; kept || heads[currency].schedule == nil {
			continue
		}
		head := heads[currency]
		if _, err := appendPrice(ctx, tx, row.ID, currency, source, nil, &head); err != nil {
			return nil, nil, err
		}
		after[currency] = "withdrawn"
	}
	for _, currency := range slices.Sorted(maps.Keys(news)) {
		n := news[currency].newPrice
		evidence[currency] = news[currency].evidence
		var head *price
		if h, ok := heads[currency]; ok {
			head = &h
		}
		id, err := appendPrice(ctx, tx, row.ID, currency, source, &n, head)
		if err != nil {
			return nil, nil, err
		}
		after[currency] = map[string]any{"price_id": id.String(), "schedule": jsonx.Raw(n.canonical)}
	}
	return after, evidence, nil
}
