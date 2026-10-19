package catalog_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

const (
	// reasoningSpec is a spec that declares what the upstream may report and the model reasons.
	reasoningSpec = `{"items":["input","output"],"tiers":[],"abilities":["reasoning"],"note":"keep me"}`
	claudeKeys    = `{"thinking":"adaptive","mid_system":true,"untrusted_system_after_tools":false,
		"default_sampling_only":true,"forced_tool_choice":false}`
)

// withKeys is reasoningSpec with the members of keys, a JSON object, added.
func withKeys(keys string) string {
	return strings.TrimSuffix(reasoningSpec, "}") + "," + strings.TrimPrefix(keys, "{")
}

func (s *store) problems(authors ...string) map[string][]string {
	s.t.Helper()
	checks, err := s.c.Check(s.t.Context(), authors)
	if err != nil {
		s.t.Fatal(err)
	}
	found := map[string][]string{}
	for _, c := range checks {
		found[c.Slug] = c.Problems
	}
	return found
}

// saysSpecLacks reports whether a problem names exactly the keys as the ones the spec does not say.
func saysSpecLacks(problems []string, keys string) bool {
	for _, p := range problems {
		if strings.Contains(p, "does not say "+keys+",") {
			return true
		}
	}
	return false
}

func aboutDialect(problems []string) bool {
	for _, p := range problems {
		if strings.Contains(p, "does not say") {
			return true
		}
	}
	return false
}

// TestCheckAsksForTheDialectOfAVendorsModels: a model of a vendor whose requests read the dialect needs its keys, each
// one named when missing, so a release finds a model no request can be written for before a client does; a vendor's model
// whose requests do not read it is not asked.
func TestCheckAsksForTheDialectOfAVendorsModels(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("anthropic/bare", reasoningSpec, basic)
	s.seed("anthropic/trusts", withKeys(`{"thinking":"adaptive","mid_system":true,"default_sampling_only":false,"forced_tool_choice":true}`), basic)
	s.seed("anthropic/full", withKeys(claudeKeys), basic)
	s.seed("anthropic/plain", `{"items":["input","output"],"tiers":[],"abilities":["tools"],"mid_system":false,"default_sampling_only":false,"forced_tool_choice":true}`, basic)
	s.seed("openai/bare", reasoningSpec, basic)
	s.seed("openai/full", withKeys(`{"encrypted_reasoning":true,"cache_breakpoints":false}`), basic)
	s.seed("acme/bare", reasoningSpec, basic)
	found := s.problems("anthropic", "openai", "acme")
	for slug, keys := range map[string]string{
		"anthropic/bare":   "thinking, default_sampling_only, forced_tool_choice, mid_system",
		"anthropic/trusts": "untrusted_system_after_tools",
		"openai/bare":      "encrypted_reasoning, cache_breakpoints",
	} {
		if !saysSpecLacks(found[slug], keys) {
			t.Errorf("%s: problems %q, want one saying the spec lacks %s", slug, found[slug], keys)
		}
	}
	for _, slug := range []string{"anthropic/full", "anthropic/plain", "openai/full", "acme/bare"} {
		if aboutDialect(found[slug]) {
			t.Errorf("%s: problems %q, want none about the dialect", slug, found[slug])
		}
	}
}

// TestCheckListsEveryRowItCannotRead: a spec the catalog cannot parse is that model's problem, and the check goes on, so
// one run lists every row to fix before the snapshot fails to load with them.
func TestCheckListsEveryRowItCannotRead(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for _, slug := range []string{"anthropic/first", "anthropic/second", "anthropic/fine"} {
		s.seed(slug, reasoningSpec, basic)
	}
	for slug, bad := range map[string]string{"anthropic/first": `{"thinking":true}`, "anthropic/second": `{"cache_breakpoints":"auto"}`} {
		if err := s.exec(platformScope, "UPDATE catalog.model SET spec = $1::jsonb WHERE slug = $2", bad, slug); err != nil {
			t.Fatal(err)
		}
	}
	found := s.problems("anthropic")
	for _, slug := range []string{"anthropic/first", "anthropic/second"} {
		if !strings.Contains(strings.Join(found[slug], "\n"), "does not parse") {
			t.Errorf("%s: problems %q, want its spec reported as unreadable", slug, found[slug])
		}
	}
	if strings.Contains(strings.Join(found["anthropic/fine"], "\n"), "does not parse") {
		t.Errorf("a model with a good spec was reported: %q", found["anthropic/fine"])
	}
	// The read-only query of the release runbook finds the same rows, so the one a person runs first is not a dead letter.
	var slugs []string
	query := runbookQuery(t)
	if err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var slug, key string
			var got *string
			if err := rows.Scan(&slug, &key, &got); err != nil {
				return err
			}
			slugs = append(slugs, slug)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(slugs)
	if !slices.Equal(slugs, []string{"anthropic/first", "anthropic/second"}) {
		t.Errorf("the runbook's query found %v", slugs)
	}
}

