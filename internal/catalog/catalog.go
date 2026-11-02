package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Config departs from the defaults only deliberately: they live here.
type Config struct {
	MaxStale  time.Duration // how long a snapshot may go unconfirmed before the node stops resolving; default 30s
	PollEvery time.Duration // how often a node asks the database for the revision; default 5s
	Logger    *slog.Logger  // default slog.Default()
	// PriceChangeLimit is the percent by which a dataset's price for an item may differ from the current one before the
	// sync holds it for a person; default 25. billing's reference-price safety valve (docs/domains/billing.md「定价」)
	// is above the catalog and cannot be called from here, so the catalog keeps its own until that valve exists.
	PriceChangeLimit int
	// CountedItems allows publishing prices for the items counted per piece, second or character. An older release's
	// parser rejects their names, and a node that cannot parse one price drops the whole snapshot and stops serving
	// every model, so this stays off until every node runs a release that reads them: this release reads them; the
	// release that opens the image, audio and speech endpoints, after the fleet runs this one, turns this on
	// (README.md「新增计费项与滚动升级」).
	CountedItems bool
	// RequestPrices allows publishing the per-request item. It has its own switch because the release that first read
	// request prices is not the release that opened counted items; the same rolling-upgrade rule applies: keep it off
	// until every node runs a release that reads "request" (README.md「新增计费项与滚动升级」).
	RequestPrices bool
	// FileSearch allows the file_search item and ability, which add names to both closed enumerations a spec and a
	// price schedule carry. It has its own switch because the release that first reads them is not the release that
	// publishes them: keep it off until every node runs a release that reads them, like RequestPrices.
	FileSearch bool
}

// ErrStale is what Current returns when the snapshot is missing, or the database has not confirmed it for longer than
// MaxStale: a model taken down elsewhere must not be called here for long.
var ErrStale = errors.New("the catalog snapshot is missing or stale")

// ErrCountedItems is what a publish or a source switch returns for a price on an item counted per piece, second or
// character while Config.CountedItems is off: a node running an older release cannot parse that name and would drop the
// whole snapshot.
var ErrCountedItems = errors.New("catalog: prices for counted items wait until every node reads them")

// ErrRequestPrices is what a publish or a source switch returns for a per-request price while Config.RequestPrices is
// off: an older release's parser rejects the name and would drop the whole snapshot.
var ErrRequestPrices = errors.New("catalog: request prices wait until every node reads them")

// ErrFileSearch is what a publish, a spec save or a source switch returns for the file_search item or ability while
// Config.FileSearch is off: an older release's parser rejects the names and would drop the whole snapshot.
var ErrFileSearch = errors.New("catalog: file search waits until every node reads it")

// Catalog holds the node's snapshot and runs the admin operations. The platform scope is given by cmd/axiom.
type Catalog struct {
	db    *pg.DB
	scope pg.Scope
	cfg   Config
	now   func() time.Time
	state atomic.Pointer[state]
}

// state is the snapshot and when the database last confirmed that its revision was the current one.
type state struct {
	snap      *Snapshot
	confirmed time.Time
}

// Open returns a catalog with no snapshot yet: it is not ready until Refresh or Run has loaded one. MaxStale must
// exceed PollEvery, or a healthy node would go stale between two polls.
func Open(db *pg.DB, scope pg.Scope, cfg Config) (*Catalog, error) {
	cfg.MaxStale = cmpOr(cfg.MaxStale, 30*time.Second)
	cfg.PollEvery = cmpOr(cfg.PollEvery, 5*time.Second)
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PriceChangeLimit == 0 {
		cfg.PriceChangeLimit = 25
	}
	if cfg.PriceChangeLimit < 0 {
		return nil, errors.New("catalog: the price change limit must be positive")
	}
	if cfg.MaxStale <= 0 || cfg.PollEvery <= 0 {
		return nil, errors.New("catalog: the staleness limit and the polling period must be positive")
	}
	if cfg.MaxStale <= cfg.PollEvery {
		return nil, fmt.Errorf("catalog: the staleness limit %s must exceed the polling period %s", cfg.MaxStale, cfg.PollEvery)
	}
	return &Catalog{db: db, scope: scope, cfg: cfg, now: time.Now}, nil
}

