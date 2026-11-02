package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// PricePolicy controls automatic publication. All ratios are exact nonnegative decimals; the floor is a multiplier.
type PricePolicy struct {
	Rev              int64  `json:"rev"`
	FloorMultiplier  string `json:"floor_multiplier"`
	ChangeRatio      string `json:"change_ratio"`
	MaxIncreaseRatio string `json:"max_increase_ratio"`
}

// Publication keeps a proposal separately from its current immutable sale version. Empty schedules mean unavailable.
type Publication struct {
	matched   bool
	Audience  Selector        `json:"audience,omitzero"`
	Model     catalog.ModelID `json:"model_id"`
	Candidate uuid.UUID       `json:"candidate_id"`
	Current   *uuid.UUID      `json:"current_id,omitempty"`
	Status    string          `json:"status"`
	Requested jsonx.Raw       `json:"requested,omitempty"`
	Schedule  jsonx.Raw       `json:"schedule,omitempty"`
	// Endpoints and RequestedEndpoints are the per-endpoint prices of a cost-plus proposal, before and after the floor.
	// They are absent for a rule whose price is the same for every endpoint; an old candidate without them is read as
	// the shared price it was.
	Endpoints          EndpointPrices `json:"endpoints,omitempty"`
	RequestedEndpoints EndpointPrices `json:"requested_endpoints,omitempty"`
	CurrentEndpoints   EndpointPrices `json:"current_endpoints,omitempty"`
	CurrentSchedule    jsonx.Raw      `json:"current_schedule,omitempty"`
	Costs              EndpointPrices `json:"costs,omitempty"`
	Floors             EndpointPrices `json:"floors,omitempty"`
	Cost               jsonx.Raw      `json:"cost,omitempty"`
	Floor              jsonx.Raw      `json:"floor,omitempty"`
	Basis              jsonx.Raw      `json:"basis,omitempty"`
	Problem            string         `json:"problem,omitempty"`
}

// ErrStalePrice refuses a confirmation decided against superseded price evidence.
var ErrStalePrice = errors.New("billing: the price proposal or publication changed; refresh before confirming")

// PriceState reads publication state without locking or writing.
func PriceState(ctx context.Context, tx pg.Tx, site uuid.UUID) (PricePolicy, []Publication, error) {
	policy, err := loadPricePolicy(ctx, tx, site)
	if err != nil {
		return PricePolicy{}, nil, err
	}
	rows, err := queries.ReadPublications(ctx, tx, site)
	if err != nil {
		return PricePolicy{}, nil, err
	}
	out := make([]Publication, 0, len(rows))
	for _, r := range rows {
		var p Publication
		if err := jsonx.Unmarshal(r.Candidate, &p); err != nil {
			return PricePolicy{}, nil, err
		}
		p.Audience = Selector{Kind: r.AudienceKind, ID: r.AudienceID}
		p.Current, p.Status = r.CurrentID, r.Status
		// The current columns, not a view field a candidate may carry from an older release, decide what is shown.
		p.CurrentSchedule, p.CurrentEndpoints = r.Schedule, nil
		if len(r.Endpoints) > 0 {
			if err := jsonx.Unmarshal(r.Endpoints, &p.CurrentEndpoints); err != nil {
				return PricePolicy{}, nil, err
			}
		}
		out = append(out, p)
	}
	return policy, out, nil
}

// LockPricePolicy serializes publishers before they read canonical database inputs.
func LockPricePolicy(ctx context.Context, tx pg.Tx, site uuid.UUID) (PricePolicy, error) {
	if err := queries.EnsurePricePolicy(ctx, tx, EnsurePricePolicyParams{SiteID: site, FloorMultiplier: defaultPricePolicy().FloorMultiplier, ChangeRatio: defaultPricePolicy().ChangeRatio, MaxIncreaseRatio: defaultPricePolicy().MaxIncreaseRatio}); err != nil {
		return PricePolicy{}, err
	}
	r, err := queries.LockPricePolicy(ctx, tx, site)
	return PricePolicy(r), err
}

