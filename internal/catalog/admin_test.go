package catalog_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
)

// TestListingGate: a model is listed only when it declares what the upstream may report, has its standard ID as a
// public name and a reference price, and every current price covers everything it declares.
func TestListingGate(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	cases := []struct {
		name, slug, spec, schedule string
		want                       string
	}{
		{"complete", "acme/complete", basicSpec, basic, ""},
		{"undeclared", "acme/undeclared", `{"context_length":1}`, basic, `must declare "items" and "tiers"`},
		{"an item without a price", "acme/audio", `{"items":["input","output","audio_in"],"tiers":[]}`, basic, "lacks items [audio_in]"},
		{"a service tier without a price", "acme/prio", priorityEnv, basic, "service tiers [priority]"},
		{"a service tier with prices", "acme/prio-ok", priorityEnv, withPriority, ""},
	}
	for _, c := range cases {
		id, _ := s.seed(c.slug, c.spec, c.schedule)
		_, err := s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: id, Rev: 0})
		if c.want == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if c.want != "" {
			wantErr(t, err, c.want)
			if !errors.Is(err, catalog.ErrNotReady) {
				t.Errorf("%s: %v is not ErrNotReady", c.name, err)
			}
		}
	}
	// No standard ID, and no price.
	s.must(s.c.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: "bare", Name: "Bare"}))
	bare := s.model(s.must(s.c.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: "bare/model", VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	_, err := s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: bare, Rev: 0})
	wantErr(t, err, `no public name for its standard ID "bare/model"`)
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: bare, Name: "bare/model", Visibility: catalog.VisibilityInternal}))
	_, err = s.c.ListModel(ctx, catalog.ModelRequest{Op: op(), Model: bare, Rev: 0})
	wantErr(t, err, "no public name")
}

// TestPriceHistory: a version is published on the head it was decided on; a second publish on the same base is
// refused, as is one from another source, one that loses a price, and one that the listing gate refuses.
func TestPriceHistory(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, first := s.seed("acme/alpha", priorityEnv, withPriority)
	s.list(id)
	publish := func(schedule string, base *uuid.UUID, mutate ...func(*catalog.PublishPriceRequest)) error {
		r := catalog.PublishPriceRequest{Op: op(), Model: id, Base: base, Source: catalog.SourceManual, Schedule: raw(schedule), Evidence: raw(evidence)}
		for _, m := range mutate {
			m(&r)
		}
		_, err := s.c.PublishPrice(ctx, r)
		return err
	}
	cheaper := `{"v":1,"currency":"USD","rates":{"input":"0.0000005","output":"0.000002"},
		"service_tiers":{"priority":{"rates":{"input":"0.000002","output":"0.000004"}}}}`
	if err := publish(cheaper, &first); err != nil {
		t.Fatal(err)
	}
	second := s.head(id, "USD")
	wantErr(t, publish(withPriority, &first), "not the one the request was decided on")
	wantErr(t, publish(withPriority, nil), "not the one the request was decided on")
	wantErr(t, publish(withPriority, &second, func(r *catalog.PublishPriceRequest) { r.Source = catalog.SourceOfficial }), "price source")
	wantErr(t, publish(basic, &second), "service tiers [priority]") // the listing gate: priority is declared
	wantErr(t, publish(withPriority, &second, func(r *catalog.PublishPriceRequest) { r.Evidence = nil }), "evidence")
	lossy := `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},"service_tiers":{"priority":{"rates":{"output":"4"}}}}`
	wantErr(t, publish(lossy, &second), "service tiers [priority]") // a listed model is held to its declaration
	if got := s.head(id, "USD"); got != second {
		t.Errorf("a refused publish moved the head to %v", got)
	}
	// The history is a chain: first <- second.
	if got := query[uuid.UUID](s, platformScope, "SELECT supersedes FROM catalog.reference_price WHERE id = $1", second); got != first {
		t.Errorf("second supersedes %v, want %v", got, first)
	}
}

// TestPriceLoss: a draft model is not held to the listing gate, but a new schedule still may not lose a price the
// current one has, whether it is published as a version or by switching the source; nothing changes when it is refused.
func TestPriceLoss(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, base := s.seed("acme/alpha", priorityEnv, withPriority)
	lossy := `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},"service_tiers":{"priority":{"rates":{"output":"4"}}}}`
	_, err := s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: id, Base: &base, Source: catalog.SourceManual, Schedule: raw(lossy), Evidence: raw(evidence),
	})
	wantErr(t, err, "would lose", "priority input above 0")
	if !errors.Is(err, catalog.ErrPriceLost) {
		t.Errorf("%v is not ErrPriceLost", err)
	}
	_, err = s.c.SelectPriceSource(ctx, catalog.SelectPriceSourceRequest{
		Op: op(), Model: id, Rev: 0, Source: catalog.SourceOfficial, Bases: s.bases(ctx, id),
		Prices: []catalog.SourcePrice{{Schedule: raw(lossy), Evidence: raw(evidence)}},
	})
	wantErr(t, err, "would lose")
	if got := query[string](s, platformScope, "SELECT price_source FROM catalog.model WHERE id = $1", uuid.UUID(id)); got != "manual" {
		t.Errorf("a refused switch left the source at %q", got)
	}
	if got := s.head(id, "USD"); got != base {
		t.Errorf("a refused operation moved the head to %v", got)
	}
}

