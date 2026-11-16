package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Outcome is what a sync round did for one model.
type Outcome string

// The outcomes. A held model keeps its price; the round says why and what it would have published, and the next round
// works it out again, so nothing is lost by holding (README.md「参考价的录入」).
const (
	OutcomePublished Outcome = "published"
	OutcomeUnchanged Outcome = "unchanged"
	OutcomeDiffers   Outcome = "differs" // a dry run only: this would be published
	OutcomeHeld      Outcome = "held"
)

// SyncResult is one model's outcome. Candidate is the schedule the dataset gives when it is a valid one that was not
// published, for a person to publish with publish-price.
type SyncResult struct {
	Model ModelID `json:"model"`
	Slug  string  `json:"slug"`
	Entry string  `json:"entry,omitempty"`
	// Base is the current price row the result was worked out against, nil when the model has no history: a person
	// publishes a candidate with it, so a price published since the round is refused, not overwritten.
	Base      *uuid.UUID `json:"base"`
	Outcome   Outcome    `json:"outcome"`
	Reason    string     `json:"reason,omitempty"`
	Candidate jsonx.Raw  `json:"candidate,omitempty"`
}

// SyncReport is a round over one dataset: one result per model that takes its price from it.
type SyncReport struct {
	Source  PriceSource `json:"source"`
	Results []SyncResult
}

// Fetched is a dataset as fetched.
type Fetched struct {
	Source PriceSource
	Body   []byte
	At     time.Time
}

// syncModel is a model that follows a dataset, with what the sync needs to know of it.
type syncModel struct {
	row   CatalogModel
	names []CatalogName
	head  price // the USD price's head; zero when there is none
	has   bool
	heads prices // every currency's
}

var syncActor = audit.System("catalog.sync")

// SyncDataset runs one round over a fetched dataset: every draft or listed model whose price source is the dataset is
// matched to the dataset's entry by name, and when the entry's schedule differs from the current one and is safe, the
// round publishes it as system:sync. A round with publish false changes nothing and reports what it would do.
//
// A body that does not parse, or has no entry of the vendors we follow, is an error and publishes nothing: models keep
// their last prices and never change source. A model is held, not published, when its entry is missing, ambiguous or
// has no usable price; when its price does not pass the sanity checks or moves by more than PriceChangeLimit; when
// the tiers' structure differs from the current price's; or when the publish refuses it (a lost price, an incomplete
// schedule). Models that follow manual or official are never in a round: PublishPrice also compares the source on the
// locked model row, so a source switch during the round wins.
func (c *Catalog) SyncDataset(ctx context.Context, f Fetched, publish bool) (SyncReport, error) {
	entries, err := parseDataset(f.Source, f.Body)
	if err != nil {
		return SyncReport{}, err
	}
	models, err := c.syncModels(ctx, f.Source)
	if err != nil {
		return SyncReport{}, err
	}
	index := indexEntries(entries)
	sum := sha256.Sum256(f.Body)
	report := SyncReport{Source: f.Source}
	for _, m := range models {
		r, err := c.syncOne(ctx, f, hex.EncodeToString(sum[:]), index, m, publish)
		if err != nil {
			return report, err
		}
		if r.Outcome == OutcomeHeld {
			c.cfg.Logger.Warn("catalog sync held a model", "dataset", f.Source, "model", r.Slug, "entry", r.Entry, "reason", r.Reason)
		}
		report.Results = append(report.Results, r)
	}
	return report, nil
}

func parseDataset(src PriceSource, body []byte) ([]Entry, error) {
	switch src {
	case SourceLiteLLM:
		return ParseLiteLLM(body)
	case SourceOpenRouter:
		return ParseOpenRouter(body)
	case SourceOfficial, SourceManual:
	}
	return nil, fmt.Errorf("%w: %s is not a dataset", ErrSourceUnsupported, src)
}

// indexEntries finds entries by the normalized form of their names.
func indexEntries(entries []Entry) map[string][]*Entry {
	index := map[string][]*Entry{}
	for i := range entries {
		for _, name := range entries[i].Names {
			norm := Normalize(name)
			if !slices.Contains(index[norm], &entries[i]) {
				index[norm] = append(index[norm], &entries[i])
			}
		}
	}
	return index
}

func (c *Catalog) syncModels(ctx context.Context, src PriceSource) ([]syncModel, error) {
	var models []syncModel
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) error {
		rows, err := New().ListModelsForSync(ctx, tx, string(src))
		if err != nil {
			return err
		}
		for _, row := range rows {
			m, err := followerOf(ctx, tx, row)
			if err != nil {
				return err
			}
			models = append(models, m)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catalog sync: read the models: %w", err)
	}
	return models, nil
}

