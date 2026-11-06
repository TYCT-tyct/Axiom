package main

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// priceEnvelope is the main site's envelope in the tests: any margin, a model's prices moved by 25% a day, 5
// publications of 5 models a day.
const priceEnvelope = `{"min_margin_bps":0,"model_daily_change_bps":2500,"daily_changes":5,"daily_models":5}`

// pricedModel is the call world's model whose prices the tests publish.
const pricedModel = "anthropic/claude-opus-5-5"

// priceFixture is the call world, whose endpoints have costs and whose rule prices every model at 1.5 times its
// reference price, with an admin node, its owner, and an agent that may publish sale prices.
type priceFixture struct {
	supplierFixture
	w     *callWorld
	token string
}

func newPriceFixture(t *testing.T) priceFixture {
	t.Helper()
	w := newCallWorld(t)
	w.price()
	const owner = "rich@example.com"
	useFastPassword(t, w.database, owner)
	node := startAdminNode(w.commandLine, &testClock{now: time.Now()})
	if _, err := node.user("", "grant-platform", "--email", owner, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	f := priceFixture{supplierFixture: supplierFixture{node, node.verified(owner)}, w: w}
	f.reprice("1.5") // the first prices publish themselves
	_, f.token = f.makeCredential(f.s, `{"name":"hermes","permissions":["pricing.view","pricing.manage","operations.money"],
		"platform":true,"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"set prices"}`)
	return f
}

// reprice sets the rule's multiplier and lets the publisher propose from it, as its job would.
func (f priceFixture) reprice(multiplier string) {
	f.t.Helper()
	if err := pgtest.Run(f.t.Context(), f.w.database.Superuser, `UPDATE billing.pricing_rules SET method = jsonb_set(method, '{multiplier}', '"`+multiplier+`"')`); err != nil {
		f.t.Fatal(err)
	}
	site, _ := f.svc.MainSite()
	if err := f.w.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return refreshSalePrices(f.t.Context(), tx, f.w.site, site.Currency) }); err != nil {
		f.t.Fatal(err)
	}
}

// publication is the model's publication as the agent reads it.
func (f priceFixture) publication(model string) operationsapi.PricePublication {
	f.t.Helper()
	rec := f.agent(f.token, because("/prices", "what is waiting"))
	expectAdmin(f.t, "pricesList", rec, http.StatusOK, "")
	var list operationsapi.PricePublicationList
	decode(f.t, rec, &list)
	for _, p := range *list.Data {
		if *p.ModelId == model {
			return p
		}
	}
	f.t.Fatalf("no publication of %s in %s", model, rec.Body)
	return operationsapi.PricePublication{}
}

// waiting is the model's publication, which waits to be published.
func (f priceFixture) waiting(model string) operationsapi.PricePublication {
	f.t.Helper()
	p := f.publication(model)
	if *p.Status != operationsapi.PricePublicationStatusPending {
		f.t.Fatalf("the publication of %s is %s", model, *p.Status)
	}
	return p
}

func (f priceFixture) publish(model string, p operationsapi.PricePublication, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := `{"candidate_id":"` + *p.CandidateId + `"`
	if p.CurrentId != nil {
		body += `,"current_id":"` + *p.CurrentId + `"`
	}
	return f.agentSend(f.token, http.MethodPost, "/prices/"+model+"/publish", body+`,"reason":"the rule moved"`+extra+`}`, idempotent(key))
}

func (f priceFixture) setEnvelope(limits string, rev string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.change(f.s, http.MethodPut, "/operations/price-envelopes/"+f.siteID(), `{"limits":`+limits+`,"rev":`+rev+`,"reason":"the agent's room"}`, newKey())
}

