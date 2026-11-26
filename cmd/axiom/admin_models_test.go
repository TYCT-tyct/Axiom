package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// TestModelFamilyThroughTheAPI: a family is set and cleared as of the rev the page read, the model answers with it and
// a rev one higher, a retry with the key is the first answer, and a stale rev, a malformed family and a session without
// catalog.manage are refused.
func TestModelFamilyThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	m := f.create("acme/alpha-2")
	f.addUser("nobody@example.com", "")
	nobody := f.verified("nobody@example.com")
	key := newKey()
	body := func(family string, rev int64) string {
		return `{"family":` + family + `,"rev":` + strconv.FormatInt(rev, 10) + `,"reason":"group the alphas"}`
	}
	var set adminapi.Model
	for _, c := range []struct {
		name   string
		s      session
		key    string
		body   string
		status int
		code   errorCode
		family *string
	}{
		{"set", f.s, key, body(`"alpha"`, m.Rev), http.StatusOK, "", ptr("alpha")},
		{"replay", f.s, key, body(`"alpha"`, m.Rev), http.StatusOK, "", ptr("alpha")},
		{"stale", f.s, newKey(), body(`"beta"`, m.Rev), http.StatusConflict, codeStaleRevision, nil},
		{"malformed", f.s, newKey(), body(`"Alpha Family"`, m.Rev+1), http.StatusBadRequest, codeInvalidRequest, nil},
		{"no permission", nobody, newKey(), body(`"beta"`, m.Rev+1), http.StatusForbidden, codeForbidden, nil},
		{"cleared", f.s, newKey(), body(`null`, m.Rev+1), http.StatusOK, "", nil},
	} {
		rec := f.change(c.s, http.MethodPut, "/models/"+m.Id+"/family", c.body, c.key)
		expectAdmin(t, c.name, rec, c.status, c.code)
		if c.status != http.StatusOK {
			continue
		}
		var got adminapi.Model
		decode(t, rec, &got)
		if !cmp.Equal(got.Family, c.family) {
			t.Errorf("%s: family %v, want %v", c.name, deref(got.Family), deref(c.family))
		}
		switch c.name {
		case "set":
			set = got
			if got.Rev != m.Rev+1 {
				t.Errorf("set: rev %d, want %d", got.Rev, m.Rev+1)
			}
		case "replay":
			if diff := cmp.Diff(set, got); diff != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
				t.Errorf("the replay (-want +got):\n%s", diff)
			}
		}
	}
	if n := f.auditCount("model.set_family"); n != "2" {
		t.Errorf("%s model.set_family audit entries, want 2: the set and the clearing", n)
	}
}

func ptr[T any](v T) *T { return &v }

// TestPutModelsOnSaleThroughTheAPI: each model of a batch passes the listing gate on its own, at the rev the page read,
// and its outcome is in the order sent; a retry answers the first outcomes without listing again; a model that is not
// there makes the request bad, and the permission is checked.
func TestPutModelsOnSaleThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	onSale := f.ready("acme/on-sale")
	if rec := f.setStatus(onSale, "listed"); rec.Code != http.StatusOK {
		t.Fatalf("list on-sale: %d %s", rec.Code, rec.Body)
	}
	onSale = f.send(http.MethodGet, "/models/"+onSale.Id, "", http.StatusOK)
	ready := f.ready("acme/ready")
	bare := f.create("acme/bare") // no public name, no price
	stale := f.ready("acme/stale")
	entry := func(m adminapi.Model, rev int64) string {
		return `{"id":"` + m.Id + `","rev":` + strconv.FormatInt(rev, 10) + `}`
	}
	batch := `{"models":[` + entry(onSale, onSale.Rev) + `,` + entry(ready, ready.Rev) + `,` + entry(bare, bare.Rev) + `,` +
		entry(stale, stale.Rev+1) + `],"reason":"the acme family"}`
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/models/listings", batch, key)
	expectAdmin(t, "putModelsOnSale", rec, http.StatusOK, "")
	var result adminapi.ModelListingResult
	decode(t, rec, &result)
	want := []adminapi.ModelListingResultDataOutcome{"not_draft", "listed", "not_ready", "stale"}
	for i, m := range []adminapi.Model{onSale, ready, bare, stale} {
		if i >= len(result.Data) || result.Data[i].ModelId != m.Id || result.Data[i].Outcome != want[i] {
			t.Fatalf("the outcomes %+v, want %v in the order sent", result.Data, want)
		}
		if listed := result.Data[i].Model != nil; listed != (want[i] == "listed") {
			t.Errorf("%s: a model in the outcome is %v", m.Slug, listed)
		}
	}
	if got := result.Data[1].Model; got.Status != adminapi.ModelStatusListed || got.Rev != ready.Rev+1 ||
		got.BillingUnits == nil || !cmp.Equal(*got.BillingUnits, []adminapi.BillingUnit{adminapi.BillingUnitToken}) {
		t.Errorf("the listed model %+v", got)
	}
	again := f.change(f.s, http.MethodPost, "/models/listings", batch, key)
	var replayed adminapi.ModelListingResult
	decode(t, again, &replayed)
	if diff := cmp.Diff(result, replayed); diff != "" || again.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-want +got):\n%s", diff)
	}
	if n := f.auditCount("model.list"); n != "2" {
		t.Errorf("%s model.list audit entries, want 2: on-sale alone and ready in the batch", n)
	}
	missing := `{"models":[{"id":"0198c0a8-0000-7000-8000-00000000dead","rev":0}],"reason":"r"}`
	expectAdmin(t, "a model that is not there", f.change(f.s, http.MethodPost, "/models/listings", missing, newKey()), http.StatusBadRequest, codeInvalidRequest)
	f.addUser("nobody@example.com", "")
	expectAdmin(t, "no permission", f.change(f.verified("nobody@example.com"), http.MethodPost, "/models/listings", batch, newKey()), http.StatusForbidden, codeForbidden)
}