// followerOf reads what the sync needs of a model: its names and its dollar price.
func followerOf(ctx context.Context, tx pg.Tx, row CatalogModel) (syncModel, error) {
	names, err := New().ListNamesOfModel(ctx, tx, row.ID)
	if err != nil {
		return syncModel{}, err
	}
	heads, err := currentPrices(ctx, tx, row.ID)
	if err != nil {
		return syncModel{}, err
	}
	head, has := heads[datasetCurrency]
	return syncModel{row: row, names: names, head: head, has: has, heads: heads}, nil
}

// syncOne decides one model. An error is the database failing, which ends the round; everything about the data is a
// held result.
func (c *Catalog) syncOne(ctx context.Context, f Fetched, bodySum string, index map[string][]*Entry, m syncModel, publish bool) (SyncResult, error) {
	r := SyncResult{Model: ModelID(m.row.ID), Slug: m.row.Slug, Outcome: OutcomeHeld}
	if m.has {
		r.Base = &m.head.id
	}
	entry, err := pickEntry(index, m)
	if err != nil {
		return r.heldBy(err, nil), nil
	}
	r.Entry = entry.Key
	var current *Schedule
	if m.has {
		current = m.head.schedule
	}
	next, err := entry.schedule(current)
	if err != nil {
		return r.heldBy(err, nil), nil
	}
	n, err := parsePrice(mustJSON(next))
	if err != nil {
		return r, err
	}
	if current != nil && bytes.Equal(n.canonical, mustJSON(*current)) {
		r.Outcome = OutcomeUnchanged
		return r, nil
	}
	spec, err := parseSpec(m.row.Spec)
	if err != nil {
		return r, err
	}
	// The publish makes these two checks again on the locked row; making them here as well gives a dry run the answer.
	if err := errors.Join(outdated(m, f), c.safe(current, next), c.AllowSchedule(next), checkPublish(m.row, spec, n, m.head, false)); err != nil {
		return r.heldBy(err, n.canonical), nil
	}
	if !publish {
		r.Outcome = OutcomeDiffers
		return r, nil
	}
	return c.publishSynced(ctx, f, bodySum, entry, m, n, r)
}

func (c *Catalog) publishSynced(ctx context.Context, f Fetched, bodySum string, e *Entry, m syncModel, n newPrice, r SyncResult) (SyncResult, error) {
	req := PublishPriceRequest{
		Op:    syncOp(f.Source, e.Key),
		Model: r.Model, Source: f.Source, Schedule: n.canonical, Evidence: datasetEvidence(f, bodySum, e),
	}
	if m.has {
		req.Base = &m.head.id
	}
	_, err := c.PublishPrice(ctx, req)
	switch {
	case err == nil:
		r.Outcome = OutcomePublished
	case errors.Is(err, ErrPriceChanged), errors.Is(err, ErrSourceMismatch), errors.Is(err, ErrPriceLost),
		errors.Is(err, ErrIncomplete), errors.Is(err, ErrTierBasisChanged), errors.Is(err, ErrFileParse):
		return r.heldBy(err, n.canonical), nil
	default:
		return r, fmt.Errorf("catalog sync: publish %s: %w", m.row.Slug, err)
	}
	return r, nil
}

// syncOp is the operation of one publish: a fresh key, since the base it carries already stops a retry from publishing twice.
func syncOp(src PriceSource, entry string) Op {
	return Op{Actor: syncActor, Key: uuid.NewV7(), Reason: fmt.Sprintf("dataset sync: %s %s", src, entry)}
}

// outdated refuses a round that fetched before the current price was published: it would put an older price after a
// newer one, and the base and the limit would not notice (two rounds fetching at different times, finishing in the other order).
func outdated(m syncModel, f Fetched) error {
	if m.has && m.head.at.After(f.At) {
		return errors.New("the current price was published after this dataset was fetched")
	}
	return nil
}

// heldBy is the result with why it was held and, if the dataset gave a valid schedule, the one a person could publish.
func (r SyncResult) heldBy(why error, candidate jsonx.Raw) SyncResult {
	r.Outcome, r.Reason, r.Candidate = OutcomeHeld, why.Error(), candidate
	return r
}

// datasetEvidence is what a published price rests on: the entry as the dataset gave it, and which fetch it was in.
func datasetEvidence(f Fetched, bodySum string, e *Entry) jsonx.Raw {
	return mustJSON(map[string]any{
		"dataset": f.Source, "entry": e.Key, "fetched_at": f.At.UTC().Format(time.RFC3339), "body_sha256": bodySum, "raw": e.Raw,
	})
}

// mustJSON is the canonical form of a schedule or a JSON value built here, which cannot fail to encode.
func mustJSON(v any) jsonx.Raw {
	b, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(fmt.Sprintf("catalog: encode %T: %v", v, err))
	}
	return b
}

