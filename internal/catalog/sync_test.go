package catalog_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// follow is a listed model that takes its prices from a dataset: the author, the model with the given names (the first
// a public standard ID, the rest public too, since a dated native name is public), and a first USD price.
func (s *store) follow(slug string, basis catalog.VersionBasis, source catalog.PriceSource, spec, schedule string, names ...string) catalog.ModelID {
	s.t.Helper()
	author, _, _ := strings.Cut(slug, "/")
	if err := s.exec(platformScope, "INSERT INTO catalog.author (slug, name) VALUES ($1, $1) ON CONFLICT DO NOTHING", author); err != nil {
		s.t.Fatal(err)
	}
	id := s.model(s.must(s.c.CreateModel(s.t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: slug, VersionBasis: basis, PriceSource: source, Spec: raw(spec),
	})))
	for _, n := range names {
		s.must(s.c.AddName(s.t.Context(), catalog.AddNameRequest{Op: op(), Model: id, Name: n, Visibility: catalog.VisibilityPublic}))
	}
	if schedule != "" {
		s.must(s.c.PublishPrice(s.t.Context(), catalog.PublishPriceRequest{
			Op: op(), Model: id, Source: source, Schedule: raw(schedule), Evidence: raw(evidence),
		}))
		s.list(id)
	}
	return id
}

// usd is the canonical USD schedule the model has now.
func (s *store) usd(id catalog.ModelID) string {
	s.t.Helper()
	stored := query[string](s, platformScope, `SELECT r.schedule::text FROM catalog.reference_price r WHERE r.model_id = $1 AND r.currency = 'USD'
		AND NOT EXISTS (SELECT FROM catalog.reference_price n WHERE n.supersedes = r.id)`, uuid.UUID(id))
	// jsonb does not keep the spelling, so the stored text is read back to its canonical form
	parsed, err := catalog.ParseSchedule([]byte(stored))
	if err != nil {
		s.t.Fatal(err)
	}
	canonical, err := parsed.MarshalJSON()
	if err != nil {
		s.t.Fatal(err)
	}
	return string(canonical)
}

func (s *store) priceCount(id catalog.ModelID) int {
	s.t.Helper()
	return query[int](s, platformScope, "SELECT count(*) FROM catalog.reference_price WHERE model_id = $1", uuid.UUID(id))
}

// litellm is a LiteLLM table of the given entries, each a key and the fields after its provider and mode.
func litellm(entries ...string) catalog.Fetched {
	var b strings.Builder
	b.WriteString("{")
	for i, e := range entries {
		key, fields, _ := strings.Cut(e, "=")
		key = strings.Trim(key, `"`)
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `%q:{"litellm_provider":"openai","mode":"chat",%s}`, key, fields)
	}
	b.WriteString("}")
	return catalog.Fetched{Source: catalog.SourceLiteLLM, Body: []byte(b.String()), At: time.Now()}
}

const (
	xSpec    = `{"items":["input","output","cache_read"],"tiers":[]}`
	xCurrent = `{"v":1,"currency":"USD","rates":{"cache_read":"0.0000001","input":"0.000001","output":"0.000002"}}`
	xEntry   = `"gpt-x-2025-01-01"=%s`
)

func xPrices(input, output, cache string) string {
	return fmt.Sprintf(`"input_cost_per_token":%s,"output_cost_per_token":%s,"cache_read_input_token_cost":%s`, input, output, cache)
}

