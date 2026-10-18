package persist_test

import (
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/syncer"
)

// A listing, as the tests below read one: the groups the supplier sells, the models of each, and the cost plan of the ones
// whose price the listing shows. It is what syncer.State keeps after a round that read the catalog, and what the
// projection writes onto the supplier's tables.

// priced is one version of a model's cost plan, as the engine works it out.
type priced struct{ fingerprint, schedule, basis string }

// The cost plan versions the tests use: a price the supplier published, and the draft a person confirmed.
var (
	unitPrice = priced{"5f2a1c0e", `{"v":1,"currency":"XXX","rates":{"input":"0.075","output":"0.3"}}`, "published"}
	plusVAT   = priced{"8b1d3f44", `{"v":1,"currency":"XXX","rates":{"input":"0.09","output":"0.36"}}`, "confirmed"}
)

// listedModel is a model of a group in a listing. plan is nil for a model whose listing shows no price.
type listedModel struct {
	name    string
	formats []protocol.Format
	plan    *priced
}

// listedGroup is a group of a listing.
type listedGroup struct {
	ref, name, ratio, facts string
	models                  []listedModel
}

// listing is a commit of the state a round that read these groups leaves: the models of each, sorted as the engine sorts
// them, with the plans of the versions the listing shows.
func listing(version int64, groups []listedGroup) syncer.Commit {
	st := syncer.State{Version: version, Groups: make([]syncer.GroupSnap, len(groups))}
	st.Tasks[syncer.TaskCatalog].LastOK = t0
	var plans []syncer.Plan
	for i, g := range groups {
		snap := syncer.GroupSnap{Ref: g.ref, Name: g.name, Ratio: g.ratio, Facts: g.facts}
		for _, m := range g.models {
			formats := make([]int, len(m.formats))
			for j, f := range m.formats {
				formats[j] = int(f)
			}
			model := syncer.ModelSnap{Name: m.name, Formats: formats}
			if m.plan != nil {
				model.Plan = m.plan.fingerprint
				plans = append(plans, syncer.Plan{
					Endpoint: g.ref + "/" + m.name, Fingerprint: m.plan.fingerprint, Schedule: m.plan.schedule, Basis: m.plan.basis,
				})
			}
			snap.Models = append(snap.Models, model)
		}
		slices.SortFunc(snap.Models, func(a, b syncer.ModelSnap) int { return strings.Compare(a.Name, b.Name) })
		st.Groups[i] = snap
	}
	return syncer.Commit{State: st, Plans: plans}
}

// commitListing stores a round's listing as the state's next version, and fails the test on an error.
func (c *cluster) commitListing(s *persist.Session, version int64, groups []listedGroup) {
	c.t.Helper()
	if err := s.Store().Commit(c.t.Context(), version-1, listing(version, groups)); err != nil {
		c.t.Fatalf("commit version %d: %v", version, err)
	}
}

// model makes a catalog model that carries the name as its public name, in the status, and returns its id. The name is
// the one a listing says, so that the listing maps by it. The catalog's own writes make the model and the name; the
// status is then written directly, because putting a model on sale is catalog's review and not what a test of the
// projection is about, and the revision is raised beside it, because that is the only thing a snapshot is reloaded on.
func (c *cluster) model(name string, status catalog.Status) catalog.ModelID {
	c.t.Helper()
	ctx := c.t.Context()
	if _, err := c.catalog.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: c.op(), Slug: "acme", Name: "Acme"}); err != nil {
		c.t.Fatal(err)
	}
	slug := "acme/" + strings.NewReplacer(":", "-", "@", "-").Replace(name)
	created, err := c.catalog.CreateModel(ctx, catalog.CreateModelRequest{
		Op: c.op(), Slug: slug, VersionBasis: catalog.VersionDated, PriceSource: catalog.SourceManual, Spec: spec,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	var id catalog.ModelID
	if err := id.UnmarshalText([]byte(created.Subject)); err != nil {
		c.t.Fatalf("the created model's id %q: %v", created.Subject, err)
	}
	if _, err := c.catalog.AddName(ctx, catalog.AddNameRequest{Op: c.op(), Model: id, Name: name, Visibility: catalog.VisibilityPublic}); err != nil {
		c.t.Fatal(err)
	}
	err = c.open().Tx(ctx, platform, func(tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE catalog.model SET status = $1 WHERE id = $2", string(status), uuid.UUID(id))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE catalog.revision SET n = n + 1")
		return err
	})
	if err != nil {
		c.t.Fatal(err)
	}
	// The snapshot the projection maps by is rebuilt after the models are there; the sync does not see them otherwise.
	if err := c.catalog.Refresh(ctx); err != nil {
		c.t.Fatal(err)
	}
	return id
}