func cmpOr(d, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return d
}

// Current returns the snapshot, or ErrStale. A request takes it once and uses it throughout.
func (c *Catalog) Current() (*Snapshot, error) {
	st := c.state.Load()
	if st == nil || c.now().Sub(st.confirmed) > c.cfg.MaxStale {
		return nil, ErrStale
	}
	return st.snap, nil
}

// Ready reports whether Current would give a snapshot.
func (c *Catalog) Ready() bool {
	_, err := c.Current()
	return err == nil
}

// Run keeps the snapshot current until ctx ends: it loads at once, then asks for the revision every PollEvery. A
// failed refresh is logged and leaves the old snapshot, which goes stale on its own if the failures last.
func (c *Catalog) Run(ctx context.Context) error {
	tick := time.NewTicker(c.cfg.PollEvery)
	defer tick.Stop()
	for {
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
			c.cfg.Logger.Error("catalog refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Refresh asks the database for the revision and loads a new snapshot if it changed. The snapshot is confirmed as of
// the moment the revision was read, which is only before the read's answer.
func (c *Catalog) Refresh(ctx context.Context) error {
	asked := c.now()
	var revision int64
	if err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		revision, err = New().GetRevision(ctx, tx)
		return err
	}); err != nil {
		return fmt.Errorf("catalog: read the revision: %w", err)
	}
	if st := c.state.Load(); st != nil && st.snap.revision == revision {
		c.publish(st.snap, asked)
		return nil
	}
	snap, loaded, err := c.load(ctx)
	if err != nil {
		return err
	}
	c.publish(snap, loaded)
	return nil
}

// publish replaces the state with snap confirmed at the given time, if that is newer: a snapshot of a lower revision
// never replaces a higher one, and one of the same revision only renews it.
func (c *Catalog) publish(snap *Snapshot, confirmed time.Time) {
	for {
		old := c.state.Load()
		if old != nil {
			switch {
			case old.snap.revision > snap.revision:
				return
			case old.snap.revision == snap.revision:
				snap = old.snap
				if !confirmed.After(old.confirmed) {
					return
				}
			}
		}
		if c.state.CompareAndSwap(old, &state{snap, confirmed}) {
			return
		}
	}
}

// load builds a snapshot from one repeatable-read transaction, so its tables are one moment of the database, and
// returns when that transaction began. The platform scope reads retired models' names too.
func (c *Catalog) load(ctx context.Context) (*Snapshot, time.Time, error) {
	began := c.now()
	var snap *Snapshot
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		snap, err = ReadSnapshot(ctx, tx)
		return err
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("catalog: load the snapshot: %w", err)
	}
	return snap, began, nil
}

// ReadSnapshot loads database-owned catalog inputs in the caller's transaction.
func ReadSnapshot(ctx context.Context, tx pg.Tx) (*Snapshot, error) {
	q := New()
	revision, err := q.GetRevision(ctx, tx)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{
		revision: revision, models: map[ModelID]Model{}, names: map[string]nameEntry{},
		aliases: map[string]ModelID{}, prices: map[priceKey]ReferencePrice{},
	}
	models, err := q.ListModelsForSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, row := range models {
		m, err := modelFromRow(row)
		if err != nil {
			return nil, err
		}
		snap.models[m.ID] = m
		if m.Status == StatusListed {
			snap.listed = append(snap.listed, m.ID)
		}
	}
	sortModels(snap.listed)
	if err := addAuthorNames(ctx, tx, snap.models); err != nil {
		return nil, err
	}
	names, err := q.ListNamesForSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	snap.addNames(names)
	aliases, err := q.ListAliasesForSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	snap.addAliases(aliases)
	prices, err := q.ListCurrentPricesForSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, row := range prices {
		p, err := priceFromRow(row)
		if err != nil {
			return nil, err
		}
		snap.prices[priceKey{p.Model, p.Currency}] = p
	}
	return snap, nil
}

