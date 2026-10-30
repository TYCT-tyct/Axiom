package main

import (
	"crypto/sha256"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

const (
	usd      = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`
	cny      = `{"v":1,"currency":"CNY","rates":{"input":"0.000007","output":"0.000014"}}`
	proof    = `{"url":"https://example.com/pricing","quote":"input 1 per million"}`
	specBody = `{"items":["input","output"],"tiers":[],"context_length":128000}`
)

// catalogFixture is the admin with an operator who holds the admin role, to work on the catalog through the API.
type catalogFixture struct {
	adminFixture
	s session
}

func newCatalogFixture(t *testing.T) catalogFixture {
	t.Helper()
	f := newAdminFixture(t)
	return catalogFixture{f, f.owner()}
}

// send makes a change of the catalog with a fresh key and returns the model it answers with.
func (f catalogFixture) send(method, path, body string, status int) adminapi.Model {
	f.t.Helper()
	rec := f.change(f.s, method, path, body, newKey())
	expectAdmin(f.t, method+" "+path, rec, status, "")
	var model adminapi.Model
	decode(f.t, rec, &model)
	return model
}

func (f catalogFixture) saveAuthor(slug string) {
	f.t.Helper()
	rec := f.change(f.s, http.MethodPut, "/authors/"+slug, `{"name":"`+strings.ToUpper(slug)+`","reason":"a vendor"}`, newKey())
	expectAdmin(f.t, "saveAuthor", rec, http.StatusOK, "")
}

// create drafts a model of the slug, whose author is made first.
func (f catalogFixture) create(slug string) adminapi.Model {
	f.t.Helper()
	f.saveAuthor(strings.Split(slug, "/")[0])
	return f.send(http.MethodPost, "/models", `{"slug":"`+slug+`","version_basis":"dated","price_source":"manual","spec":`+specBody+`,"reason":"a new model"}`, http.StatusCreated)
}

// addName gives a model a public name, the only visibility an admin call may set here.
func (f catalogFixture) addName(m adminapi.Model, name string) adminapi.Model {
	f.t.Helper()
	return f.send(http.MethodPost, "/models/"+m.Id+"/names", `{"name":"`+name+`","visibility":"public","reason":"its name"}`, http.StatusOK)
}

func (f catalogFixture) publish(m adminapi.Model, schedule string, base *adminapi.Uuid) adminapi.Model {
	f.t.Helper()
	baseText := ""
	if base != nil {
		baseText = `"base":"` + *base + `",`
	}
	return f.send(http.MethodPost, "/models/"+m.Id+"/prices", `{`+baseText+`"source":"manual","schedule":`+schedule+`,"evidence":`+proof+`,"reason":"the price"}`, http.StatusOK)
}

// officialProof is the current evidence a page watch would have left, without reaching a live vendor in API tests.
func (f catalogFixture) officialProof(author, currency string) string {
	f.t.Helper()
	ctx := f.t.Context()
	var page, snapshot uuid.UUID
	const text = "input 1 per million output 2 per million"
	digest := sha256.Sum256([]byte(text))
	err := f.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		q := catalog.New()
		if err := q.RegisterPageSite(ctx, tx, catalog.RegisterPageSiteParams{Origin: "https://example.com", DelaySeconds: 10}); err != nil {
			return err
		}
		var err error
		page, err = q.InsertWatchedPage(ctx, tx, catalog.InsertWatchedPageParams{Author: author, Url: "https://example.com/" + uuid.NewV7().String(), Origin: "https://example.com", Currency: currency, SectionStart: "Start", SectionEnd: "End", IntervalSeconds: 3600})
		if err != nil {
			return err
		}
		snapshot, err = q.InsertPageSnapshot(ctx, tx, catalog.InsertPageSnapshotParams{PageID: page, ExtractorVersion: 1, Text: text, TextSha256: digest[:]})
		if err != nil {
			return err
		}
		return q.RecordPageSuccess(ctx, tx, catalog.RecordPageSuccessParams{ID: page, CurrentSnapshotID: &snapshot, Seconds: 3600})
	})
	if err != nil {
		f.t.Fatal(err)
	}
	body, err := jsonx.MarshalCanonical(catalog.PageEvidence{PageID: page, SnapshotID: snapshot, Excerpts: []string{"input 1 per million"}})
	if err != nil {
		f.t.Fatal(err)
	}
	return string(body)
}

// ready drafts a model that can be put on sale: its standard ID as a public name and a USD price.
func (f catalogFixture) ready(slug string) adminapi.Model {
	f.t.Helper()
	return f.publish(f.addName(f.create(slug), slug), usd, nil)
}

func (f catalogFixture) setStatus(m adminapi.Model, status string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPut, "/models/"+m.Id+"/status", `{"status":"`+status+`","rev":`+strconv.FormatInt(m.Rev, 10)+`,"reason":"review"}`, newKey())
}

// TestAuthorsThroughTheAPI: an author is made by its slug and renamed by it, listed by slug, and a slug that is none is
// a 400.
func TestAuthorsThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	for _, c := range []struct{ slug, name string }{{"zeta", "Zeta"}, {"acme", "Acme"}, {"acme", "Acme Corp"}} {
		rec := f.change(f.s, http.MethodPut, "/authors/"+c.slug, `{"name":"`+c.name+`","reason":"the vendor"}`, newKey())
		expectAdmin(t, "saveAuthor "+c.slug, rec, http.StatusOK, "")
		var author adminapi.Author
		decode(t, rec, &author)
		if author != (adminapi.Author{Slug: c.slug, Name: c.name}) {
			t.Errorf("saveAuthor %s answered %+v", c.slug, author)
		}
	}
	rec := f.do(http.MethodGet, "/authors", "", as(f.s))
	expectAdmin(t, "listAuthors", rec, http.StatusOK, "")
	var list adminapi.AuthorList
	decode(t, rec, &list)
	if want := []adminapi.Author{{Slug: "acme", Name: "Acme Corp"}, {Slug: "zeta", Name: "Zeta"}}; !slices.Equal(list.Data, want) {
		t.Errorf("the authors: %v, want %v", list.Data, want)
	}
	for _, slug := range []string{"Acme", "-acme", "a_b", strings.Repeat("a", 41)} {
		expectAdmin(t, "saveAuthor "+slug, f.change(f.s, http.MethodPut, "/authors/"+slug, `{"name":"X","reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	}
	if f.auditCount("author.save") != "3" {
		t.Errorf("%s audit entries of author.save, want 3", f.auditCount("author.save"))
	}
}

// isUSDBasic is whether the price is the USD schedule of the tests, input at one millionth.
func isUSDBasic(p adminapi.ReferencePrice) bool {
	return p.Currency == "USD" && p.Schedule != nil && p.Schedule.Rates.Input != nil && *p.Schedule.Rates.Input == "0.000001"
}

// TestFreeModelThroughTheAPI: the contract admits the slug the database admits, and the model read back carries it.
func TestFreeModelThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	base := f.ready("acme/alpha")
	expectAdmin(t, "list the base", f.setStatus(base, "listed"), http.StatusOK, "")
	free := f.create("acme/alpha:free")
	free = f.addName(free, "acme/alpha:free")
	free = f.publish(free, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`, nil)
	free = f.send(http.MethodPut, "/models/"+free.Id+"/status", `{"status":"listed","rev":`+strconv.FormatInt(free.Rev, 10)+`,"reason":"free"}`, http.StatusOK)
	if free.Slug != "acme/alpha:free" {
		t.Errorf("the created free model has slug %q", free.Slug)
	}
	rec := f.do(http.MethodGet, "/models/"+free.Id, "", as(f.s))
	expectAdmin(t, "get the free model", rec, http.StatusOK, "")
	var got adminapi.Model
	decode(t, rec, &got)
	if got.Slug != "acme/alpha:free" {
		t.Errorf("the API read the free model as %q", got.Slug)
	}
}

// TestModelDraft: a model is drafted, gets its name and its price, each change answered with the model as it is.
func TestModelDraft(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	draft := f.create("acme/alpha")
	if draft.Status != "draft" || draft.Rev != 0 || draft.Slug != "acme/alpha" || draft.Author != "acme" || len(draft.Names) != 0 || len(draft.Prices) != 0 {
		t.Fatalf("the draft: %+v", draft)
	}
	ready := f.ready("acme/beta")
	if len(ready.Names) != 1 || ready.Names[0] != (adminapi.ModelName{Name: "acme/beta", Visibility: "public"}) || len(ready.Prices) != 1 {
		t.Fatalf("the model with its name and price: %+v", ready)
	}
	if !isUSDBasic(ready.Prices[0]) {
		t.Errorf("its price: %+v", ready.Prices[0])
	}
}

// onSale puts a model on sale and then takes it out of use, and returns the model after each step.
func (f catalogFixture) onSale() (listed, retired adminapi.Model) {
	f.t.Helper()
	rec := f.setStatus(f.ready("acme/beta"), "listed")
	expectAdmin(f.t, "listing", rec, http.StatusOK, "")
	decode(f.t, rec, &listed)
	rec = f.setStatus(listed, "retired")
	expectAdmin(f.t, "retiring", rec, http.StatusOK, "")
	decode(f.t, rec, &retired)
	return listed, retired
}

// TestModelOnSale: a model is put on sale and taken out of use, each change answered with the model as it is and its rev
// one more.
func TestModelOnSale(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	listed, retired := f.onSale()
	if listed.Status != "listed" || listed.Rev != 1 || retired.Status != "retired" || retired.Rev != 2 {
		t.Errorf("on sale: status %q rev %d; out of use: status %q rev %d", listed.Status, listed.Rev, retired.Status, retired.Rev)
	}
}

// TestCatalogChangesAreAudited: every change is audited once, with the reason and who made it.
func TestCatalogChangesAreAudited(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	f.create("acme/alpha")
	listed, _ := f.onSale()
	for action, n := range map[string]string{"author.save": "2", "model.create": "2", "name.add": "1", "price.publish": "1", "model.list": "1", "model.retire": "1"} {
		if got := f.auditCount(action); got != n {
			t.Errorf("%s audit entries of %s, want %s", got, action, n)
		}
	}
	f.expectAudit("model.retire", map[string]string{
		"actor_kind || ':' || actor_id": "user:" + f.text("SELECT id::text FROM accounts.users WHERE email = '"+ownerEmail+"'"),
		"reason":                        "review", "module": "catalog", "target_kind": "model", "target_id": listed.Id,
	})
}

// TestModelRefusals: a change the catalog refuses is the contract's answer for it and changes nothing.
func TestModelRefusals(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	m := f.create("acme/alpha")
	f.addName(m, "acme/alpha")
	official := f.officialProof("acme", "USD")
	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     errorCode
	}{
		{"a slug taken", http.MethodPost, "/models", `{"slug":"acme/alpha","version_basis":"dated","price_source":"manual","spec":` + specBody + `,"reason":"r"}`, http.StatusConflict, codeConflict},
		{"an author that is not there", http.MethodPost, "/models", `{"slug":"nobody/x","version_basis":"dated","price_source":"manual","spec":` + specBody + `,"reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a spec that names no item", http.MethodPost, "/models", `{"slug":"acme/y","version_basis":"dated","price_source":"manual","spec":{"items":["telepathy"],"tiers":[]},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"a name taken", http.MethodPost, "/models/" + m.Id + "/names", `{"name":"ACME/Alpha","visibility":"public","reason":"r"}`, http.StatusConflict, codeNameTaken},
		{"listing without a price", http.MethodPut, "/models/" + m.Id + "/status", `{"status":"listed","rev":0,"reason":"r"}`, http.StatusUnprocessableEntity, codeNotReady},
		{"retiring a draft", http.MethodPut, "/models/" + m.Id + "/status", `{"status":"retired","rev":0,"reason":"r"}`, http.StatusConflict, codeConflict},
		{"a stale spec", http.MethodPut, "/models/" + m.Id + "/spec", `{"rev":9,"spec":` + specBody + `,"reason":"r"}`, http.StatusConflict, codeStaleRevision},
		{"a price of another source", http.MethodPost, "/models/" + m.Id + "/prices", `{"source":"official","schedule":` + usd + `,"evidence":` + official + `,"reason":"r"}`, http.StatusConflict, codeConflict},
		{"a price on a base that is not the head", http.MethodPost, "/models/" + m.Id + "/prices", `{"base":"0198c0a8-0000-7000-8000-000000000009","source":"manual","schedule":` + usd + `,"evidence":` + proof + `,"reason":"r"}`, http.StatusConflict, codeStaleRevision},
		{"a model that is not there", http.MethodPost, "/models/0198c0a8-0000-7000-8000-000000000009/names", `{"name":"x/y","visibility":"public","reason":"r"}`, http.StatusNotFound, codeNotFound},
		{"an ID that is none", http.MethodPost, "/models/nonsense/names", `{"name":"x/y","visibility":"public","reason":"r"}`, http.StatusNotFound, codeNotFound},
	} {
		expectAdmin(t, c.name, f.change(f.s, c.method, c.path, c.body, newKey()), c.status, c.code)
	}
	if got := f.text("SELECT count(*)::text FROM catalog.model"); got != "1" {
		t.Errorf("%s models after the refusals, want 1", got)
	}
}

// TestPriceRules: a version is published on the head it was decided on, a schedule that loses a price is refused, and
// the history is a chain of versions, the head being the current price.
func TestPriceRules(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	m := f.ready("acme/alpha")
	first := m.Prices[0].Id
	cheaper := `{"v":1,"currency":"USD","rates":{"input":"0.0000005","output":"0.000002"}}`
	second := f.publish(m, cheaper, &first)
	if len(second.Prices) != 1 || second.Prices[0].Id == first || *second.Prices[0].Schedule.Rates.Input != "0.0000005" {
		t.Errorf("after the second version: %+v", second.Prices)
	}
	expectAdmin(t, "the first as the base again", f.change(f.s, http.MethodPost, "/models/"+m.Id+"/prices",
		`{"base":"`+first+`","source":"manual","schedule":`+usd+`,"evidence":`+proof+`,"reason":"r"}`, newKey()), http.StatusConflict, codeStaleRevision)
	lossy := `{"v":1,"currency":"USD","rates":{"input":"0.0000005"}}`
	head := second.Prices[0].Id
	expectAdmin(t, "a schedule that loses the output price", f.change(f.s, http.MethodPost, "/models/"+m.Id+"/prices",
		`{"base":"`+head+`","source":"manual","schedule":`+lossy+`,"evidence":`+proof+`,"reason":"r"}`, newKey()), http.StatusUnprocessableEntity, codeUnprocessable)
	withCNY := f.publish(second, cny, nil)
	if len(withCNY.Prices) != 2 || withCNY.Prices[0].Currency != "CNY" || withCNY.Prices[1].Currency != "USD" {
		t.Errorf("a price in another currency: %+v", withCNY.Prices)
	}
	if got := f.text("SELECT count(*)::text FROM catalog.reference_price"); got != "3" {
		t.Errorf("%s price rows, want 3: the history is never rewritten", got)
	}
}

// TestSelectPriceSource: switching a model to another source publishes the new source's price in each currency and
// withdraws the rest, in one change.
func TestSelectPriceSource(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	m := f.publish(f.ready("acme/alpha"), cny, nil)
	bases := `{"CNY":"` + m.Prices[0].Id + `","USD":"` + m.Prices[1].Id + `"}`
	official := f.officialProof("acme", "USD")
	body := `{"rev":` + strconv.FormatInt(m.Rev, 10) + `,"source":"official","bases":` + bases + `,"prices":[{"schedule":` + usd + `,"evidence":` + official + `}],"reason":"the vendor's page"}`
	switched := f.send(http.MethodPut, "/models/"+m.Id+"/price-source", body, http.StatusOK)
	if switched.PriceSource != "official" || len(switched.Prices) != 2 || switched.Prices[0].Currency != "CNY" || switched.Prices[0].Schedule != nil ||
		switched.Prices[1].Currency != "USD" || switched.Prices[1].Schedule == nil || switched.Prices[1].Source != "official" || switched.Rev != m.Rev+1 {
		t.Errorf("after the switch: %+v", switched)
	}
	expectAdmin(t, "the same switch on stale bases", f.change(f.s, http.MethodPut, "/models/"+m.Id+"/price-source", body, newKey()), http.StatusConflict, codeStaleRevision)
}

// TestModelPages: models by slug in pages, drafts included, filtered; a cursor this API did not give out is a 400, and
// a model is read by its ID.
func TestModelPages(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	for _, slug := range []string{"acme/alpha", "acme/beta", "zeta/gamma"} {
		f.create(slug)
	}
	var slugs []string
	path := "/models?limit=2"
	for range 5 {
		rec := f.do(http.MethodGet, path, "", as(f.s))
		expectAdmin(t, "listModels", rec, http.StatusOK, "")
		var list adminapi.ModelList
		decode(t, rec, &list)
		for _, m := range list.Data {
			slugs = append(slugs, m.Slug)
		}
		if list.NextCursor == nil {
			break
		}
		path = "/models?limit=2&cursor=" + *list.NextCursor
	}
	if want := []string{"acme/alpha", "acme/beta", "zeta/gamma"}; !slices.Equal(slugs, want) {
		t.Errorf("the models in pages of 2: %v, want %v", slugs, want)
	}
	for query, want := range map[string]int{"status=draft": 3, "status=listed": 0, "author=zeta": 1, "slug_prefix=acme/": 2, "slug_prefix=%25": 0} {
		rec := f.do(http.MethodGet, "/models?"+query, "", as(f.s))
		var list adminapi.ModelList
		decode(t, rec, &list)
		if len(list.Data) != want {
			t.Errorf("listModels?%s: %d models, want %d", query, len(list.Data), want)
		}
	}
	expectAdmin(t, "a cursor that is none", f.do(http.MethodGet, "/models?cursor=***", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)
	created := f.create("acme/gamma")
	rec := f.do(http.MethodGet, "/models/"+created.Id, "", as(f.s))
	var read adminapi.Model
	decode(t, rec, &read)
	if diff := cmp.Diff(created, read); diff != "" {
		t.Errorf("getModel is the model as created (-want +got):\n%s", diff)
	}
	expectAdmin(t, "getModel of none", f.do(http.MethodGet, "/models/"+uuid.NewV7().String(), "", as(f.s)), http.StatusNotFound, codeNotFound)
}

// TestCatalogKeys: the Idempotency-Key claims one request: the same request again answers with the first response, even
// after the model changed, and does nothing; the key of another kind of change, of another model or of another body is
// refused, and so is the all-zero key, before anything is written.
func TestCatalogKeys(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	key := newKey()
	body := `{"slug":"acme/alpha","version_basis":"dated","price_source":"manual","spec":` + specBody + `,"reason":"r"}`
	f.saveAuthor("acme")
	rec := f.change(f.s, http.MethodPost, "/models", body, key)
	expectAdmin(t, "createModel", rec, http.StatusCreated, "")
	var first, again adminapi.Model
	decode(t, rec, &first)
	f.addName(first, "acme/alpha") // the model moves on; the replay is still the first response
	rec = f.change(f.s, http.MethodPost, "/models", body, key)
	expectAdmin(t, "createModel again", rec, http.StatusCreated, "")
	decode(t, rec, &again)
	if diff := cmp.Diff(first, again); diff != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-want +got) replayed=%q:\n%s", rec.Header().Get("Idempotency-Replayed"), diff)
	}
	if got := f.text("SELECT count(*)::text FROM catalog.model"); got != "1" {
		t.Errorf("%s models, want 1", got)
	}
	expectAdmin(t, "the key for another kind of change", f.change(f.s, http.MethodPut, "/authors/acme", `{"name":"Acme","reason":"r"}`, key),
		http.StatusConflict, codeIdempotencyReuse)
	expectAdmin(t, "the key with another body", f.change(f.s, http.MethodPost, "/models", strings.Replace(body, "alpha", "beta", 1), key),
		http.StatusConflict, codeIdempotencyReuse)

	other := f.create("acme/beta")
	specKey := newKey()
	spec := func(m adminapi.Model) string {
		return `{"rev":` + strconv.FormatInt(m.Rev, 10) + `,"spec":` + specBody + `,"reason":"r"}`
	}
	m := first
	expectAdmin(t, "the spec of one model", f.change(f.s, http.MethodPut, "/models/"+m.Id+"/spec", spec(m), specKey), http.StatusOK, "")
	expectAdmin(t, "the same key for another model", f.change(f.s, http.MethodPut, "/models/"+other.Id+"/spec", spec(other), specKey),
		http.StatusConflict, codeIdempotencyReuse)

	audits := f.auditCount("model.create")
	rec = f.change(f.s, http.MethodPost, "/models", strings.Replace(body, "alpha", "gamma", 1), "00000000-0000-0000-0000-000000000000")
	expectAdmin(t, "the all-zero key", rec, http.StatusBadRequest, codeInvalidRequest)
	if fields := errorDetailOf(t, rec).Fields; fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != "Idempotency-Key" {
		t.Errorf("the all-zero key: fields %v, want Idempotency-Key", fields)
	}
	if got := f.text("SELECT count(*)::text FROM catalog.model"); got != "2" || f.auditCount("model.create") != audits {
		t.Errorf("the all-zero key wrote: %s models, audits %s -> %s", got, audits, f.auditCount("model.create"))
	}
}

// TestCatalogKeyUsedByTheCommandLine: a key the command line already used for a change of one model is refused for the
// same kind of change of another, which stays as it was.
func TestCatalogKeyUsedByTheCommandLine(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	a, b := f.create("acme/alpha"), f.create("acme/beta")
	key := newKey()
	cli, err := catalog.Open(f.db, pg.PlatformScope(), catalog.Config{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.UpdateSpec(t.Context(), catalog.UpdateSpecRequest{
		Actor: audit.System("catalog.cli"), Key: uuid.MustParse(key), Reason: "r", Model: catalog.ModelID(uuid.MustParse(a.Id)), Rev: a.Rev, Spec: jsonx.Raw(specBody),
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"rev":` + strconv.FormatInt(b.Rev, 10) + `,"spec":` + specBody + `,"reason":"r"}`
	expectAdmin(t, "the command line's key for another model", f.change(f.s, http.MethodPut, "/models/"+b.Id+"/spec", body, key),
		http.StatusConflict, codeIdempotencyReuse)
	if got := f.text("SELECT rev::text FROM catalog.model WHERE id = '" + b.Id + "'"); got != strconv.FormatInt(b.Rev, 10) {
		t.Errorf("the other model's rev is %s, want %d", got, b.Rev)
	}
}

// TestCatalogKeyRacing: the same request sent twice at once is one change, and both senders get its response.
func TestCatalogKeyRacing(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	f.saveAuthor("acme")
	key := newKey()
	body := `{"slug":"acme/alpha","version_basis":"dated","price_source":"manual","spec":` + specBody + `,"reason":"r"}`
	recs := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { recs <- f.change(f.s, http.MethodPost, "/models", body, key) }()
	}
	replays := 0
	for range 2 {
		rec := <-recs
		expectAdmin(t, "createModel at once", rec, http.StatusCreated, "")
		if rec.Header().Get("Idempotency-Replayed") == "true" {
			replays++
		}
	}
	if got := f.text("SELECT count(*)::text FROM catalog.model"); got != "1" || replays != 1 {
		t.Errorf("%s models and %d replays, want 1 and 1", got, replays)
	}
}