// SetPricePolicy uses optimistic concurrency and audits the change in the caller's platform transaction.
func SetPricePolicy(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, in PricePolicy, reason string) error {
	for _, f := range []struct {
		name, value string
		min, max    int64
	}{
		{"floor_multiplier", in.FloorMultiplier, 1, 10}, {"change_ratio", in.ChangeRatio, 0, 10}, {"max_increase_ratio", in.MaxIncreaseRatio, 0, 10},
	} {
		v, _, err := ratio(f.name, &f.value)
		if err != nil {
			return err
		}
		if v.Cmp(big.NewRat(f.min, 1)) < 0 || v.Cmp(big.NewRat(f.max, 1)) > 0 {
			return &RuleError{f.name, "is outside the supported range"}
		}
	}
	if err := priceReason(reason); err != nil {
		return err
	}
	old, err := LockPricePolicy(ctx, tx, site)
	if err != nil {
		return err
	}
	if old.Rev != in.Rev {
		return ErrStalePrice
	}
	if err := queries.UpdatePricePolicy(ctx, tx, UpdatePricePolicyParams{SiteID: site, FloorMultiplier: in.FloorMultiplier, ChangeRatio: in.ChangeRatio, MaxIncreaseRatio: in.MaxIncreaseRatio}); err != nil {
		return err
	}
	in.Rev++
	return priceAudit(ctx, tx, actor, site, "price_policy.update", reason, map[string]any{"before": old, "after": in})
}