// addAuthorNames resolves each model's author to its display name, which a verification answer must name: the slug is
// the catalog's key, not what a model calls its maker (docs/domains/quality.md「真伪检测」). An author row without a
// name falls back to its slug, so a check never compares an answer with an empty claim.
func addAuthorNames(ctx context.Context, tx pg.Tx, models map[ModelID]Model) error {
	authors, err := New().ListAuthors(ctx, tx)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(authors))
	for _, a := range authors {
		names[a.Slug] = a.Name
	}
	for id, m := range models {
		m.AuthorName = names[m.Author]
		if m.AuthorName == "" {
			m.AuthorName = m.Author
		}
		models[id] = m
	}
	return nil
}

func modelFromRow(row CatalogModel) (Model, error) {
	spec, err := parseSpec(row.Spec)
	if err != nil {
		return Model{}, fmt.Errorf("model %s: %w", row.Slug, err)
	}
	return Model{
		ID: ModelID(row.ID), Slug: row.Slug, Author: row.Author, Status: Status(row.Status),
		VersionBasis: VersionBasis(row.VersionBasis), PriceSource: PriceSource(row.PriceSource),
		spec: string(row.Spec), Rev: row.Rev, reportable: spec.reportable, serving: spec.serving, serves: spec.serves,
		directServes: spec.directServes, dialect: spec.dialect, toolUses: spec.toolUses, speed: spec.speed,
		reasoning: spec.reasoning, alwaysSearches: spec.alwaysSearches,
	}, nil
}

func priceFromRow(row CatalogReferencePrice) (ReferencePrice, error) {
	schedule, err := ParseSchedule(row.Schedule)
	if err != nil {
		return ReferencePrice{}, fmt.Errorf("reference price %s: %w", row.ID, err)
	}
	return ReferencePrice{
		ID: row.ID, Model: ModelID(row.ModelID), Currency: row.Currency, Source: PriceSource(row.Source),
		Schedule: schedule, PublishedAt: row.PublishedAt,
	}, nil
}

// specJSON is the part of a model's spec the catalog reads itself: what the upstream may report and what an endpoint of
// the model offers. The rest of the baseline is the spec's own business. Nil members are left out, not zeroed: absent
// and false or zero are different claims.
type specJSON struct {
	Items     *[]string        `json:"items"`
	Tiers     *[]string        `json:"tiers"`
	Context   *int64           `json:"context_length"`
	MaxOutput *int64           `json:"max_output_tokens"`
	Streams   *bool            `json:"streams"`
	Abilities *[]string        `json:"abilities"`
	MaxUses   map[string]int64 `json:"max_uses"`
	// AlwaysSearches marks a model that searches the web on every call without a tool, such as Perplexity's sonar.
	// The gateway must not add a backend search to such a call: it would search twice.
	AlwaysSearches *bool `json:"always_searches"`

	OutputSpeed      *int64    `json:"output_speed"`
	ReasoningVisible *[]string `json:"reasoning_visible"`

	DialectKeys
}

// DialectKeys are the spec keys that say what a request to the model is written with (Dialect), each of which a spec may
// leave out: a nil key is absent, which is not false.
type DialectKeys struct {
	Thinking                  *string `json:"thinking,omitzero"`
	EncryptedReasoning        *bool   `json:"encrypted_reasoning,omitzero"`
	MidSystem                 *bool   `json:"mid_system,omitzero"`
	UntrustedSystemAfterTools *bool   `json:"untrusted_system_after_tools,omitzero"`
	DefaultSamplingOnly       *bool   `json:"default_sampling_only,omitzero"`
	ForcedToolChoice          *bool   `json:"forced_tool_choice,omitzero"`
	CacheBreakpoints          *bool   `json:"cache_breakpoints,omitzero"`
}