// TestPriceBasisChange: thresholds that count other items cannot be compared, so the publish needs the operator's
// confirmation.
func TestPriceBasisChange(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	tiered := func(basis string) string {
		return `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},"context_tiers":{"basis":[` + basis +
			`],"method":"whole","tiers":[{"above":100,"rates":{"input":"2","output":"4"}}]}}`
	}
	id, base := s.seed("acme/tiered", basicSpec, tiered(`"input"`))
	r := catalog.PublishPriceRequest{
		Op: op(), Model: id, Base: &base, Source: catalog.SourceManual, Schedule: raw(tiered(`"input","cache_read"`)), Evidence: raw(evidence),
	}
	_, err := s.c.PublishPrice(ctx, r)
	if !errors.Is(err, catalog.ErrTierBasisChanged) {
		t.Fatalf("got %v, want ErrTierBasisChanged", err)
	}
	r.Op, r.ConfirmBasisChange = op(), true
	s.must(s.c.PublishPrice(ctx, r))
}

// TestSelectPriceSource: the switch keeps every price: the currencies the new source has get a new version with the
// new source, the ones it lacks are withdrawn, and a schedule that loses a price is refused with nothing changed.
func TestSelectPriceSource(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, usd := s.seed("acme/alpha", priorityEnv, withPriority)
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: id, Source: catalog.SourceManual, Schedule: raw(`{"v":1,"currency":"CNY","rates":{"input":"7","output":"14"},
			"service_tiers":{"priority":{"rates":{"input":"8","output":"16"}}}}`), Evidence: raw(evidence),
	}))
	s.list(id)
	rev := func() int64 {
		return query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(id))
	}
	selectOfficial := func(schedule string, mutate ...func(*catalog.SelectPriceSourceRequest)) error {
		r := catalog.SelectPriceSourceRequest{
			Op: op(), Model: id, Rev: rev(), Source: catalog.SourceOfficial, Bases: s.bases(ctx, id),
			Prices: []catalog.SourcePrice{{Schedule: raw(schedule), Evidence: raw(evidence)}},
		}
		for _, m := range mutate {
			m(&r)
		}
		_, err := s.c.SelectPriceSource(ctx, r)
		return err
	}
	lossy := `{"v":1,"currency":"USD","rates":{"input":"1","output":"2"},"service_tiers":{"priority":{"rates":{"output":"4"}}}}`
	wantErr(t, selectOfficial(lossy), "service tiers [priority]")
	if got := query[string](s, platformScope, "SELECT price_source FROM catalog.model WHERE id = $1", uuid.UUID(id)); got != "manual" {
		t.Errorf("a refused switch left the source at %q", got)
	}
	wantErr(t, selectOfficial(basicCNY, func(r *catalog.SelectPriceSourceRequest) { r.Source = catalog.SourceLiteLLM }), "cannot be selected yet")
	wantErr(t, selectOfficial(basic, func(r *catalog.SelectPriceSourceRequest) { r.Rev = 99 }), "changed since it was reviewed")
	wantErr(t, selectOfficial(basic), "service tiers [priority]") // listed: the gate holds for the new schedule too

	if err := selectOfficial(withPriority); err != nil {
		t.Fatal(err)
	}
	newUSD := s.head(id, "USD")
	if newUSD == usd {
		t.Error("an identical schedule from the new source was not published again")
	}
	// CNY, which the new source lacks, is withdrawn: the head has no schedule and the snapshot has no CNY price.
	if got := query[bool](s, platformScope, "SELECT schedule IS NULL FROM catalog.reference_price WHERE id = $1", s.head(id, "CNY")); !got {
		t.Error("CNY still has a price after switching to a source without it")
	}
	snap := s.current()
	if _, ok := snap.ReferencePrice(id, "CNY"); ok {
		t.Error("the snapshot still prices CNY")
	}
	if p, ok := snap.ReferencePrice(id, "USD"); !ok || p.ID != newUSD || p.Source != catalog.SourceOfficial {
		t.Errorf("the snapshot's USD price is %+v, %v", p, ok)
	}
}