// RefreshPrices reconciles input state, not events. It serializes publishers on the policy row and publishes only
// safe prices. Costs must be from the same immutable endpoint/rate reading the caller will put into routing.
func RefreshPrices(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, costs []PriceCost, rates ...platform.ExchangeRate) (map[catalog.ModelID]Sold, error) {
	policy, err := LockPricePolicy(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rules, err := Rules(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	rows, err := queries.ReadPublications(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	grouped, previous := publicationInputs(rules, rows)
	byModel := map[catalog.ModelID][]PriceCost{}
	for _, c := range costs {
		byModel[c.Model] = append(byModel[c.Model], c)
	}
	defaults, err := priceDefaults(ctx, tx, currency)
	if err != nil {
		return nil, err
	}
	// Reconcile previous selectors too: an edit may have moved the rule to a different audience.
	for audience, rules := range grouped {
		if err := refreshAudience(ctx, tx, site, currency, snap, byModel, defaults, policy, audience, rules, previous[audience], rates); err != nil {
			return nil, err
		}
	}
	if err := queries.ConfirmPriceRefresh(ctx, tx, site); err != nil {
		return nil, err
	}
	return PublishedPrices(ctx, tx, site)
}

func publicationInputs(rules []Rule, rows []ReadPublicationsRow) (map[Selector][]Rule, map[Selector]map[catalog.ModelID]ReadPublicationsRow) {
	previous := map[Selector]map[catalog.ModelID]ReadPublicationsRow{}
	grouped := map[Selector][]Rule{{Kind: "all"}: nil}
	for _, r := range rules {
		if r.Enabled {
			grouped[r.Audience] = append(grouped[r.Audience], r)
		}
	}
	for _, r := range rows {
		audience := Selector{Kind: r.AudienceKind, ID: r.AudienceID}
		if previous[audience] == nil {
			previous[audience] = map[catalog.ModelID]ReadPublicationsRow{}
		}
		previous[audience][catalog.ModelID(r.ModelID)] = r
	}
	for audience := range previous {
		if _, ok := grouped[audience]; !ok {
			grouped[audience] = nil
		}
	}
	return grouped, previous
}

func refreshAudience(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, costs map[catalog.ModelID][]PriceCost, defaults fallbacks, policy PricePolicy, audience Selector, rules []Rule, previous map[catalog.ModelID]ReadPublicationsRow, rates []platform.ExchangeRate) error {
	for m := range snap.Models() {
		old := previous[m.ID]
		if audience.Kind != "all" && old.CandidateID == uuid.Nil() && !slices.ContainsFunc(rules, func(r Rule) bool { return r.Enabled && pricesModel(r.Target, m) }) {
			continue
		}
		p, err := proposeAudiencePrice(audience, policy, rules, m, snap, currency, costs[m.ID], defaults, rates...)
		if err != nil {
			return err
		}
		if audience.Kind != "all" && !p.matched && old.CandidateID == uuid.Nil() {
			continue
		}
		if _, err := reconcilePrice(ctx, tx, site, policy, p, old); err != nil {
			return fmt.Errorf("billing: refresh price %s: %w", m.ID, err)
		}
	}
	return nil
}

func digestPrice(raw []byte) uuid.UUID {
	sum := sha256.Sum256(raw)
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = id[6]&0x0f | 0x80
	id[8] = id[8]&0x3f | 0x80
	return id
}

func reconcilePrice(ctx context.Context, tx pg.Tx, site uuid.UUID, policy PricePolicy, p Publication, old ReadPublicationsRow) (Sold, error) {
	if p.Candidate == old.CandidateID {
		if old.Status == "published" || old.Status == "pending" {
			return soldPublication(old)
		}
		return Sold{}, nil
	}
	p.Current = old.CurrentID
	current, status, err := publicationDecision(policy, p, old)
	if err != nil {
		return Sold{}, err
	}
	p.Status = status
	if status == "published" {
		current, err = publishVersion(ctx, tx, audit.System("billing.prices"), site, p, false, "automatic price publication")
		if err != nil {
			return Sold{}, err
		}
		p.Current = &current.Version
	}
	if err := publicationSignal(ctx, tx, site, p, false, old.Status); err != nil {
		return Sold{}, err
	}
	if err := persistPublication(ctx, tx, site, p); err != nil {
		return Sold{}, err
	}
	return current, nil
}

func publicationDecision(policy PricePolicy, p Publication, old ReadPublicationsRow) (Sold, string, error) {
	if len(p.Schedule) == 0 {
		return Sold{}, "unpriced", nil
	}
	next, err := catalog.ParseSchedule(p.Schedule)
	if err != nil {
		return Sold{}, "", err
	}
	previous, err := catalog.ParseSchedule(p.Requested)
	if err != nil {
		return Sold{}, "", err
	}
	floor, err := catalog.ParseSchedule(p.Floor)
	if err != nil {
		return Sold{}, "", err
	}
	var current Sold
	if old.CurrentID != nil {
		current, err = soldPublication(old)
		if err != nil {
			return Sold{}, "", err
		}
		// A shared version has no endpoint map, so its cost evidence is the only record of which endpoints it
		// prices. It is read only on this decision path, not when routing loads every published price.
		if current.Endpoints == nil {
			current.endpointCosts = endpointCostsFromBasis(old.Basis, current.Schedule.Currency())
		}
		previous = current.Schedule
	}
	status := automaticPriceStatus(policy, previous, next, floor, old.CurrentID != nil)
	if old.CurrentID != nil && current.Endpoints == nil && p.Endpoints == nil && status == "published" {
		// The price itself did not move, but adding or removing an endpoint changes what the shared price covers.
		// Cost evidence on both sides is the only record of the endpoint set; without it, a valid shared proposal
		// could not have been built, so an unreadable side only leaves the ordinary gate in place.
		nextCosts := endpointCostsFromBasis(p.Basis, next.Currency())
		if len(current.endpointCosts) > 0 && len(nextCosts) > 0 &&
			endpointMembershipMoved(endpointSet(current.endpointCosts), endpointSet(nextCosts)) {
			status = "pending"
		}
	}
	switch {
	case p.Endpoints != nil:
		status = automaticEndpointPriceStatus(policy, current, p.Endpoints, p.Floors, old.CurrentID != nil)
	case old.CurrentID != nil && current.Endpoints != nil:
		// The proposal goes back to a shared fixed or reference price. Every endpoint's current price is a real old
		// charge, so compare each one with the shared proposal rather than only the model envelope, which may not move.
		// The proposal's own cost evidence recovers its endpoint floors; an old candidate without it falls back to
		// the ordinary move gate, which still holds any change for confirmation.
		nextFloors, _ := endpointFloorsFromBasis(p.Basis, next.Currency(), policy.FloorMultiplier)
		status = automaticSharedEndpointPriceStatus(policy, current, next, nextFloors)
	}
	if status == "blocked" {
		current = Sold{}
	}
	return current, status, nil
}

func automaticPriceStatus(policy PricePolicy, previous, next, floor catalog.Schedule, hasHead bool) string {
	safe, err := previous.CoversCost(floor)
	safe = hasHead && safe && err == nil
	if !safe && priceIncreased(previous, next, policy.MaxIncreaseRatio) {
		return "blocked"
	}
	if !hasHead || !safe || !priceMoved(previous, next, policy.ChangeRatio) {
		return "published"
	}
	return "pending"
}

func priceIncreased(previous, next catalog.Schedule, ratio string) bool {
	r, _ := new(big.Rat).SetString(ratio)
	moved, err := previous.PriceIncrease(next, r)
	return moved || err != nil
}

func priceMoved(previous, next catalog.Schedule, ratio string) bool {
	r, _ := new(big.Rat).SetString(ratio)
	moved, err := previous.PriceChange(next, r)
	return moved || err != nil
}

func soldPublication(r ReadPublicationsRow) (Sold, error) {
	if r.CurrentID == nil {
		return Sold{}, nil
	}
	s, err := catalog.ParseSchedule(r.Schedule)
	if err != nil {
		return Sold{}, err
	}
	endpoints, err := parseEndpointPrices(r.Endpoints)
	if err != nil {
		return Sold{}, err
	}
	return Sold{Version: *r.CurrentID, Schedule: s, Endpoints: endpoints}, nil
}

func persistPublication(ctx context.Context, tx pg.Tx, site uuid.UUID, p Publication) error {
	// CurrentEndpoints and CurrentSchedule are a read view of the joined current version, not part of the proposal.
	// PublishPrice reads them from PriceState and persists the candidate afterwards; keeping them would make an old
	// view override the row's actual current version after the next publication.
	p.CurrentEndpoints, p.CurrentSchedule = nil, nil
	raw, err := jsonx.MarshalCanonical(p)
	if err != nil {
		return err
	}
	if p.Audience.Kind != "all" {
		return queries.SaveAudiencePublication(ctx, tx, SaveAudiencePublicationParams{SiteID: site, ModelID: uuid.UUID(p.Model), AudienceKind: p.Audience.Kind, AudienceID: p.Audience.ID, CandidateID: p.Candidate, Candidate: raw, CurrentID: p.Current, Status: p.Status})
	}
	return queries.SavePublication(ctx, tx, SavePublicationParams{SiteID: site, ModelID: uuid.UUID(p.Model), CandidateID: p.Candidate, Candidate: raw, CurrentID: p.Current, Status: p.Status})
}

// PublishPrice confirms exactly the proposal and head the administrator read. The HTTP idempotency transaction
// handles acknowledgement loss. An override uses the rule's requested price and is scoped to this evidence alone.
func PublishPrice(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, model catalog.ModelID, candidate uuid.UUID, current *uuid.UUID, confirmed, override bool, reason string) (uuid.UUID, error) {
	if err := priceReason(reason); err != nil {
		return uuid.Nil(), err
	}
	if !confirmed {
		return uuid.Nil(), &RuleError{"confirmed", "must explicitly confirm publication"}
	}
	if _, err := LockPricePolicy(ctx, tx, site); err != nil {
		return uuid.Nil(), err
	}
	_, prices, err := PriceState(ctx, tx, site)
	if err != nil {
		return uuid.Nil(), err
	}
	at := slices.IndexFunc(prices, func(p Publication) bool { return p.Model == model && p.Candidate == candidate })
	if at < 0 {
		return uuid.Nil(), ErrStalePrice
	}
	p := prices[at]
	if p.Candidate != candidate || !samePriceHead(p.Current, current) {
		return uuid.Nil(), ErrStalePrice
	}
	if len(p.Schedule) == 0 {
		return uuid.Nil(), &RuleError{"candidate_id", "has no known safe price; refresh its cost and rule"}
	}
	sold, err := publishVersion(ctx, tx, actor, site, p, override, reason)
	if err != nil {
		return uuid.Nil(), err
	}
	p.Current = &sold.Version
	p.Status = "published"
	if err := publicationSignal(ctx, tx, site, p, true, ""); err != nil {
		return uuid.Nil(), err
	}
	return sold.Version, persistPublication(ctx, tx, site, p)
}

func samePriceHead(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func priceReason(reason string) error {
	if strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 1000 {
		return &RuleError{"reason", "must contain 1 to 1000 characters"}
	}
	return nil
}

func publishVersion(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, p Publication, override bool, reason string) (Sold, error) {
	raw := p.Schedule
	endpoints := p.Endpoints
	if override {
		if p.Endpoints != nil && p.RequestedEndpoints == nil {
			return Sold{}, &RuleError{"candidate_id", "has no requested endpoint prices; refresh before confirming"}
		}
		raw = p.Requested
		endpoints = p.RequestedEndpoints
	}
	s, err := catalog.ParseSchedule(raw)
	if err != nil {
		return Sold{}, err
	}
	var endpointBody []byte
	if endpoints != nil {
		endpointBody, err = jsonx.MarshalCanonical(endpoints)
		if err != nil {
			return Sold{}, err
		}
	}
	id := uuid.NewV7()
	if err := queries.InsertPriceVersion(ctx, tx, InsertPriceVersionParams{ID: id, SiteID: site, ModelID: uuid.UUID(p.Model), AudienceKind: p.Audience.Kind, AudienceID: p.Audience.ID, CandidateID: p.Candidate, Schedule: raw, Basis: p.Basis, Endpoints: endpointBody, Overridden: override, Actor: actor.String(), Reason: reason}); err != nil {
		return Sold{}, err
	}
	raised, err := priceRaised(p)
	if err != nil {
		return Sold{}, err
	}
	err = priceAudit(ctx, tx, actor, id, "price.publish", reason, map[string]any{"model": p.Model, "audience": p.Audience, "candidate": p.Candidate, "overridden": override, "raised_to_floor": raised && !override})
	return Sold{Version: id, Schedule: s, Endpoints: endpoints}, err
}

func priceAudit(ctx context.Context, tx pg.Tx, actor audit.Actor, id uuid.UUID, action, reason string, detail map[string]any) error {
	kind := "price_version"
	if action == "price_policy.update" {
		kind = "price_policy"
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Reason: reason, Target: audit.Target{Module: "billing", Kind: kind, ID: id.String()}, Detail: detail})
}

func publicationSignal(ctx context.Context, tx pg.Tx, site uuid.UUID, p Publication, acknowledged bool, previous string) error {
	kind := "billing.price.review"
	severity := "warning"
	raised, err := priceRaised(p)
	if err != nil {
		return err
	}
	resolved := acknowledged || p.Status == "published" && !raised
	if p.Status == "unpriced" {
		severity, err = unpricedSeverity(p, previous)
		if err != nil || severity == "" {
			return err
		}
		resolved = severity == "info"
	}

	if p.Status == "blocked" {
		if previous == "blocked" {
			return nil
		}
		severity = "critical"
	}
	key := site.String() + ":" + p.Model.String()
	if p.Audience.Kind != "all" {
		key += ":" + p.Audience.Kind + ":" + p.Audience.ID
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: kind, Key: key, Severity: severity, Resolved: resolved, Fields: map[string]string{"model": p.Model.String(), "status": p.Status, "candidate": p.Candidate.String()}})
}

func unpricedSeverity(p Publication, previous string) (string, error) {
	var basis struct {
		Rule  *Rule       `json:"rule"`
		Costs []jsonx.Raw `json:"costs"`
	}
	if err := jsonx.Unmarshal(p.Basis, &basis); err != nil {
		return "", err
	}
	if basis.Rule == nil || len(basis.Costs) == 0 {
		if previous == "" {
			return "", nil
		}
		return "info", nil
	}
	switch previous {
	case "unpriced":
		return "", nil
	case "published", "pending", "blocked":
		return "critical", nil
	default:
		return "warning", nil
	}
}

func priceRaised(p Publication) (bool, error) {
	if len(p.Schedule) == 0 {
		return false, nil
	}
	requested, err := catalog.ParseSchedule(p.Requested)
	if err != nil {
		return false, err
	}
	protected, err := catalog.ParseSchedule(p.Schedule)
	if err != nil {
		return false, err
	}
	if p.Endpoints != nil && p.RequestedEndpoints != nil {
		// The model-level envelope can stay the same while one endpoint is raised to its own floor. The audit and the
		// review signal are about every price this version publishes, so compare the endpoint schedules too.
		for endpoint, wanted := range p.RequestedEndpoints {
			protected, ok := p.Endpoints[endpoint]
			if !ok {
				continue
			}
			raised, err := wanted.PriceChange(protected, big.NewRat(0, 1))
			if err != nil || raised {
				return raised, err
			}
		}
		return false, nil
	}
	return requested.PriceChange(protected, big.NewRat(0, 1))
}