type spec struct {
	toolUses       map[Item]int64
	reportable     Reportable
	declared       bool // both lists are present, possibly empty
	serving        Serving
	serves         bool // every key Serving reads is present
	directServes   bool // context is present; direct-only formats need neither an output limit nor streaming
	abilities      bool // the abilities are listed, possibly empty
	dialect        Dialect
	speed          int64     // the fastest output, in tokens per second; 0 when the spec does not say
	reasoning      FormatSet // the call formats in which the model's stream shows reasoning progress
	alwaysSearches bool      // the model searches without a tool, so a plugin search would be a second one
}

// parseSpec checks a spec is a JSON object whose items, tiers and abilities, if present, name billing items, service
// tiers and abilities, whose lengths, if present, are positive, and whose stream data, if present, is usable.
func parseSpec(raw []byte) (spec, error) {
	var w specJSON
	if err := jsonx.Unmarshal(raw, &w); err != nil {
		return spec{}, fmt.Errorf("spec: %w", err)
	}
	reportable, err := parseReportable(w)
	if err != nil {
		return spec{}, err
	}
	serving, err := parseServing(w)
	if err != nil {
		return spec{}, err
	}
	toolUses, err := parseToolUses(w.MaxUses)
	if err != nil {
		return spec{}, err
	}
	dialect, err := parseDialect(w.DialectKeys)
	if err != nil {
		return spec{}, err
	}
	speed, reasoning, err := parseStream(w)
	if err != nil {
		return spec{}, err
	}
	return spec{
		toolUses:       toolUses,
		reportable:     reportable,
		serving:        serving,
		dialect:        dialect,
		declared:       w.Items != nil && w.Tiers != nil,
		serves:         w.Context != nil && w.MaxOutput != nil && w.Streams != nil && w.Abilities != nil,
		abilities:      w.Abilities != nil,
		speed:          speed,
		reasoning:      reasoning,
		alwaysSearches: w.AlwaysSearches != nil && *w.AlwaysSearches,
		directServes:   w.Context != nil,
	}, nil
}

// parseStream reads the model data a reservation made in segments needs: the fastest output, which sizes the overshoot
// allowance, and the call formats whose stream shows reasoning progress, which the gateway must see to stop the call
// within that allowance. Both are optional and independent: an absent speed or a format left out reserves the whole
// output, which is never short.
func parseStream(w specJSON) (int64, FormatSet, error) {
	var speed int64
	var visible FormatSet
	if w.OutputSpeed != nil {
		if speed = *w.OutputSpeed; speed <= 0 {
			return 0, 0, fmt.Errorf("spec output_speed must be above 0, not %d", speed)
		}
	}
	if w.ReasoningVisible != nil {
		formats, err := parseSet[CallFormat](formatNames[:], *w.ReasoningVisible)
		if err != nil {
			return 0, 0, fmt.Errorf("spec reasoning_visible: %w", err)
		}
		visible = formats
	}
	return speed, visible, nil
}

// parseToolUses accepts bounds only for server tools billing can quote. An absent bound stays absent.
func parseToolUses(w map[string]int64) (map[Item]int64, error) {
	var uses map[Item]int64
	if len(w) > 0 {
		uses = make(map[Item]int64, len(w))
	}
	for name, n := range w {
		it, err := ParseItem(name)
		if err != nil || (it != ItemWebSearch && it != ItemCodeExecution && it != ItemFileSearch) || n <= 0 || n > 1e10 {
			return nil, fmt.Errorf("spec max_uses: %s must name a supported server tool with an integer from 1 to 10000000000", name)
		}
		uses[it] = n
	}
	return uses, nil
}