// spec is a model's spec, minimal and valid: what it may report, and the four keys Serving reads.
var spec = jsonx.Raw(`{"items":["input","output"],"tiers":[],"context_length":200000,"max_output_tokens":64000,` +
	`"streams":true,"abilities":["tools"]}`)

// op is an operation key for a catalog write, distinct each time as a person's is.
func (c *cluster) op() catalog.Op {
	c.t.Helper()
	return catalog.Op{Actor: audit.System("suppliers.test"), Key: uuid.NewV7(), Reason: "a listing test"}
}

// name adds another public name for a model, as the catalog's own write does. A name without a date is only a candidate
// of the version it stands for (catalog's Recognize).
func (c *cluster) name(model catalog.ModelID, name string) {
	c.t.Helper()
	req := catalog.AddNameRequest{Op: c.op(), Model: model, Name: name, Visibility: catalog.VisibilityPublic}
	if _, err := c.catalog.AddName(c.t.Context(), req); err != nil {
		c.t.Fatal(err)
	}
	// The snapshot the projection maps by is rebuilt after the name is there; the sync does not see it otherwise.
	if err := c.catalog.Refresh(c.t.Context()); err != nil {
		c.t.Fatal(err)
	}
}

// alias makes the supplier's alias for an upstream name, as a person sets one.
func (c *cluster) alias(group uuid.UUID, name string, model catalog.ModelID) {
	c.t.Helper()
	var in any
	if group != uuid.Nil() {
		in = group
	}
	err := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(c.t.Context(), `INSERT INTO suppliers.supplier_aliases (supplier_id, supplier_group_id, upstream_name, model_id, set_by)
			VALUES ($1, $2, $3, $4, 'user')`, c.supplier, in, name, uuid.UUID(model))
		return err
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

// tables are the tables a sync writes, with the key a test tells their rows apart by.
var tables = [][2]string{
	{"supplier_groups", "id::text"},
	{"supplier_models", "supplier_group_id::text || '/' || upstream_name"},
	{"endpoints", "id::text"},
	{"cost_schedules", "id::text"},
	{"supplier_aliases", "coalesce(supplier_group_id::text, '-') || '/' || upstream_name"},
}

// column reads one column of the supplier's rows of a table, in the order given.
func (c *cluster) column(table, col, order string) []string {
	c.t.Helper()
	var out []string
	err := c.open().ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(c.t.Context(), "SELECT "+col+" FROM suppliers."+table+" WHERE supplier_id = $1 ORDER BY "+order, c.supplier)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v *string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			if v == nil {
				out = append(out, "NULL")
			} else {
				out = append(out, *v)
			}
		}
		return rows.Err()
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

// marks is what a test compares to see that a table was not written again: the key of each row with the transaction that
// wrote it. A row that was updated has a new one, even if the update left its values as they were.
func (c *cluster) marks(table, key string) []string {
	c.t.Helper()
	var out []string
	err := c.open().ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(c.t.Context(),
			"SELECT "+key+" || ' ' || xmin::text FROM suppliers."+table+" WHERE supplier_id = $1 ORDER BY 1", c.supplier)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

// allMarks reads the five tables as one value, which a test compares before and after a step.
func (c *cluster) allMarks() [][]string {
	c.t.Helper()
	out := make([][]string, len(tables))
	for i, t := range tables {
		out[i] = c.marks(t[0], t[1])
	}
	return out
}

// value reads one value in the platform scope.
func (c *cluster) value(dest any, query string, args ...any) {
	c.t.Helper()
	err := c.open().ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(c.t.Context(), query, args...).Scan(dest)
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

// pending lists the supplier's names that wait to be mapped, as the back office's queue of them does.
func (c *cluster) pending() []string {
	c.t.Helper()
	var out []string
	err := c.open().ReadTx(c.t.Context(), platform, func(tx pg.Tx) error {
		rows, err := tx.Query(c.t.Context(), `SELECT m.upstream_name FROM suppliers.supplier_models m
			JOIN suppliers.supplier_groups g ON g.id = m.supplier_group_id
			WHERE m.supplier_id = $1 AND m.model_id IS NULL AND m.present AND g.present
				AND NOT EXISTS (SELECT FROM suppliers.supplier_aliases a WHERE a.supplier_id = m.supplier_id
					AND a.upstream_name = m.upstream_name AND (a.supplier_group_id IS NULL OR a.supplier_group_id = m.supplier_group_id))
			ORDER BY 1`, c.supplier)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			out = append(out, name)
		}
		return rows.Err()
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

// same fails the test unless got equals want.
func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// TestAListingFillsTheTablesAndReadingItAgainWritesNothing is the card's acceptance test: after one sync the groups, the
// models, the endpoints and the cost schedules of a listing are rows, and what the listing cannot use is either left out
// of them or kept for the queue of names to map; committing the same listing again writes nothing in any of the five
// tables.
func TestAListingFillsTheTablesAndReadingItAgainWritesNothing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	sonnet := c.model("claude-sonnet-4-5-20250929", catalog.StatusListed)
	gpt := c.model("gpt-4o", catalog.StatusListed)
	c.model("draft-model", catalog.StatusDraft)

	groups := []listedGroup{
		{ref: "default", name: "Default", ratio: "0.8", facts: `{"platform":"openai"}`, models: []listedModel{
			{name: "claude-sonnet-4-5-20250929", formats: []protocol.Format{protocol.Messages, protocol.Chat}, plan: &unitPrice},
			{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
			// The catalog knows no such name, and a suffix is never one of its names: only an alias maps this.
			{name: "mystery-model:free", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		}},
		{ref: "vip", name: "VIP", ratio: "1.2", models: []listedModel{
			{name: "claude-sonnet-4-5-20250929", formats: []protocol.Format{protocol.Messages}, plan: &plusVAT},
			// A draft model is not on sale, so it gets no endpoint, whatever the listing prices it at.
			{name: "draft-model", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		}},
	}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, groups)

	// The groups, made unselected: which groups to use is a person's choice.
	same(t, "supplier_groups", c.column("supplier_groups", "upstream_ref || ' ' || ratio::text || ' ' || facts::text || ' ' || present::text || selected::text || coalesce(codename, '-')", "upstream_ref"),
		[]string{`default 0.8 {"platform": "openai"} truefalse-`, "vip 1.2 {} truefalse-"})

	// Every name the listing has is a row, with the model it maps to, or none. The two rows of one name are the two
	// groups, in the order formats::text sorts them.
	same(t, "supplier_models", c.column("supplier_models", "upstream_name || ' ' || coalesce(model_id::text, '-') || ' ' || formats::text || present::text", "upstream_name, formats::text"),
		[]string{
			"claude-sonnet-4-5-20250929 " + sonnet.String() + " {messages,chat}true",
			"claude-sonnet-4-5-20250929 " + sonnet.String() + " {messages}true",
			"draft-model - {chat}true",
			"gpt-4o " + gpt.String() + " {chat}true",
			"mystery-model:free - {chat}true",
		})

	// An endpoint for each mapped, listed model of the listing — with the name a call sends upstream — and only those:
	// the unmapped and the draft model have none.
	same(t, "endpoints", c.column("endpoints",
		"upstream_name || ' ' || model_id::text || ' ' || formats::text || ' ' || enabled::text || ' ' || cost_state", "upstream_name, formats::text"),
		[]string{
			"claude-sonnet-4-5-20250929 " + sonnet.String() + " {messages,chat} true known",
			"claude-sonnet-4-5-20250929 " + sonnet.String() + " {messages} true known",
			"gpt-4o " + gpt.String() + " {chat} true known",
		})

	// A cost schedule per endpoint, holding the plan the listing gave, in the supplier's native unit.
	same(t, "cost_schedules", c.column("cost_schedules", "native_unit || ' ' || basis || ' ' || schedule::text", "basis, schedule::text"),
		[]string{
			`quota confirmed {"v": 1, "rates": {"input": "0.09", "output": "0.36"}, "currency": "XXX"}`,
			`quota published {"v": 1, "rates": {"input": "0.075", "output": "0.3"}, "currency": "XXX"}`,
			`quota published {"v": 1, "rates": {"input": "0.075", "output": "0.3"}, "currency": "XXX"}`,
		})

	// The sync does not decide aliases: a name the catalog does not know waits in the queue of names to map.
	if got := c.count("supplier_aliases"); got != 0 {
		t.Errorf("the sync wrote %d aliases, want none", got)
	}
	same(t, "the names to map", c.pending(), []string{"draft-model", "mystery-model:free"})

	// The same listing again: the state moves on, and no row of the five tables is written.
	before, events := c.allMarks(), c.count("sync_events")
	c.commitListing(s, 2, groups)
	if after := c.allMarks(); !sameMarks(after, before) {
		t.Errorf("the second read of the listing wrote to the tables:\n got %q\nwant %q", after, before)
	}
	if got := c.count("sync_events"); got != events {
		t.Errorf("the second read of the listing added %d events", got-events)
	}
	var version int64
	c.value(&version, "SELECT version FROM suppliers.sync_states WHERE supplier_id = $1", c.supplier)
	if version != 2 {
		t.Errorf("the state is at version %d, want 2: the round was not run again", version)
	}
}

// sameMarks reports whether two readings of the tables are the same.
func sameMarks(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}

// TestAnAliasMapsANameBeforeTheCatalogDoes: an upstream name maps by the supplier's alias first, so a name the catalog
// would map to one model maps to the model the alias names; two names of one group that stand for the same model are one
// endpoint, the schema's one per group and model.
func TestAnAliasMapsANameBeforeTheCatalogDoes(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	sonnet := c.model("claude-sonnet-4-5-20250929", catalog.StatusListed)
	c.model("gpt-4o", catalog.StatusListed)
	c.alias(uuid.Nil(), "gpt-4o", sonnet)
	c.alias(uuid.Nil(), "sonnet:free", sonnet)

	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		{name: "sonnet:free", formats: []protocol.Format{protocol.Messages}, plan: &unitPrice},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, groups)

	same(t, "the model each name maps to", c.column("supplier_models", "upstream_name || ' ' || coalesce(model_id::text, '-')", "upstream_name"),
		[]string{"gpt-4o " + sonnet.String(), "sonnet:free " + sonnet.String()})
	// Both names are the alias's, so both are evidence of the version and the first by name keeps the endpoint.
	same(t, "the endpoint of the model the two names stand for",
		c.column("endpoints", "upstream_name || ' ' || model_id::text || ' ' || formats::text", "upstream_name"),
		[]string{"gpt-4o " + sonnet.String() + " {chat}"})
	same(t, "the names to map", c.pending(), nil)
}

// TestAnEndpointIsTheNameThatIsEvidenceOfTheVersion: one group may offer one model under two names, the dated one and one
// the vendor moves. The endpoint is the dated name — the only one that says which version a call gets — and stays it,
// whatever order the listing read them in: the two names are one endpoint because a group has one of each model.
func TestAnEndpointIsTheNameThatIsEvidenceOfTheVersion(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	sonnet := c.model("claude-sonnet-4-5-20250929", catalog.StatusListed)
	c.name(sonnet, "claude-sonnet-4-5")

	// The engine sorts the names of a group, so the candidate is read first and the dated name has to take the endpoint.
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "claude-sonnet-4-5", formats: []protocol.Format{protocol.Messages}, plan: &unitPrice},
		{name: "claude-sonnet-4-5-20250929", formats: []protocol.Format{protocol.Messages}, plan: &unitPrice},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, groups)

	same(t, "the model both names stand for",
		c.column("supplier_models", "upstream_name || ' ' || coalesce(model_id::text, '-')", "upstream_name"),
		[]string{"claude-sonnet-4-5 " + sonnet.String(), "claude-sonnet-4-5-20250929 " + sonnet.String()})
	same(t, "the endpoint of that model", c.column("endpoints", "upstream_name || ' ' || model_id::text", "upstream_name"),
		[]string{"claude-sonnet-4-5-20250929 " + sonnet.String()})
}

// TestAModelWithoutACostPlanHasNoEndpoint: a model that is on sale but has no cost plan is a row of the listing, not an
// endpoint: routing would have nothing to price a call with, so the endpoint appears when the plan does.
func TestAModelWithoutACostPlanHasNoEndpoint(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	gpt := c.model("gpt-4o", catalog.StatusListed)
	withPrice := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	withoutPrice := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, withoutPrice)
	if got := [3]int{c.count("supplier_models"), c.count("endpoints"), c.count("cost_schedules")}; got != [3]int{1, 0, 0} {
		t.Fatalf("a listing whose model has no plan wrote %d models, %d endpoints and %d schedules, want 1, 0 and 0",
			got[0], got[1], got[2])
	}

	c.commitListing(s, 2, withPrice)
	same(t, "the endpoint of the priced model",
		c.column("endpoints", "model_id::text || ' ' || enabled::text || ' ' || cost_state", "id"),
		[]string{gpt.String() + " true known"})
	if got := c.count("cost_schedules"); got != 1 {
		t.Errorf("%d cost schedules, want 1", got)
	}
}

// TestAnEndpointAPersonSwitchedOffStaysOff: enabled is a person's switch. A sync that changes an endpoint's cost plan
// writes the new schedule and leaves the switch as it was.
func TestAnEndpointAPersonSwitchedOffStaysOff(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-4o", catalog.StatusListed)
	at := func(p priced) []listedGroup {
		return []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
			{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &p},
		}}}
	}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, at(unitPrice))
	err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "UPDATE suppliers.endpoints SET enabled = false WHERE supplier_id = $1", c.supplier)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	c.commitListing(s, 2, at(plusVAT))
	same(t, "the endpoint after a new plan", c.column("endpoints", "enabled::text || ' ' || cost_state", "id"), []string{"false known"})
	same(t, "the schedules it has been priced by", c.column("cost_schedules", "basis", "basis"), []string{"confirmed", "published"})
}

