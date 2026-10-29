package catalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
)

// bases are the current price rows of every currency the model has any history in.
func (s *store) bases(ctx context.Context, id catalog.ModelID) map[string]uuid.UUID {
	s.t.Helper()
	bases := map[string]uuid.UUID{}
	err := s.db.Tx(ctx, platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.currency, r.id FROM catalog.reference_price r WHERE r.model_id = $1
			AND NOT EXISTS (SELECT FROM catalog.reference_price n WHERE n.supersedes = r.id)`, uuid.UUID(id))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var currency string
			var head uuid.UUID
			if err := rows.Scan(&currency, &head); err != nil {
				return err
			}
			bases[currency] = head
		}
		return rows.Err()
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return bases
}

// switchTo asks to select the official source with the given schedules, on the heads now current unless bases is given.
func (s *store) switchTo(id catalog.ModelID, bases map[string]uuid.UUID, confirm bool, schedules ...string) error {
	s.t.Helper()
	if bases == nil {
		bases = s.bases(s.t.Context(), id)
	}
	r := catalog.SelectPriceSourceRequest{
		Op: op(), Model: id, Rev: query[int64](s, platformScope, "SELECT rev FROM catalog.model WHERE id = $1", uuid.UUID(id)),
		Source: catalog.SourceOfficial, Bases: bases, ConfirmBasisChange: confirm,
	}
	for _, schedule := range schedules {
		r.Prices = append(r.Prices, catalog.SourcePrice{Schedule: raw(schedule), Evidence: s.officialEvidence(s.t.Context(), "acme", mustScheduleCurrency(s, schedule))})
	}
	_, err := s.c.SelectPriceSource(s.t.Context(), r)
	return err
}

// TestSelectPriceSourceChecksTheReviewedHeads: a switch decided on one set of price heads is refused once a publish
// moved one, in any currency, or when a currency's head is missing from or invented in the request.
func TestSelectPriceSourceChecksTheReviewedHeads(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, usd := s.seed("acme/alpha", basicSpec, basic)
	reviewed := s.bases(t.Context(), id)
	// A publish lands between the review and the switch. The model's rev does not change.
	s.must(s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{
		Op: op(), Model: id, Base: &usd, Source: catalog.SourceManual, Schedule: raw(basic), Evidence: raw(evidence),
	}))
	wantErr(t, s.switchTo(id, reviewed, false, basic), "not the one the request was decided on")
	wantErr(t, s.switchTo(id, map[string]uuid.UUID{}, false, basic), "not the one the request was decided on")
	wantErr(t, s.switchTo(id, map[string]uuid.UUID{"USD": s.head(id, "USD"), "CNY": usd}, false, basic), "no history")
	if err := s.switchTo(id, nil, false, basic); err != nil {
		t.Fatal(err)
	}
}

// TestSelectPriceSourceKeepsEveryPrice: every new schedule is compared with every currency that has a price, so
// switching to another currency, or withdrawing one, cannot drop a price; a changed threshold basis needs confirming.
func TestSelectPriceSourceKeepsEveryPrice(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", priorityEnv, withPriority) // a draft: not held to the listing gate
	noPriorityInput := `{"v":1,"currency":"CNY","rates":{"input":"7","output":"14"},"service_tiers":{"priority":{"rates":{"output":"16"}}}}`
	wantErr(t, s.switchTo(id, nil, false, noPriorityInput), "would lose", "against the current USD price")
	if err := s.switchTo(id, nil, false, strings.Replace(withPriority, "USD", "CNY", 1)); err != nil {
		t.Fatalf("a currency switch that keeps every price: %v", err)
	}
	if got := query[bool](s, platformScope, "SELECT schedule IS NULL FROM catalog.reference_price WHERE id = $1", s.head(id, "USD")); !got {
		t.Error("USD was not withdrawn")
	}
	tiered := func(currency, basis string) string {
		return `{"v":1,"currency":"` + currency + `","rates":{"input":"1","output":"2"},"context_tiers":{"basis":[` + basis +
			`],"method":"whole","tiers":[{"above":100,"rates":{"input":"2","output":"4"}}]}}`
	}
	other, _ := s.seed("acme/tiered", basicSpec, tiered("USD", `"input"`))
	if err := s.switchTo(other, nil, false, tiered("CNY", `"input","cache_read"`)); !errors.Is(err, catalog.ErrTierBasisChanged) {
		t.Fatalf("a different basis in another currency: %v", err)
	}
	if err := s.switchTo(other, nil, true, tiered("CNY", `"input","cache_read"`)); err != nil {
		t.Fatalf("confirmed: %v", err)
	}
}

// TestUpdateSpecKeepsTheDeclarations: a listed model's spec must keep declaring both lists; null and absent are not
// declarations, an explicit empty list is.
func TestUpdateSpecKeepsTheDeclarations(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(id)
	for _, spec := range []string{`{}`, `{"items":["input"]}`, `{"tiers":[]}`, `{"items":null,"tiers":[]}`, `{"items":[],"tiers":null}`} {
		_, err := s.c.UpdateSpec(t.Context(), catalog.UpdateSpecRequest{Op: op(), Model: id, Rev: 1, Spec: raw(spec)})
		wantErr(t, err, `must declare "items" and "tiers"`)
	}
	s.must(s.c.UpdateSpec(t.Context(), catalog.UpdateSpecRequest{Op: op(), Model: id, Rev: 1, Spec: raw(`{"items":[],"tiers":[]}`)}))
}

// TestOperationKeyIsRequired: an omitted or zero key is refused instead of being taken for the retry of the first
// operation that also had none; the database refuses it too.
func TestOperationKeyIsRequired(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	for _, key := range []uuid.UUID{{}, uuid.Nil()} {
		_, err := s.c.SaveAuthor(ctx, catalog.SaveAuthorRequest{Actor: op().Actor, Key: key, Reason: "r", Slug: "a", Name: "A"})
		if !errors.Is(err, catalog.ErrNoOpKey) {
			t.Errorf("key %v: %v", key, err)
		}
	}
	wantErr(t, s.exec(platformScope, `INSERT INTO catalog.change (op_key, actor, reason, kind, subject)
		VALUES ('00000000-0000-0000-0000-000000000000', 'a', 'r', 'x.y', 's')`), "op_key")
	first := catalog.SaveAuthorRequest{Op: op(), Slug: "a", Name: "A"}
	s.must(s.c.SaveAuthor(ctx, first))
	if again := s.must(s.c.SaveAuthor(ctx, first)); !again.Replayed {
		t.Error("a valid key was not replayed")
	}
	if r := s.must(s.c.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: "b", Name: "B"})); r.Replayed {
		t.Error("a different operation was taken for a replay")
	}
}

// TestNameLengthIsInBytes: one rule everywhere: 1 to 200 bytes, so a name that can be saved can be resolved.
func TestNameLengthIsInBytes(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(id)
	fits := strings.Repeat("é", 99) + "ab" // 200 bytes, 101 characters
	over := strings.Repeat("é", 100) + "a" // 201 bytes, 101 characters
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: id, Name: fits, Visibility: catalog.VisibilityPublic}))
	if got, err := s.current().ResolvePublic(fits); err != nil || got.Model != id {
		t.Errorf("a 200-byte name saved but not resolved: %+v, %v", got, err)
	}
	for _, name := range []string{over, "", strings.Repeat("a", 201)} {
		_, err := s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: id, Name: name, Visibility: catalog.VisibilityPublic})
		if !errors.Is(err, catalog.ErrNameLength) {
			t.Errorf("%d bytes: %v", len(name), err)
		}
	}
	wantErr(t, s.exec(platformScope, "INSERT INTO catalog.name (name, norm, visibility, model_id) VALUES ($1, $1, 'internal', $2)",
		over, uuid.UUID(id)), "name_name_check")
}

// TestSnapshotCannotBeChangedByItsReaders: the spec a caller gets is a copy, from both ways out.
func TestSnapshotCannotBeChangedByItsReaders(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(id)
	snap := s.current()
	m, _ := snap.Model(id)
	m.Spec()[0] = 'X'
	for listed := range snap.Models() {
		listed.Spec()[1] = 'X'
	}
	again, _ := snap.Model(id)
	if got := string(again.Spec()); !strings.HasPrefix(got, `{"`) {
		t.Errorf("a caller changed the snapshot's spec to %q", got)
	}
}

func TestOpenRefusesNonPositiveDurations(t *testing.T) {
	t.Parallel()
	for _, cfg := range []catalog.Config{{PollEvery: -time.Second}, {MaxStale: -time.Second}, {MaxStale: -time.Minute, PollEvery: -time.Hour}} {
		_, err := catalog.Open(nil, platformScope, cfg)
		wantErr(t, err, "positive")
	}
}

func mustScheduleCurrency(s *store, schedule string) string {
	s.t.Helper()
	p, err := catalog.ParseSchedule([]byte(schedule))
	if err != nil {
		s.t.Fatal(err)
	}
	return p.Currency().String()
}