// parseReportable reads the billing items and service tiers the spec declares, if it declares them.
func parseReportable(w specJSON) (Reportable, error) {
	var r Reportable
	var err error
	if w.Items != nil {
		if r.Items, err = parseSet[Item](itemNames[:], *w.Items); err != nil {
			return Reportable{}, fmt.Errorf("spec items: %w", err)
		}
	}
	if w.Tiers != nil {
		if r.ServiceTiers, err = parseSet[ServiceTier](serviceTierNames[:], *w.Tiers); err != nil {
			return Reportable{}, fmt.Errorf("spec tiers: %w", err)
		}
	}
	return r, nil
}

// parseServing reads what an endpoint of the model offers, refusing a length that is not positive and a name that is not
// an ability.
func parseServing(w specJSON) (Serving, error) {
	var s Serving
	if w.Context != nil {
		if s.Context = *w.Context; s.Context <= 0 {
			return Serving{}, fmt.Errorf("spec context_length must be above 0, not %d", s.Context)
		}
	}
	if w.MaxOutput != nil {
		if s.MaxOutput = *w.MaxOutput; s.MaxOutput <= 0 {
			return Serving{}, fmt.Errorf("spec max_output_tokens must be above 0, not %d", s.MaxOutput)
		}
	}
	if w.Streams != nil {
		s.Streams = *w.Streams
	}
	if w.Abilities != nil {
		abilities, err := parseSet[Ability](abilityNames[:], *w.Abilities)
		if err != nil {
			return Serving{}, fmt.Errorf("spec abilities: %w", err)
		}
		s.Abilities = abilities
	}
	return s, nil
}

// parseSavedSpec is parseSpec for a spec about to be saved, which must also be consistent: what is saved can be
// refused for more than what is already stored may be, or one bad row already stored would fail the snapshot.
func parseSavedSpec(raw []byte) (spec, error) {
	s, err := parseSpec(raw)
	if err != nil {
		return spec{}, err
	}
	reasons := s.serving.Abilities.Has(AbilityReasoning)
	switch {
	case !s.abilities:
	case reasons && s.dialect.Thinking == ThinkingNone:
		return spec{}, errors.New(`spec says the model reasons (abilities) and does not think (thinking "none")`)
	case !reasons && (s.dialect.Thinking == ThinkingAdaptive || s.dialect.Thinking == ThinkingManual):
		return spec{}, errors.New("spec says the model thinks (thinking) and does not list reasoning in its abilities")
	case !reasons && s.reasoning != 0:
		return spec{}, errors.New("spec says reasoning shows in a stream (reasoning_visible) and does not list reasoning in its abilities")
	case s.serving.Abilities.Has(AbilityFileSearch) && !s.reportable.Items.Has(ItemFileSearch):
		return spec{}, errors.New("spec lists the file_search ability but not the file_search item its calls are billed as")
	}
	return s, nil
}

// parseDialect reads how the spec says requests to the model are written. A key it leaves out, or sets to null, stays
// unset; a thinking mode it names must be one the catalog knows, as with the abilities.
func parseDialect(w DialectKeys) (Dialect, error) {
	d := Dialect{
		EncryptedReasoning: claimOf(w.EncryptedReasoning), MidSystem: claimOf(w.MidSystem),
		UntrustedSystemAfterTools: claimOf(w.UntrustedSystemAfterTools), DefaultSamplingOnly: claimOf(w.DefaultSamplingOnly),
		ForcedToolChoice: claimOf(w.ForcedToolChoice), CacheBreakpoints: claimOf(w.CacheBreakpoints),
	}
	if w.Thinking != nil {
		t, ok := thinkingByName[*w.Thinking]
		if !ok {
			return Dialect{}, fmt.Errorf("spec thinking: unknown name %q", *w.Thinking)
		}
		d.Thinking = t
	}
	return d, nil
}