// TestAModelThatLeavesTheListingKeepsItsRows: a group or a model the listing no longer has is marked absent rather than
// deleted, and its endpoint stops being routable; when it comes back it is the row it was, priced by the schedule it had,
// and it does not look like a name that has just arrived.
func TestAModelThatLeavesTheListingKeepsItsRows(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-4o", catalog.StatusListed)
	c.model("claude-sonnet-4-5-20250929", catalog.StatusListed)
	full := []listedGroup{
		{ref: "default", name: "Default", ratio: "1", models: []listedModel{
			{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		}},
		{ref: "legacy", name: "Legacy", ratio: "1", models: []listedModel{
			{name: "claude-sonnet-4-5-20250929", formats: []protocol.Format{protocol.Messages}, plan: &unitPrice},
		}},
	}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, full)
	same(t, "the endpoints of the first read", c.column("endpoints", "upstream_name || ' ' || cost_state", "upstream_name"),
		[]string{"claude-sonnet-4-5-20250929 known", "gpt-4o known"})
	var seen time.Time
	c.value(&seen, "SELECT first_seen_at FROM suppliers.supplier_models WHERE supplier_id = $1 AND upstream_name = 'gpt-4o'", c.supplier)
	schedules := c.count("cost_schedules")

	// The supplier drops both models, though it still has the groups.
	c.commitListing(s, 2, []listedGroup{
		{ref: "default", name: "Default", ratio: "1"},
		{ref: "legacy", name: "Legacy", ratio: "1"},
	})
	same(t, "what the tables hold after the listing shrank", []string{
		strings.Join(c.column("supplier_groups", "upstream_ref || ' ' || present::text", "upstream_ref"), ", "),
		strings.Join(c.column("supplier_models", "upstream_name || ' ' || present::text", "upstream_name"), ", "),
		strings.Join(c.column("endpoints", "upstream_name || ' ' || cost_state || ' ' || coalesce(cost_state_reason, '-')", "upstream_name"), ", "),
	}, []string{
		"default true, legacy true",
		"claude-sonnet-4-5-20250929 false, gpt-4o false",
		"claude-sonnet-4-5-20250929 unknown the listing no longer offers the model, gpt-4o unknown the listing no longer offers the model",
	})
	if got := c.count("cost_schedules"); got != schedules {
		t.Errorf("%d cost schedules after the listing shrank, want %d: a schedule is never dropped", got, schedules)
	}

	// Both come back, at the price they had.
	c.commitListing(s, 3, full)
	same(t, "what the tables hold when the listing has them again", []string{
		strings.Join(c.column("supplier_groups", "upstream_ref || ' ' || present::text", "upstream_ref"), ", "),
		strings.Join(c.column("supplier_models", "upstream_name || ' ' || present::text", "upstream_name"), ", "),
		strings.Join(c.column("endpoints", "upstream_name || ' ' || cost_state || ' ' || enabled::text", "upstream_name"), ", "),
	}, []string{
		"default true, legacy true",
		"claude-sonnet-4-5-20250929 true, gpt-4o true",
		"claude-sonnet-4-5-20250929 known true, gpt-4o known true",
	})
	var again time.Time
	c.value(&again, "SELECT first_seen_at FROM suppliers.supplier_models WHERE supplier_id = $1 AND upstream_name = 'gpt-4o'", c.supplier)
	if !again.Equal(seen) {
		t.Errorf("the name waited to be mapped from %s, and came back at %s: it is not new", seen, again)
	}
	if got := c.count("cost_schedules"); got != schedules {
		t.Errorf("%d cost schedules after the model came back, want %d: the plan is the one the endpoint had", got, schedules)
	}
}

