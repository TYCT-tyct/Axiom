package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/quality"
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
	if *p.Status != adminapi.PricePublicationStatusPending {
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
	if preview := agentOperation(t, rec); rec.Code != http.StatusOK || !preview.DryRun || preview.State != adminapi.OperationStateApproved || versions() != before {
		t.Errorf("the preview: %d %s", rec.Code, rec.Body)
	}
	key := newKey()
	rec = f.publish(model, p, key, "")
	done := agentOperation(t, rec)
	if rec.Code != http.StatusOK || done.State != adminapi.OperationStateSucceeded || !strings.Contains(rec.Body.String(), `"change_bps":2000`) || versions() == before {
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
		Reasons []adminapi.OperationsApprovalReason `json:"reasons"`
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
	// It waits because the move is past the model's daily move, and only for that.
	expectReasons(t, "a 20% move", asked.Reasons, `[{"code":"envelope_model_daily_change"}]`)
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

// TestBasePrices: the base rates are a complete comparison only when neither side prices endpoints or tiers. An
// endpoint whose own price rises under an unchanged model envelope, or a tier that moves, leaves the base rates equal,
// which must not read as "no price changes".
func TestBasePrices(t *testing.T) {
	t.Parallel()
	flat := func(input string) jsonx.Raw {
		return jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"` + input + `"}}`)
	}
	tiered := jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"0.000001"},` +
		`"context_tiers":{"method":"whole","basis":["input"],"tiers":[{"above":1000,"rates":{"input":"0.000003"}}]}}`)
	endpoint := func(input string) catalog.Schedule {
		s, err := catalog.ParseSchedule(flat(input))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := uuid.NewV7(), uuid.NewV7()
	for _, c := range []struct {
		name     string
		p        billing.Publication
		complete bool
	}{
		{"one shared price each side", billing.Publication{CurrentSchedule: flat("0.000001"), Schedule: flat("0.0000012"), Cost: tiered}, true},
		{"an endpoint up under the same envelope", billing.Publication{
			CurrentSchedule: flat("0.000002"), Schedule: flat("0.000002"),
			CurrentEndpoints: billing.EndpointPrices{a: endpoint("0.000001"), b: endpoint("0.000002")},
			Endpoints:        billing.EndpointPrices{a: endpoint("0.0000015"), b: endpoint("0.000002")},
		}, false},
		{"a tier moves", billing.Publication{CurrentSchedule: flat("0.000001"), Schedule: tiered}, false},
	} {
		prices, complete, err := basePrices(c.p)
		if err != nil || complete != c.complete {
			t.Errorf("%s: complete %v, %v", c.name, complete, err)
		}
		if prices["current"] == nil || prices["proposed"] == nil {
			t.Errorf("%s: %v", c.name, prices)
		}
	}
}

// TestPricePublishRoutesAtOnce: every way a sale price is published, a pricing rule the agent changes included, rebuilds the publishing node's routing once it has
// committed, so the node's next call is admitted at the new version, not at the one the periodic rebuild last read.
// The call path here has no periodic rebuild at all: only the publication's rebuild can bring the new version.
func TestPricePublishRoutesAtOnce(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	model := f.w.models[pricedModel].String()
	n, _, _, _, routes := f.w.healthCallPath(&testClock{now: time.Now()}, quality.Observe)
	f.adminFixture = f.routing(routes)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	admitsAtCurrent := func(how string) {
		t.Helper()
		if status := f.w.callControlled(n, messagesCall); status != http.StatusOK {
			t.Fatalf("%s: the call answered %d", how, status)
		}
		current := f.publication(model).CurrentId
		if current == nil {
			t.Fatalf("%s: no version is current", how)
		}
		if frozen := f.text(`SELECT price_versions[1]::text FROM billing.reservations ORDER BY created_at DESC LIMIT 1`); frozen != *current {
			t.Errorf("%s: the call was admitted at %s, the current version is %s", how, frozen, *current)
		}
	}
	admitsAtCurrent("before any publication")

	f.servedOnly()
	rule := f.rule() // 1.5 to 1.6 for every model, inside the envelope: the agent changes it itself
	if rec := f.changeRule(rule.Id, rule.Rev, "1.6", newKey(), ""); rec.Code != http.StatusOK {
		t.Fatalf("the agent's rule change: %d %s", rec.Code, rec.Body)
	}
	admitsAtCurrent("after the agent changed a rule")

	f.reprice("1.8") // 20%, inside the envelope's 25%: the agent publishes it itself
	if rec := f.publish(model, f.waiting(model), newKey(), ""); rec.Code != http.StatusOK {
		t.Fatalf("the agent's publication: %d %s", rec.Code, rec.Body)
	}
	admitsAtCurrent("after the agent published")

	f.reprice("2.16") // another 20%, past the model's 25% a day: the owner approves it
	rec := f.publish(model, f.waiting(model), newKey(), "")
	expectAdmin(t, "a second move", rec, http.StatusAccepted, "")
	if approved := f.approve(agentOperation(t, rec).Approval.ApprovalId); approved.OperationState != adminapi.OperationStateSucceeded {
		t.Fatalf("the approval: %+v", approved)
	}
	admitsAtCurrent("after the owner approved")

	f.reprice("2.6")
	p := f.waiting(model)
	body := `{"candidate_id":"` + *p.CandidateId + `","current_id":"` + *p.CurrentId + `","confirmed":true,"override_floor":false,"reason":"the rule moved"}`
	expectAdmin(t, "the administrator's publication", f.change(f.s, http.MethodPost, "/pricing/publications/"+model, body, newKey()), http.StatusOK, "")
	admitsAtCurrent("after an administrator published")

	// A rebuild stuck on a source does not hold up a publication that committed: it answers once the refresh's limit
	// passes, says so in the log, keeps the version it published, and the next rebuild routes at it.
	release := f.lockPresets()
	f.reprice("3")
	p = f.waiting(model)
	body = `{"candidate_id":"` + *p.CandidateId + `","current_id":"` + *p.CurrentId + `","confirmed":true,"override_floor":false,"reason":"the rule moved"}`
	expectAdmin(t, "a publication while routing cannot be read", f.change(f.s, http.MethodPost, "/pricing/publications/"+model, body, newKey()), http.StatusOK, "")
	if !strings.Contains(f.logs.String(), "did not rebuild its routing") {
		t.Errorf("the stuck rebuild was not logged: %s", f.logs)
	}
	if current := f.publication(model).CurrentId; current == nil || *current == *p.CurrentId {
		t.Fatalf("the publication did not stay: the current version is %v", current)
	}
	release()
	if err := routes.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	admitsAtCurrent("after the source freed")
}

// lockPresets holds routing's preset table, which every routing build reads, until the returned function is called.
func (f priceFixture) lockPresets() (release func()) {
	f.t.Helper()
	db, err := pg.Open(f.t.Context(), f.w.database.Superuser)
	if err != nil {
		f.t.Fatal(err)
	}
	locked, free, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.Tx(context.WithoutCancel(f.t.Context()), pg.PlatformScope(), func(tx pg.Tx) error {
			if _, err := tx.Exec(f.t.Context(), `LOCK TABLE routing.presets IN ACCESS EXCLUSIVE MODE`); err != nil {
				return err
			}
			close(locked)
			<-free
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		f.t.Fatalf("locking the presets: %v", err)
	}
	return func() {
		close(free)
		if err := <-done; err != nil {
			f.t.Error(err)
		}
		db.Close()
	}
}