// TestSyncPublishes: a price within the limit is published as the sync's, with the entry as evidence; the next round
// finds nothing to do.
func TestSyncPublishes(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	data := litellm(`"gpt-x-2025-01-01"=` + xPrices("1.1e-06", "2.2e-06", "1.1e-07"))

	report, err := s.c.SyncDataset(t.Context(), data, true)
	if err != nil || len(report.Results) != 1 || report.Results[0].Outcome != catalog.OutcomePublished {
		t.Fatalf("round: %+v, %v", report, err)
	}
	want := `{"currency":"USD","rates":{"cache_read":"0.00000011","input":"0.0000011","output":"0.0000022"},"v":1}`
	if got := s.usd(id); got != want {
		t.Errorf("schedule %s, want %s", got, want)
	}
	last := func(column, table string) string {
		return query[string](s, platformScope, "SELECT "+column+"::text FROM catalog."+table+" ORDER BY id DESC LIMIT 1")
	}
	if actor := last("actor", "change"); actor != "system:catalog.sync" {
		t.Errorf("actor %q", actor)
	}
	if ev := last("evidence", "change"); !strings.Contains(ev, `"entry": "gpt-x-2025-01-01"`) || !strings.Contains(ev, "body_sha256") {
		t.Errorf("evidence %s", ev)
	}
	if source := last("source", "reference_price"); source != "litellm" {
		t.Errorf("source %q", source)
	}
	report, err = s.c.SyncDataset(t.Context(), data, true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomeUnchanged || s.priceCount(id) != 2 {
		t.Errorf("second round: %+v, %v, %d prices", report, err, s.priceCount(id))
	}
}

// TestSyncDryRun: a round that does not publish says what it would do and makes no change.
func TestSyncDryRun(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	before := s.usd(id)
	report, err := s.c.SyncDataset(t.Context(), litellm(`"gpt-x-2025-01-01"=`+xPrices("1.1e-06", "2.2e-06", "1.1e-07")), false)
	if err != nil || len(report.Results) != 1 || report.Results[0].Outcome != catalog.OutcomeDiffers {
		t.Fatalf("dry run: %+v, %v", report, err)
	}
	if s.priceCount(id) != 1 || s.usd(id) != before {
		t.Error("a dry run published")
	}
}

// TestSyncHolds: a model is held, with the reason and the price it keeps, when the dataset cannot be trusted for it.
func TestSyncHolds(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		data   catalog.Fetched
		reason string
	}{
		"moves by more than the limit":   {litellm(`"gpt-x-2025-01-01"=` + xPrices("1.4e-06", "2e-06", "1e-07")), "moves from 0.000001 to 0.0000014"},
		"a cache read dearer than input": {litellm(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1.2e-06")), "cache read"},
		"a price per million, not token": {litellm(`"gpt-x-2025-01-01"=` + xPrices("1", "2", "0.1")), "a unit mistake"},
		"no entry":                       {litellm(`"gpt-y-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07")), "no entry"},
		"a negative price":               {litellm(`"gpt-x-2025-01-01"=` + xPrices("-1e-06", "2e-06", "1e-07")), "not a sign"},
		"too many decimals":              {litellm(`"gpt-x-2025-01-01"=` + xPrices("1e-16", "2e-06", "1e-07")), "15 decimals"},
		"an item the current price has":  {litellm(`"gpt-x-2025-01-01"="input_cost_per_token":1e-06,"output_cost_per_token":2e-06`), "lacks items [cache_read]"},
		"entries that disagree":          {litellm(`"gpt-x-2025-01-01"=`+xPrices("1e-06", "2e-06", "1e-07"), `"openai/gpt-x-2025-01-01"=`+xPrices("1.1e-06", "2e-06", "1e-07")), "different prices"},
		"tiers the current price lacks":  {litellm(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07") + `,"input_cost_per_token_above_200k_tokens":2e-06`), "tiers"},
		"a service tier out of range":    {litellm(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07") + `,"input_cost_per_token_priority":0.5`), "a unit mistake"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
			before := s.usd(id)
			tc.data.At = time.Now() // fetched after the model's price was published
			report, err := s.c.SyncDataset(t.Context(), tc.data, true)
			if err != nil {
				t.Fatal(err)
			}
			if r := report.Results[0]; r.Outcome != catalog.OutcomeHeld || !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("result %+v, want held for %q", r, tc.reason)
			}
			if s.usd(id) != before || s.priceCount(id) != 1 {
				t.Error("a held model's price changed")
			}
		})
	}
}

// TestSyncHoldKeepsTheCandidate: what is held for a person to approve is the schedule they would publish.
func TestSyncHoldKeepsTheCandidate(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	report, err := s.c.SyncDataset(t.Context(), litellm(`"gpt-x-2025-01-01"=`+xPrices("2e-06", "4e-06", "2e-07")), true)
	if err != nil {
		t.Fatal(err)
	}
	r := report.Results[0]
	if want := `{"currency":"USD","rates":{"cache_read":"0.0000002","input":"0.000002","output":"0.000004"},"v":1}`; string(r.Candidate) != want {
		t.Errorf("candidate %s, want %s", r.Candidate, want)
	}
	// a price published after the report makes its candidate stale: the report's base is refused, and the model's own is not what was reviewed
	id := r.Model
	publish := func() error {
		_, err := s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
			Op: op(), Model: id, Base: r.Base, Source: catalog.SourceLiteLLM, Schedule: r.Candidate, Evidence: raw(evidence),
		})
		return err
	}
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: id, Base: r.Base, Source: catalog.SourceLiteLLM, Schedule: raw(xCurrent), Evidence: raw(evidence),
	}))
	wantErr(t, publish(), "not the one the request was decided on")
}

// TestSyncOrdersRoundsByTheDatabaseClock: the node's clock runs 30 seconds fast, which must not make a dataset fetched
// before a price was published look newer than it.
func TestSyncOrdersRoundsByTheDatabaseClock(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.c.SetClock(func() time.Time { return time.Now().Add(30 * time.Second) })
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(litellm(`"gpt-x-2025-01-01"=` + xPrices("1.1e-06", "2.2e-06", "1.1e-07")).Body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	f := catalog.NewTestFetcher(egress.NewForTesting(egresstest.Loopback(t)), map[catalog.PriceSource]string{catalog.SourceLiteLLM: srv.URL})
	fetched, err := s.c.Fetch(t.Context(), f, catalog.SourceLiteLLM)
	if err != nil {
		t.Fatal(err)
	}
	// another round publishes after this one fetched
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: id, Base: new(s.head(id, "USD")), Source: catalog.SourceLiteLLM,
		Schedule: raw(`{"v":1,"currency":"USD","rates":{"cache_read":"0.0000001","input":"0.0000012","output":"0.000002"}}`), Evidence: raw(evidence),
	}))
	before := s.usd(id)
	report, err := s.c.SyncDataset(t.Context(), fetched, true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomeHeld || !strings.Contains(report.Results[0].Reason, "published after") {
		t.Fatalf("round: %+v, %v", report, err)
	}
	if s.usd(id) != before {
		t.Error("an older round replaced a newer price")
	}
}

// TestSyncHoldsADatasetOlderThanThePrice: a round that fetched before the current price was published cannot put its older
// price after the newer one.
func TestSyncHoldsADatasetOlderThanThePrice(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	before := s.usd(id)
	data := litellm(`"gpt-x-2025-01-01"=` + xPrices("1.1e-06", "2.2e-06", "1.1e-07"))
	data.At = time.Now().Add(-time.Hour)
	report, err := s.c.SyncDataset(t.Context(), data, true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomeHeld || !strings.Contains(report.Results[0].Reason, "published after") {
		t.Fatalf("round: %+v, %v", report, err)
	}
	if s.usd(id) != before {
		t.Error("an older dataset replaced a newer price")
	}
}

// TestSyncMatchesOnlyVersionEvidence: an undated name of a model fixed to one version is not what the dataset's entry of that
// name is about, since the dataset may have moved it to a newer version; a floating model's name is.
func TestSyncMatchesOnlyVersionEvidence(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	fixed := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x")
	floating := s.follow("openai/gpt-y", catalog.VersionFloating, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-y", "gpt-y")
	data := litellm(`"gpt-x"=`+xPrices("1.1e-06", "2e-06", "1e-07"), `"gpt-y"=`+xPrices("1.1e-06", "2e-06", "1e-07"))
	report, err := s.c.SyncDataset(t.Context(), data, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[catalog.ModelID]catalog.SyncResult{}
	for _, r := range report.Results {
		got[r.Model] = r
	}
	if r := got[fixed]; r.Outcome != catalog.OutcomeHeld || !strings.Contains(r.Reason, "not evidence of the version") {
		t.Errorf("fixed: %+v", r)
	}
	if r := got[floating]; r.Outcome != catalog.OutcomePublished {
		t.Errorf("floating: %+v", r)
	}
}

// TestSyncLeavesOtherSourcesAlone: a model whose prices are typed in, or come from the vendor's page, or from the other
// dataset, is not in a round, so no dataset changes it.
func TestSyncLeavesOtherSourcesAlone(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	data := litellm(`"gpt-x-2025-01-01"=` + xPrices("5e-06", "9e-06", "1e-07"))
	var ids []catalog.ModelID
	for i, source := range []catalog.PriceSource{catalog.SourceManual, catalog.SourceOfficial, catalog.SourceOpenRouter} {
		slug := fmt.Sprintf("openai/gpt-x%d@20250101", i)
		ids = append(ids, s.follow(slug, catalog.VersionDated, source, xSpec, xCurrent, fmt.Sprintf("openai/gpt-x%d", i), fmt.Sprintf("gpt-x%d-2025-01-01", i)))
	}
	followed := s.follow("openai/gpt-z@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-z", "gpt-x-2025-01-01")
	report, err := s.c.SyncDataset(t.Context(), data, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Model != followed {
		t.Fatalf("a round covers %+v, want only the model that follows the dataset", report.Results)
	}
	for _, id := range ids {
		if s.usd(id) != `{"currency":"USD","rates":{"cache_read":"0.0000001","input":"0.000001","output":"0.000002"},"v":1}` || s.priceCount(id) != 1 {
			t.Errorf("model %v changed", id)
		}
	}
}

// TestSyncTiers: the dataset gives where a tier starts and the current price says what counts toward it.
func TestSyncTiers(t *testing.T) {
	t.Parallel()
	const tiered = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"},
		"context_tiers":{"basis":["input","cache_read"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000002","output":"0.000004"}}]}}`
	const spec = `{"items":["input","output"],"tiers":[]}`
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, spec, tiered, "openai/gpt-x", "gpt-x-2025-01-01")
	const fields = `"input_cost_per_token":1.1e-06,"output_cost_per_token":2.2e-06,`
	report, err := s.c.SyncDataset(t.Context(), litellm(`"gpt-x-2025-01-01"=`+fields+`"input_cost_per_token_above_200k_tokens":2.2e-06,"output_cost_per_token_above_200k_tokens":4.4e-06`), true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomePublished {
		t.Fatalf("same threshold: %+v, %v", report, err)
	}
	if got := s.usd(id); !strings.Contains(got, `"basis":["input","cache_read"]`) || !strings.Contains(got, `"above":200000`) {
		t.Errorf("schedule %s", got)
	}
	report, err = s.c.SyncDataset(t.Context(), litellm(`"gpt-x-2025-01-01"=`+fields+`"input_cost_per_token_above_128k_tokens":2.2e-06`), true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomeHeld || !strings.Contains(report.Results[0].Reason, "context tiers") {
		t.Errorf("another threshold: %+v, %v", report, err)
	}
	report, err = s.c.SyncDataset(t.Context(), litellm(`"gpt-x-2025-01-01"=`+strings.TrimSuffix(fields, ",")), true)
	if err != nil || report.Results[0].Outcome != catalog.OutcomeHeld {
		t.Errorf("tiers dropped: %+v, %v", report, err)
	}
}

// TestSyncBadDatasets: a body that is not the dataset publishes nothing and is an error, not a round of held models.
func TestSyncBadDatasets(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id := s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	for _, body := range []string{``, `<html>`, `{}`, `{"a":{"litellm_provider":"x"}}`} {
		if _, err := s.c.SyncDataset(t.Context(), catalog.Fetched{Source: catalog.SourceLiteLLM, Body: []byte(body)}, true); err == nil {
			t.Errorf("body %q: no error", body)
		}
	}
	if _, err := s.c.SyncDataset(t.Context(), catalog.Fetched{Source: catalog.SourceOfficial, Body: []byte(`{}`)}, true); err == nil {
		t.Error("official is not a dataset")
	}
	if s.priceCount(id) != 1 {
		t.Error("a bad dataset changed a price")
	}
}

// TestSelectDatasetSource: the switch takes the dollar price from the entry, withdraws the other currencies, loses no
// price, and leaves the model in the rounds of that dataset.
func TestSelectDatasetSource(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, _ := s.seed("openai/gpt-x@20250101", xSpec, xCurrent)
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: id, Name: "gpt-x-2025-01-01", Visibility: catalog.VisibilityPublic}))
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: id, Source: catalog.SourceManual, Schedule: raw(`{"v":1,"currency":"CNY","rates":{"cache_read":"1","input":"7","output":"14"}}`), Evidence: raw(evidence),
	}))
	s.list(id)
	sel := func(data catalog.Fetched, mutate ...func(*catalog.SelectDatasetSourceRequest)) error {
		r := catalog.SelectDatasetSourceRequest{Op: op(), Model: id, Source: data.Source}
		for _, m := range mutate {
			m(&r)
		}
		_, err := s.c.SelectDatasetSource(ctx, data, r)
		return err
	}
	wantErr(t, sel(litellm(`"gpt-x-2025-01-01"=`+xPrices("1e-06", "2e-06", "1e-07")), func(r *catalog.SelectDatasetSourceRequest) { r.Source = catalog.SourceOpenRouter }), "the dataset is litellm")
	wantErr(t, sel(litellm(`"gpt-y-2025-01-01"=`+xPrices("1e-06", "2e-06", "1e-07"))), "no entry")
	wantErr(t, sel(litellm(`"gpt-x-2025-01-01"="input_cost_per_token":1e-06,"output_cost_per_token":2e-06`)), "lacks items [cache_read]")
	if got := query[string](s, platformScope, "SELECT price_source FROM catalog.model WHERE id = $1", uuid.UUID(id)); got != "manual" {
		t.Fatalf("a refused switch left the source at %q", got)
	}
	if err := sel(litellm(`"gpt-x-2025-01-01"=` + xPrices("1e-06", "2e-06", "1e-07"))); err != nil {
		t.Fatal(err)
	}
	if got := query[string](s, platformScope, "SELECT price_source FROM catalog.model WHERE id = $1", uuid.UUID(id)); got != "litellm" {
		t.Errorf("source %q", got)
	}
	if !query[bool](s, platformScope, "SELECT schedule IS NULL FROM catalog.reference_price WHERE id = $1", s.head(id, "CNY")) {
		t.Error("CNY is still priced")
	}
	// now the model follows the dataset, and a price that moved is the next round's
	report, err := s.c.SyncDataset(ctx, litellm(`"gpt-x-2025-01-01"=`+xPrices("1.1e-06", "2e-06", "1e-07")), true)
	if err != nil || len(report.Results) != 1 || report.Results[0].Outcome != catalog.OutcomePublished {
		t.Errorf("round after the switch: %+v, %v", report, err)
	}
}