// pickEntry finds the one entry a model follows. Only a name that is evidence of the version matches: a model fixed to a
// version does not follow an undated name, which the dataset may already move to a newer version (README.md「名字解析」).
// Entries that give different schedules are ambiguous, and so is one with no usable price among several.
func pickEntry(index map[string][]*Entry, m syncModel) (*Entry, error) {
	var found []*Entry
	var weak []string
	for _, n := range m.names {
		hits := index[n.Norm]
		switch {
		case len(hits) == 0:
		case VersionBasis(m.row.VersionBasis) == VersionFloating || hasDate(n.Norm):
			for _, e := range hits {
				if !slices.Contains(found, e) {
					found = append(found, e)
				}
			}
		default:
			weak = append(weak, n.Name)
		}
	}
	switch {
	case len(found) == 0 && len(weak) > 0:
		return nil, fmt.Errorf("the dataset knows it only by %s, which is not evidence of the version", strings.Join(weak, ", "))
	case len(found) == 0:
		return nil, errors.New("the dataset has no entry for it")
	}
	return oneOf(found)
}

// oneOf picks the entry that says everything the others say: LiteLLM keeps some models under two keys, one of which
// also prices an item the other leaves out (a cache write at zero). Entries that differ in any price, or of which only
// some are usable, are ambiguous. Fields are never taken from different entries, so the schedule is one a dataset gave.
func oneOf(found []*Entry) (*Entry, error) {
	best := found[0]
	for _, e := range found[1:] {
		if e.sheet.covers(best.sheet) {
			best = e
		}
	}
	for _, e := range found {
		if (e.Err == nil) != (best.Err == nil) || best.Err == nil && !best.sheet.covers(e.sheet) {
			keys := make([]string, len(found))
			for i, e := range found {
				keys[i] = e.Key
			}
			return nil, errors.New("the dataset has several entries with different prices for it: " + strings.Join(keys, ", "))
		}
	}
	return best, nil
}

// maxTokenPrice is $1000 per million tokens, past 100 times what the dearest models cost; a price above it is a unit
// mistake (per million for per token). maxUsePrice is the same for anything not counted in tokens: a search or a call,
// and a piece, second or character, each of which is priced per thing rather than per million.
var maxTokenPrice, maxUsePrice = mustPrice("0.001"), mustPrice("1")

func mustPrice(s string) platform.UnitPrice {
	p, err := platform.ParseUnitPrice(s)
	if err != nil {
		panic(err)
	}
	return p
}

// safe is the reference-price safety valve in the catalog (docs/domains/billing.md「定价」): the prices are in a
// sane range and order, the tiers have the structure the current price has, and no item moves by more than the limit.
func (c *Catalog) safe(current *Schedule, next Schedule) error {
	if err := sane(next); err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if current.basis != next.basis || !slices.Equal(current.above, next.above) {
		return errors.New("the context tiers differ from the current price's")
	}
	return moved(*current, next, c.cfg.PriceChangeLimit)
}

// sane refuses a price out of its unit's range, and a cache read that costs more than the input it replaces.
func sane(s Schedule) error {
	for st := range numServiceTiers {
		for band := 0; band <= len(s.above); band++ {
			if err := saneBand(s, st, band); err != nil {
				return fmt.Errorf("%s above %d: %w", st, bandStart(s, band), err)
			}
		}
	}
	return nil
}

func saneBand(s Schedule, st ServiceTier, band int) error {
	for it := range NumItems {
		limit := maxTokenPrice
		if it.Unit() != UnitToken {
			limit = maxUsePrice
		}
		if p, ok := s.price(st, band, it); ok && p.Cmp(limit) > 0 {
			return fmt.Errorf("%s costs %s, past %s: a unit mistake", it, p, limit)
		}
	}
	in, hasIn := s.price(st, band, ItemInput)
	cache, hasCache := s.price(st, band, ItemCacheRead)
	if hasIn && hasCache && cache.Cmp(in) > 0 {
		return fmt.Errorf("a cache read (%s) costs more than the input (%s)", cache, in)
	}
	return nil
}

func bandStart(s Schedule, band int) int64 {
	if band == 0 {
		return 0
	}
	return s.above[band-1]
}

// moved refuses a price that differs from the current one by more than limit percent, for every item both have.
func moved(current, next Schedule, limit int) error {
	for st := range numServiceTiers {
		for band := 0; band <= len(next.above); band++ {
			for it := range NumItems {
				old, hadOld := current.price(st, band, it)
				now, hasNow := next.price(st, band, it)
				if hadOld && hasNow && changedBy(old, now, limit) {
					return fmt.Errorf("%s %s above %d moves from %s to %s, by more than %d%%", st, it, bandStart(next, band), old, now, limit)
				}
			}
		}
	}
	return nil
}

// changedBy reports whether |now - old| > old × limit / 100, exactly.
func changedBy(old, now platform.UnitPrice, limit int) bool {
	o, _ := new(big.Rat).SetString(old.String())
	n, _ := new(big.Rat).SetString(now.String())
	diff := new(big.Rat).Sub(n, o)
	allowed := new(big.Rat).Mul(o, big.NewRat(int64(limit), 100))
	return diff.Abs(diff).Cmp(allowed) > 0
}