// litellmPrices is a LiteLLM price table: a model the catalog has, a dated and an undated model it lacks, one of an
// author it lacks and one whose prices change with the prompt length, which a draft cannot take without a basis.
const litellmPrices = `{
	"sample_spec": {"litellm_provider": "one of https://docs.litellm.ai/docs/providers", "mode": "one of: chat, embedding"},
	"gpt-known": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06},
	"gpt-9": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 4e-06},
	"gpt-9-2026-01-01": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 4e-06},
	"claude-x": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05},
	"gpt-long": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06,
		"input_cost_per_token_above_200k_tokens": 2e-06}
}`

// TestImportModelsThroughTheAPI: an import adds a draft for each entry the catalog does not know, with the entry's
// names, a family from its name, the dataset as its source and its price, holds what it cannot use, and leaves what it
// knows; a retry is the first answer without reading the list again, and a second import adds nothing.
func TestImportModelsThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	known := f.create("openai/gpt-known")
	f.addName(known, "gpt-known")
	expectAdmin(t, "a node that reads no price list", f.change(f.s, http.MethodPost, "/models/imports", `{"source":"litellm","reason":"seed"}`, newKey()), http.StatusServiceUnavailable, codeUnavailable)
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(litellmPrices))
	}))
	t.Cleanup(srv.Close)
	f.adminFixture = f.withAPI(func(api *adminAPI) {
		api.datasets = catalog.NewTestFetcher(t, egress.NewForTesting(egresstest.Loopback(t)), map[catalog.PriceSource]string{catalog.SourceLiteLLM: srv.URL})
	})
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/models/imports", `{"source":"litellm","reason":"seed"}`, key)
	expectAdmin(t, "importModels", rec, http.StatusCreated, "")
	var first adminapi.ModelImport
	decode(t, rec, &first)
	want := adminapi.ModelImport{Source: "litellm", Added: 2, Known: 1, Held: 2, HeldNames: &[]string{"claude-x", "gpt-long"}}
	if diff := cmp.Diff(want, first); diff != "" {
		t.Errorf("the import (-want +got):\n%s", diff)
	}
	again := f.change(f.s, http.MethodPost, "/models/imports", `{"source":"litellm","reason":"seed"}`, key)
	var replayed adminapi.ModelImport
	decode(t, again, &replayed)
	if diff := cmp.Diff(first, replayed); diff != "" || again.Header().Get("Idempotency-Replayed") != "true" || reads.Load() != 1 {
		t.Errorf("the replay read the list %d times (-want +got):\n%s", reads.Load(), diff)
	}
	var drafts adminapi.ModelList
	decode(t, f.do(http.MethodGet, "/models?status=draft&slug_prefix=openai/gpt-9", "", as(f.s)), &drafts)
	if len(drafts.Data) != 2 {
		t.Fatalf("the drafts %+v, want openai/gpt-9 and openai/gpt-9-2026-01-01", drafts.Data)
	}
	for _, d := range drafts.Data {
		if d.PriceSource != adminapi.PriceSourceLitellm || deref(d.Family) != "gpt-9" {
			t.Errorf("the draft %s: source %s, family %v", d.Slug, d.PriceSource, deref(d.Family))
		}
	}
	dated := f.send(http.MethodGet, "/models/"+drafts.Data[1].Id, "", http.StatusOK)
	wantNames := []adminapi.ModelName{{Name: "gpt-9-2026-01-01", Visibility: "public"}, {Name: "openai/gpt-9-2026-01-01", Visibility: "public"}}
	if diff := cmp.Diff(wantNames, dated.Names); diff != "" || dated.VersionBasis != adminapi.Dated ||
		len(dated.Prices) != 1 || dated.Prices[0].Source != adminapi.PriceSourceLitellm || dated.Prices[0].Schedule == nil {
		t.Errorf("the dated draft %+v (names -want +got):\n%s", dated, diff)
	}
	rec = f.change(f.s, http.MethodPost, "/models/imports", `{"source":"litellm","reason":"seed again"}`, newKey())
	var second adminapi.ModelImport
	decode(t, rec, &second)
	if second.Added != 0 || second.Known != 3 || second.Held != 2 {
		t.Errorf("the second import %+v, want nothing added and the three known", second)
	}
	expectAdmin(t, "a source that is none", f.change(f.s, http.MethodPost, "/models/imports", `{"source":"official","reason":"r"}`, newKey()), http.StatusBadRequest, codeInvalidRequest)
	f.addUser("nobody@example.com", "")
	expectAdmin(t, "no permission", f.change(f.verified("nobody@example.com"), http.MethodPost, "/models/imports", `{"source":"litellm","reason":"r"}`, newKey()), http.StatusForbidden, codeForbidden)
}