// runbookQuery is the first SQL block of docs/ops/release.md.
func runbookQuery(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../docs/ops/release.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(doc), "```sql\n")
	query, _, closed := strings.Cut(rest, "```")
	if !found || !closed {
		t.Fatal("docs/ops/release.md has no SQL block")
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
}

// TestASavedSpecIsConsistent: a spec that says the model reasons and does not think, or thinks and does not reason, is
// refused when it is saved, whether the model is new or not; one that says nothing of thinking is not (the check says
// so), and neither is one that does not list abilities, which cannot be told.
func TestASavedSpecIsConsistent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for i, c := range []struct {
		name, spec, want string
	}{
		{"reasons but does not think", `{"abilities":["reasoning"],"thinking":"none"}`, "does not think"},
		{"thinks but does not reason", `{"abilities":["tools"],"thinking":"adaptive"}`, "does not list reasoning"},
		{"thinks manually but does not reason", `{"abilities":[],"thinking":"manual"}`, "does not list reasoning"},
		{"reasons, says nothing of thinking", `{"abilities":["reasoning"]}`, ""},
		{"does not reason, does not think", `{"abilities":["tools"],"thinking":"none"}`, ""},
		{"thinks, lists no abilities", `{"thinking":"adaptive"}`, ""},
	} {
		id, _ := s.seed("acme/m"+string(rune('a'+i)), basicSpec, basic)
		_, err := s.c.UpdateSpec(s.t.Context(), catalog.UpdateSpecRequest{Op: op(), Model: id, Rev: 0, Spec: raw(c.spec)})
		_, created := s.c.CreateModel(s.t.Context(), catalog.CreateModelRequest{
			Op: op(), Slug: "acme/n" + string(rune('a'+i)), VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: raw(c.spec),
		})
		if c.want == "" {
			if err != nil || created != nil {
				t.Errorf("%s: %v, %v", c.name, err, created)
			}
			continue
		}
		wantErr(t, err, c.want)
		if !errors.Is(err, catalog.ErrInvalid) || !errors.Is(created, catalog.ErrInvalid) {
			t.Errorf("%s: %v, %v are not ErrInvalid", c.name, err, created)
		}
	}
}

func (s *store) view(id catalog.ModelID) catalog.ModelView {
	s.t.Helper()
	view, err := s.c.Model(s.t.Context(), id)
	if err != nil {
		s.t.Fatal(err)
	}
	return view
}