// TestOperationRetry: an operation whose response was lost can be sent again with its key and gets the first result;
// the key cannot stand for another kind of operation.
func TestOperationRetry(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	r := catalog.UpdateSpecRequest{Op: op(), Model: id, Rev: 0, Spec: raw(`{"items":["input"],"tiers":[],"note":"first"}`)}
	first := s.must(s.c.UpdateSpec(ctx, r))
	again := s.must(s.c.UpdateSpec(ctx, r)) // the rev is stale now, and the retry still succeeds
	if !again.Replayed || again.Change != first.Change {
		t.Errorf("the retry got %+v, first was %+v", again, first)
	}
	if n := query[int](s, platformScope, "SELECT count(*) FROM catalog.change WHERE op_key = $1", r.Key); n != 1 {
		t.Errorf("%d changes for one key", n)
	}
	_, err := s.c.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: r.Op, Slug: "acme", Name: "x"})
	if !errors.Is(err, catalog.ErrOpKeyReused) {
		t.Errorf("the key of another kind of operation: %v", err)
	}
}

// TestConcurrentEdits: two edits decided on the same rev, in parallel: one wins and the other is refused, not
// silently overwritten.
func TestConcurrentEdits(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	errs := make(chan error, 2)
	for _, note := range []string{"a", "b"} {
		go func() {
			_, err := s.c.UpdateSpec(s.t.Context(), catalog.UpdateSpecRequest{
				Op: op(), Model: id, Rev: 0, Spec: raw(`{"items":["input"],"tiers":[],"note":"` + note + `"}`),
			})
			errs <- err
		}()
	}
	failed := 0
	for range 2 {
		if err := <-errs; errors.Is(err, catalog.ErrStaleRev) {
			failed++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if failed != 1 {
		t.Errorf("%d of two edits on one rev were refused, want 1", failed)
	}
}

// TestHistoryAndAudit: every operation leaves a change with its evidence in the platform data and an audit entry that
// commits with it; a refused operation leaves neither.
func TestHistoryAndAudit(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	changes := query[int](s, platformScope, "SELECT count(*) FROM catalog.change")
	audits := query[int](s, platformScope, "SELECT count(*) FROM platform.audit_log WHERE module = 'catalog'")
	if changes != audits || changes != 4 { // author, model, name and price
		t.Errorf("%d changes and %d audit entries after the seed", changes, audits)
	}
	if got := query[string](s, platformScope, "SELECT evidence ->> 'url' FROM catalog.change WHERE kind = 'price.publish'"); got != "https://example.com/pricing" {
		t.Errorf("the price change's evidence url is %q", got)
	}
	if got := query[string](s, platformScope, "SELECT actor FROM catalog.change LIMIT 1"); got != "system:catalog.test" {
		t.Errorf("actor %q", got)
	}
	_, err := s.c.Retire(s.t.Context(), catalog.ModelRequest{Op: op(), Model: id, Rev: 0}) // a draft cannot be retired
	wantErr(t, err, "not listed")
	if n := query[int](s, platformScope, "SELECT count(*) FROM catalog.change"); n != changes {
		t.Errorf("a refused operation left %d changes", n-changes)
	}
	_, err = s.c.SaveAuthor(s.t.Context(), catalog.SaveAuthorRequest{Actor: op().Actor, Key: uuid.NewV7(), Slug: "x", Name: "X"})
	wantErr(t, err, "reason")
}

// TestNamesAreUnique: normalized, a name belongs to one model, whichever spelling and whichever visibility.
func TestNamesAreUnique(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, _ := s.seed("acme/claude-4.5", basicSpec, basic)
	other, _ := s.seed("acme/other", basicSpec, basic)
	for _, name := range []string{"ACME/Claude-4.5", "acme/claude-4-5", " acme/claude_4.5 "} {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: other, Name: name, Visibility: catalog.VisibilityInternal})
		wantErr(t, err, "already taken")
	}
	// The database refuses a public name that cannot be called, whatever the program computed.
	for _, name := range []string{"acme/model-latest", "acme/model@1"} {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: id, Name: name, Visibility: catalog.VisibilityPublic})
		wantErr(t, err, "public_name")
	}
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: id, Name: "acme/model-latest", Visibility: catalog.VisibilityInternal}))
}

// TestRevisionOnlyForWhatASnapshotHolds: editing a draft does not move the revision; listing and retiring do.
func TestRevisionOnlyForWhatASnapshotHolds(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	revision := func() int64 { return query[int64](s, platformScope, "SELECT n FROM catalog.revision") }
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	if revision() != 0 {
		t.Errorf("revision %d after drafting", revision())
	}
	s.list(id)
	if revision() != 1 {
		t.Errorf("revision %d after listing", revision())
	}
	s.must(s.c.Retire(s.t.Context(), catalog.ModelRequest{Op: op(), Model: id, Rev: 1}))
	if revision() != 2 {
		t.Errorf("revision %d after retiring", revision())
	}
	_, err := s.c.UpdateSpec(s.t.Context(), catalog.UpdateSpecRequest{Op: op(), Model: id, Rev: 2, Spec: raw(`{}`)})
	wantErr(t, err, "retired")
}