// TestAListingTheDatabaseRefusesWritesNothing: a projection that fails halfway leaves none of its rows, because it
// writes in the step's own transaction.
func TestAListingTheDatabaseRefusesWritesNothing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-4o", catalog.StatusListed)
	s := c.acquire(c.node("a"))
	// The second model asks for a call format the store has no name for, which the projection cannot write: the group is
	// written before it fails, and must be gone with it.
	bad := listing(1, []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		{name: "unknown-format", formats: []protocol.Format{7}, plan: &unitPrice},
	}}})
	if err := s.Store().Commit(t.Context(), 0, bad); err == nil {
		t.Fatal("a listing with a format the store has no name to write was written")
	}
	if got := [3]int{c.count("supplier_groups"), c.count("supplier_models"), c.count("endpoints")}; got != [3]int{0, 0, 0} {
		t.Errorf("the refused step left %d groups, %d models and %d endpoints", got[0], got[1], got[2])
	}
}

// TestAGroupNothingCanBeCalledFromIsNotListed: a group the supplier will not sell us — a subscription group, one priced
// by the hour — still appears in the listing, with the names of its models and no call format. Those names are not rows
// of the supplier's models (a row holds at least one format) and have no endpoint; a name that was a row goes away
// without being deleted, so an alias of it is not removed either, and comes back with its endpoint when the group is
// callable again — the reason the projection itself wrote is one it lifts.
func TestAGroupNothingCanBeCalledFromIsNotListed(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-4o", catalog.StatusListed)
	sellable := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, sellable)

	// The same group is now sold by subscription: the supplier lists the name with no call format.
	unserved := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", plan: &unitPrice},
	}}}
	c.commitListing(s, 2, unserved)
	same(t, "what the tables hold for a group nothing can be called from", []string{
		strings.Join(c.column("supplier_models", "upstream_name || ' ' || present::text", "upstream_name"), ", "),
		strings.Join(c.column("endpoints", "upstream_name || ' ' || cost_state || ' ' || coalesce(cost_state_reason, '-')", "upstream_name"), ", "),
	}, []string{
		"gpt-4o false",
		"gpt-4o unknown no call format is served here",
	})

	// The group is callable again, at the price it had: the endpoint is the same row and routable again, though the
	// projection was the one that took it down.
	c.commitListing(s, 3, sellable)
	same(t, "what the tables hold when the group is callable again", []string{
		strings.Join(c.column("supplier_models", "upstream_name || ' ' || present::text", "upstream_name"), ", "),
		strings.Join(c.column("endpoints", "upstream_name || ' ' || cost_state || ' ' || coalesce(cost_state_reason, '-')", "upstream_name"), ", "),
	}, []string{
		"gpt-4o true",
		"gpt-4o known -",
	})
}