func dialectFileOf(t *testing.T, doc string) catalog.DialectFile {
	t.Helper()
	f, err := catalog.ParseDialectFile([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func outcomes(results []catalog.DialectResult) map[string]string {
	m := map[string]string{}
	for _, r := range results {
		m[r.Slug] = r.Outcome
	}
	return m
}

func sameOutcomes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestApplyDialects: the file's keys go into the specs of the models of the same slugs and nothing else changes; a run
// without apply changes nothing; a second run changes nothing; a model that cannot be changed does not stop the others.
func TestApplyDialects(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	a, _ := s.seed("anthropic/a", reasoningSpec, basic)
	longer, _ := s.seed("anthropic/a-2026", reasoningSpec, basic) // a slug the file's slug is a prefix of
	noReasoning, _ := s.seed("anthropic/c", `{"items":["input","output"],"tiers":[],"abilities":["tools"]}`, basic)
	file := dialectFileOf(t, `{"models":{
		"anthropic/a":       `+claudeKeys+`,
		"anthropic/c":       {"thinking":"adaptive"},
		"anthropic/missing": {"mid_system":true}}}`)
	before := s.view(a)
	actor := op().Actor

	plan := s.c.ApplyDialects(ctx, actor, file, "test", false)
	want := map[string]string{"anthropic/a": catalog.DialectSet, "anthropic/c": catalog.DialectSet, "anthropic/missing": catalog.DialectAbsent}
	if got := outcomes(plan); !sameOutcomes(got, want) {
		t.Fatalf("a dry run reported %v, want %v", got, want)
	}
	if after := s.view(a); string(after.Spec) != string(before.Spec) || after.Rev != before.Rev {
		t.Errorf("a dry run changed the model: %s at rev %d", after.Spec, after.Rev)
	}

	applied := s.c.ApplyDialects(ctx, actor, file, "test", true)
	want["anthropic/c"] = catalog.DialectFailed // the spec lists no reasoning, which a model that thinks does
	if got := outcomes(applied); !sameOutcomes(got, want) {
		t.Fatalf("a run reported %v, want %v", got, want)
	}
	if !strings.Contains(applied[1].Error, "reasoning") {
		t.Errorf("the model that failed says %q", applied[1].Error)
	}
	view := s.view(a)
	if view.Rev != before.Rev+1 {
		t.Errorf("rev %d, want %d", view.Rev, before.Rev+1)
	}
	var spec map[string]any
	if err := jsonx.Unmarshal(view.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"note": "keep me", "thinking": "adaptive", "mid_system": true, "forced_tool_choice": false} {
		if spec[key] != want {
			t.Errorf("the spec %s has %v for %s, want %v", view.Spec, spec[key], key, want)
		}
	}
	if abilities, _ := spec["abilities"].([]any); len(abilities) != 1 {
		t.Errorf("the spec %s lost its abilities", view.Spec)
	}
	if got := s.view(longer); string(got.Spec) != string(before.Spec) {
		t.Errorf("a model whose slug the file's is a prefix of was changed: %s", got.Spec)
	}
	if got := s.view(noReasoning); got.Rev != 0 {
		t.Errorf("the model that failed was changed")
	}
	if n := query[int](s, platformScope, "SELECT count(*) FROM catalog.change WHERE kind = 'model.update_spec'"); n != 1 {
		t.Errorf("%d spec changes were recorded, want the one", n)
	}

	again := s.c.ApplyDialects(ctx, actor, file, "test", true)
	if got := outcomes(again)["anthropic/a"]; got != catalog.DialectUnchanged {
		t.Errorf("a second run on the model: %q, want unchanged", got)
	}
	if s.view(a).Rev != view.Rev {
		t.Error("a second run changed the model")
	}

	// A key that is there with another value is changed, and the run says from what.
	flip := dialectFileOf(t, `{"models":{"anthropic/a":{"mid_system":false}}}`)
	got := s.c.ApplyDialects(ctx, actor, flip, "test", false)
	if len(got[0].Changes) != 1 || got[0].Changes[0] != "mid_system: true -> false" {
		t.Errorf("changes %q", got[0].Changes)
	}
}

// TestParseDialectFile: a member the file should not have, a thinking mode the catalog does not know, and a model with no
// key are refused where the file is read, before any model is touched.
func TestParseDialectFile(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, doc, want string }{
		{"a model with a key set", `{"models":{"a/b":{"mid_system":true}}}`, ""},
		{"an unknown member", `{"models":{"a/b":{"mid_system":true}},"extra":1}`, "extra"},
		{"an unknown key", `{"models":{"a/b":{"midsystem":true}}}`, "midsystem"},
		{"an unknown thinking mode", `{"models":{"a/b":{"thinking":"telepathy"}}}`, "telepathy"},
		{"a model with no key", `{"models":{"a/b":{}}}`, "sets no key"},
		{"a key that is no bool", `{"models":{"a/b":{"mid_system":"yes"}}}`, "dialect file"},
	} {
		_, err := catalog.ParseDialectFile([]byte(c.doc))
		if c.want == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if c.want != "" {
			wantErr(t, err, c.want)
		}
	}
}