// TestOperationsPricePublish: within the envelope the agent publishes a proposal at once, as itself, and it counts in
// the day; a retry finds it, a proposal read against an older head is stale, and a published one is not published
// again. Without an envelope nothing is published, and a preview writes nothing.
func TestOperationsPricePublish(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	model := f.w.models[pricedModel].String()
	f.reprice("1.8") // a 20% move, over the publisher's own 10%
	p := f.waiting(model)
	expectAdmin(t, "without an envelope", f.publish(model, p, newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	versions := func() string { return f.text(`SELECT count(*)::text FROM billing.price_versions`) }
	before := versions()
	rec := f.publish(model, p, newKey(), `,"dry_run":true`)
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != operationsapi.OperationStateApproved || versions() != before {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	key := newKey()
	rec = f.publish(model, p, key, "")
	done := agentOperation(t, rec)
	if rec.Code != http.StatusOK || done.State != operationsapi.OperationStateSucceeded || !strings.Contains(rec.Body.String(), `"change_bps":2000`) || versions() == before {
		t.Errorf("the publication: %d %s", rec.Code, rec.Body)
	}
	f.expectReplayed(model, p, key, *done.OperationId)
	expectAdmin(t, "an old head", f.publish(model, p, newKey(), ""), http.StatusConflict, codeStaleRevision)
	expectAdmin(t, "published already", f.publish(model, f.publication(model), newKey(), ""), http.StatusConflict, codeConflict)
	f.expectUse(2000)
}

// expectUse checks that caps_get shows one envelope with one publication of one model that moved by bps.
func (f priceFixture) expectUse(bps int64) {
	f.t.Helper()
	use := f.caps(f.token).PriceEnvelopes
	if use == nil || len(*use) != 1 || (*use)[0].Changes != 1 || (*use)[0].Models != 1 || len((*use)[0].Moves) != 1 || (*use)[0].Moves[0].ChangeBps != bps {
		f.t.Errorf("the envelope's use: %+v", use)
	}
}

// expectReplayed checks that the publication was audited as the agent's and that its retry under key finds it.
func (f priceFixture) expectReplayed(model string, p operationsapi.PricePublication, key, operation string) {
	f.t.Helper()
	if got := f.text(`SELECT actor_kind FROM platform.audit_log WHERE action = 'price.publish' ORDER BY id DESC LIMIT 1`); got != "operations_agent" {
		f.t.Errorf("published as %s", got)
	}
	if rec := f.publish(model, p, key, ""); rec.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(f.t, rec).OperationId != operation {
		f.t.Errorf("the retry: %d %s", rec.Code, rec.Body)
	}
}

// TestOperationsPriceApproval: a publication over the envelope waits for the owner, who is shown the envelope, and
// runs when approved, counting in the day; one whose proposal changed before the approval ends rejected.
func TestOperationsPriceApproval(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	model := f.w.models[pricedModel].String()
	expectAdmin(t, "the envelope", f.setEnvelope(`{"min_margin_bps":0,"model_daily_change_bps":1000,"daily_changes":5,"daily_models":5}`, "0"), http.StatusOK, "")
	f.reprice("1.8")
	rec := f.publish(model, f.waiting(model), newKey(), "")
	expectAdmin(t, "a 20% move", rec, http.StatusAccepted, "")
	waits := agentOperation(t, rec)
	rec = f.do(http.MethodGet, "/operations/approvals/"+waits.Approval.ApprovalId, "", as(f.s))
	var asked struct {
		Summary struct {
			Slug     string            `json:"slug"`
			Current  map[string]string `json:"current"`
			Proposed map[string]string `json:"proposed"`
			Cost     map[string]string `json:"cost"`
			Envelope struct {
				ModelDailyChangeBps int64 `json:"model_daily_change_bps"`
				ChangeBps           int64 `json:"change_bps"`
			} `json:"envelope"`
		} `json:"summary"`
	}
	decode(t, rec, &asked)
	summary := asked.Summary
	// The owner sees the model by name and each price now, as proposed and as it costs, beside the envelope: a 20%
	// move of the 1.5× price is the 1.8× one, so the input price is 1.2 times what it is now.
	if summary.Slug != pricedModel || summary.Envelope.ModelDailyChangeBps != 1000 || summary.Envelope.ChangeBps != 2000 ||
		summary.Cost["input"] == "" {
		t.Fatalf("the owner sees %s", rec.Body)
	}
	now, _ := new(big.Rat).SetString(summary.Current["input"])
	next, _ := new(big.Rat).SetString(summary.Proposed["input"])
	if now == nil || next == nil || next.Cmp(new(big.Rat).Mul(now, big.NewRat(6, 5))) != 0 {
		t.Errorf("the input price: %s now, %s proposed", summary.Current["input"], summary.Proposed["input"])
	}
	if approved := f.approve(waits.Approval.ApprovalId); approved.OperationState != adminapi.OperationStateSucceeded {
		t.Errorf("the approval: %+v", approved)
	}
	if got := f.text(`SELECT string_agg(count || ':' || change_bps, ' ' ORDER BY model_id NULLS FIRST) FROM operations.price_counters`); got != "1:0 1:2000" {
		t.Errorf("the counters: %s", got)
	}

	f.reprice("2.4")
	rec = f.publish(model, f.waiting(model), newKey(), "")
	expectAdmin(t, "another move", rec, http.StatusAccepted, "")
	stale := agentOperation(t, rec)
	f.reprice("3")
	if approved := f.approve(stale.Approval.ApprovalId); approved.OperationState != adminapi.OperationStateRejected || *approved.ErrorCode != string(codeStaleRevision) {
		t.Errorf("an approval of a replaced proposal: %+v", approved)
	}
}

// TestOperationsPriceRefusals: publishing needs operations.money, and the owner's envelope keeps a revision.
func TestOperationsPriceRefusals(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	model := f.w.models[pricedModel].String()
	f.reprice("1.8")
	_, token := f.makeCredential(f.s, `{"name":"no money","permissions":["pricing.view","pricing.manage"],"platform":true,
		"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	p := f.waiting(model)
	rec := f.agentSend(token, http.MethodPost, "/prices/"+model+"/publish", `{"candidate_id":"`+*p.CandidateId+`","reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "without operations.money", rec, http.StatusForbidden, codeForbidden)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	expectAdmin(t, "an old revision", f.setEnvelope(priceEnvelope, "0"), http.StatusConflict, codeStaleRevision)
	expectAdmin(t, "a margin over 100%", f.setEnvelope(`{"min_margin_bps":10001,"model_daily_change_bps":0,"daily_changes":0,"daily_models":0}`, "1"), http.StatusBadRequest, codeInvalidRequest)
	rec = f.do(http.MethodGet, "/operations/price-envelopes", "", as(f.s))
	var list adminapi.OperationsPriceEnvelopeList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].SiteId != f.siteID() || list.Data[0].Rev != 1 || list.Data[0].Limits.ModelDailyChangeBps != 2500 {
		t.Errorf("the envelopes: %s", rec.Body)
	}
	if got := f.text(`SELECT string_agg(action, ',') FROM platform.audit_log WHERE action = 'price_envelope.set'`); got != "price_envelope.set" {
		t.Errorf("the audit: %s", got)
	}
}

// TestOperationsPricePublishRetryRace: a retry sent while the first request still runs waits for it on the site's
// price policy and answers with its operation; the price is published and counted once.
func TestOperationsPricePublishRetryRace(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	model := f.w.models[pricedModel].String()
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	f.reprice("1.8")
	p := f.waiting(model)
	versions := f.w.query(`SELECT count(*)::text FROM billing.price_versions`)
	key := newKey()
	answers := make(chan *httptest.ResponseRecorder, 2)
	if err := f.w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM billing.price_policies WHERE site_id = $1 FOR UPDATE`, f.w.site); err != nil {
			return err
		}
		for range 2 {
			go func() { answers <- f.publish(model, p, key, "") }()
		}
		waitUntil(t, 10*time.Second, func() bool {
			return f.w.query(`SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) == "2"
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, second := <-answers, <-answers
	a, b := agentOperation(t, first), agentOperation(t, second)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || a.OperationId == nil || b.OperationId == nil || *a.OperationId != *b.OperationId {
		t.Fatalf("the two answers: %d %s / %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	if got := f.w.query(`SELECT (count(*) - ` + versions + `)::text FROM billing.price_versions`); got != "1" {
		t.Errorf("%s versions published", got)
	}
	f.expectUse(2000)
}