// TestSelectDatasetSourceKeepsTiers: a dataset price without the context tiers the current price has is refused, though
// its base price covers every range.
func TestSelectDatasetSourceKeepsTiers(t *testing.T) {
	t.Parallel()
	const tiered = `{"v":1,"currency":"USD","rates":{"cache_read":"0.0000001","input":"0.000001","output":"0.000002"},
		"context_tiers":{"basis":["input","cache_read"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000002","output":"0.000004"}}]}}`
	s := newStore(t)
	id, _ := s.seed("openai/gpt-x@20250101", xSpec, tiered)
	s.must(s.c.AddName(t.Context(), catalog.AddNameRequest{Op: op(), Model: id, Name: "gpt-x-2025-01-01", Visibility: catalog.VisibilityPublic}))
	before := s.priceCount(id)
	_, err := s.c.SelectDatasetSource(t.Context(), litellm(`"gpt-x-2025-01-01"=`+xPrices("1e-06", "2e-06", "1e-07")),
		catalog.SelectDatasetSourceRequest{Op: op(), Model: id, Source: catalog.SourceLiteLLM})
	wantErr(t, err, "lacks the context tiers")
	if s.priceCount(id) != before || query[string](s, platformScope, "SELECT price_source FROM catalog.model WHERE id = $1", uuid.UUID(id)) != "manual" {
		t.Error("a refused switch changed the model")
	}
}

// TestSelectPriceSourceTakesNoDatasetPrices: the operator's own prices are never filed under a dataset.
func TestSelectPriceSourceTakesNoDatasetPrices(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("openai/gpt-x@20250101", xSpec, xCurrent)
	for _, source := range []catalog.PriceSource{catalog.SourceLiteLLM, catalog.SourceOpenRouter} {
		_, err := s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
			Op: op(), Model: id, Rev: 1, Source: source,
			Bases: s.bases(t.Context(), id), Prices: []catalog.SourcePrice{{Schedule: raw(xCurrent), Evidence: raw(evidence)}},
		})
		wantErr(t, err, "cannot be selected yet")
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceLiteLLM, xSpec, xCurrent, "openai/gpt-x", "gpt-x-2025-01-01")
	s.follow("other/model", catalog.VersionDated, catalog.SourceManual, xSpec, xCurrent, "other/model")
	// a draft with no price, and a listed one whose price lacks the cache read its spec declares cannot both be told apart
	// from a good one by anything but the check
	draft := s.model(s.must(s.c.CreateModel(t.Context(), catalog.CreateModelRequest{
		Op: op(), Slug: "openai/draft", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(`{"items":["input"],"tiers":[]}`),
	})))
	checks, err := s.c.Check(t.Context(), []string{"openai"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks %+v", checks)
	}
	byName := map[string]catalog.ModelCheck{}
	for _, c := range checks {
		byName[c.Slug] = c
	}
	if c := byName["openai/gpt-x@20250101"]; len(c.Problems) != 0 || c.Source != catalog.SourceLiteLLM || len(c.PublicNames) != 2 || fmt.Sprint(c.Currencies) != "[USD]" {
		t.Errorf("good model: %+v", c)
	}
	c := byName["openai/draft"]
	for _, want := range []string{"not listed", "no public name", "no current price"} {
		if !strings.Contains(strings.Join(c.Problems, "; "), want) {
			t.Errorf("draft %v: no problem %q", draft, want)
		}
	}
}

// TestUnrecognized: the text models of the vendors that OpenRouter lists and no name of the catalog stands for.
func TestUnrecognized(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.follow("openai/gpt-x@20250101", catalog.VersionDated, catalog.SourceManual, xSpec, xCurrent, "openai/gpt-x")
	s.follow("openai/gpt-y@20990101", catalog.VersionDated, catalog.SourceManual, xSpec, xCurrent, "openai/gpt-y", "openai/gpt-y-20990101")
	model := func(id, output string) string {
		return fmt.Sprintf(`{"id":%q,"canonical_slug":%q,"architecture":{"output_modalities":[%q]},"pricing":{"prompt":"0.000001","completion":"0.000002"}}`, id, id+"-20990101", output)
	}
	entries, err := catalog.ParseOpenRouter([]byte(`{"data":[` + strings.Join([]string{
		model("openai/gpt-x", "text"),      // known only by an undated name, which says nothing of the version (its dated one is new)
		model("openai/gpt-y", "text"),      // known by the dated name
		model("openai/gpt-new", "text"),    // not known
		model("openai/gpt-image", "image"), // not a text model
		model("other/model", "text"),       // not a checked vendor
	}, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := s.c.Unrecognized(entries, []string{"openai"})
	if err != nil || fmt.Sprint(got) != "[openai/gpt-new openai/gpt-x]" {
		t.Errorf("Unrecognized = %v, %v", got, err)
	}
}
