package main

import (
	"bytes"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// rule is the call world's one rule as the agent reads it.
func (f priceFixture) rule() operationsapi.PricingRule {
	f.t.Helper()
	rec := f.agent(f.token, because("/pricing/rules", "what prices the models"))
	expectAdmin(f.t, "pricingRulesList", rec, http.StatusOK, "")
	var list operationsapi.PricingRuleList
	decode(f.t, rec, &list)
	if len(list.Data) != 1 {
		f.t.Fatalf("the rules: %s", rec.Body)
	}
	return list.Data[0]
}

// multiplied is the body of a rule that prices target at its reference price times multiplier.
func multiplied(target, multiplier string) string {
	return `{"enabled":true,"target":` + target + `,"audience":{"kind":"all"},"method":{"kind":"reference_multiplier",` +
		`"multiplier":"` + multiplier + `","currency_conversion":"face"},"constraints":{}}`
}

// changeRule asks for the call world's rule to price at multiplier as of rev.
func (f priceFixture) changeRule(id string, rev int64, multiplier, key, extra string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := `{"rev":` + strconv.FormatInt(rev, 10) + `,"rule":` + multiplied(`{"kind":"all"}`, multiplier) + `,"reason":"costs fell"` + extra + `}`
	return f.agentSend(f.token, http.MethodPut, "/pricing/rules/"+id, body, idempotent(key))
}

// ruleMoves is what a rule write answered of the prices it moved.
type ruleMoves struct {
	RuleID     string `json:"rule_id"`
	Rev        int64  `json:"rev"`
	MovesTotal int    `json:"moves_total"`
	Moves      []struct {
		ModelID  string `json:"model_id"`
		Slug     string `json:"slug"`
		Audience struct {
			Kind string `json:"kind"`
		} `json:"audience"`
		ChangeBps *int64 `json:"change_bps"`
		MarginBps *int64 `json:"margin_bps"`
	} `json:"moves"`
}

func movesOf(t *testing.T, op operationsapi.Operation) ruleMoves {
	t.Helper()
	var m ruleMoves
	if op.Result == nil {
		t.Fatalf("no result: %+v", op)
	}
	if err := jsonx.Unmarshal(*op.Result, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// counters is the main site's price counters today: the site's publications/models, then each model's moves#count.
func (f priceFixture) counters() string {
	f.t.Helper()
	return f.text(`SELECT coalesce(string_agg(CASE WHEN model_id IS NULL THEN count || '/' || models ELSE change_bps || '#' || count END,
		' ' ORDER BY model_id NULLS FIRST), '') FROM operations.price_counters`)
}

// TestOperationsPricingRules: the agent changes a rule as itself, and every sale price the change publishes at once is
// measured by the server and counted in the day's envelope; a preview writes nothing and says what would move, a retry
// finds the change, an old revision is stale, and a second rule for one target and audience is a conflict. Without an
// envelope no rule changes.
func TestOperationsPricingRules(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	f.servedOnly()
	rule := f.rule()
	expectAdmin(t, "without an envelope", f.changeRule(rule.Id, rule.Rev, "1.6", newKey(), ""), http.StatusUnprocessableEntity, codeCapExceeded)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	before := f.versions()

	rec := f.changeRule(rule.Id, rule.Rev, "1.6", newKey(), `,"dry_run":true`)
	preview := agentOperation(t, rec)
	moved := movesOf(t, preview)
	if got := fmt.Sprintf("%d %v %s %v %d %v", rec.Code, preview.DryRun, preview.State, f.versions() == before, f.rule().Rev, moved.MovesTotal > 0 && len(moved.Moves) == moved.MovesTotal); got != fmt.Sprintf("200 true approved true %d true", rule.Rev) {
		t.Fatalf("the preview: %s: %s", got, rec.Body)
	}

	key := newKey()
	rec = f.changeRule(rule.Id, rule.Rev, "1.6", key, "")
	done := agentOperation(t, rec)
	result := movesOf(t, done)
	if got := fmt.Sprintf("%d %s %s %d %d %v", rec.Code, done.State, result.RuleID, result.Rev, result.MovesTotal, f.versions() == before); got != fmt.Sprintf("200 succeeded %s %d %d false", rule.Id, rule.Rev+1, moved.MovesTotal) {
		t.Fatalf("the change: %s: %s", got, rec.Body)
	}
	f.expectMovedBy667(result, "pricing_rule.update")
	if again := f.changeRule(rule.Id, rule.Rev, "1.6", key, ""); again.Header().Get("Idempotency-Replayed") != "true" || *agentOperation(t, again).OperationId != *done.OperationId {
		t.Errorf("the retry: %d %s", again.Code, again.Body)
	}
	expectAdmin(t, "an old revision", f.changeRule(rule.Id, rule.Rev, "1.55", newKey(), ""), http.StatusConflict, codeStaleRevision)
	rec = f.agentSend(f.token, http.MethodPost, "/pricing/rules", `{"rule":`+multiplied(`{"kind":"all"}`, "1.7")+`,"reason":"r"}`, idempotent(newKey()))
	expectAdmin(t, "a second rule for everything", rec, http.StatusConflict, codeConflict)
}

func (f priceFixture) versions() string {
	return f.text(`SELECT count(*)::text FROM billing.price_versions`)
}

// expectMovedBy667 checks that each move of result is 1.6 against 1.5, a fifteenth (666.67 basis points, counted as
// 667), with the model's slug, that the day counted each, and that action was audited as the agent's.
func (f priceFixture) expectMovedBy667(result ruleMoves, action string) {
	f.t.Helper()
	for _, m := range result.Moves {
		if m.ChangeBps == nil || *m.ChangeBps != 667 || m.Slug == "" {
			f.t.Errorf("a move: %+v", m)
		}
	}
	n := strconv.Itoa(result.MovesTotal)
	if got, want := f.counters(), n+"/"+n+strings.Repeat(" 667#1", result.MovesTotal); got != want {
		f.t.Errorf("the counters are %s, want %s", got, want)
	}
	if got := f.text(`SELECT actor_kind FROM platform.audit_log WHERE action = '` + action + `'`); got != "operations_agent" {
		f.t.Errorf("%s as %s", action, got)
	}
}

// TestOperationsPricingRuleOverThePublisher: a change whose moves are over the publisher's own threshold publishes
// nothing yet, so it changes the rule and counts nothing; the moves wait as proposals, which price_publish weighs.
func TestOperationsPricingRuleOverThePublisher(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	f.servedOnly()
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	rule := f.rule()
	// 1.5 to 1.8 is 20%, over the publisher's 10%.
	before := f.versions()
	rec := f.changeRule(rule.Id, rule.Rev, "1.8", newKey(), "")
	op := agentOperation(t, rec)
	if got := fmt.Sprintf("%d %s %d %v %d %s", rec.Code, op.State, movesOf(t, op).MovesTotal, f.versions() == before, f.rule().Rev, f.counters()); got != fmt.Sprintf("200 succeeded 0 true %d 0/0", rule.Rev+1) {
		t.Errorf("a move over the publisher's threshold: %s: %s", got, rec.Body)
	}
	model := f.w.models[pricedModel].String()
	if p := f.waiting(model); p.CandidateId == nil {
		t.Errorf("no proposal waits for %s", model)
	}
}

// TestOperationsPricingRuleCreate: a rule the agent adds for one model moves only that model's price, as itself.
func TestOperationsPricingRuleCreate(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	model := f.w.models[pricedModel].String()
	rec := f.agentSend(f.token, http.MethodPost, "/pricing/rules", `{"rule":`+multiplied(`{"kind":"model","id":"`+model+`"}`, "1.6")+`,"reason":"r"}`, idempotent(newKey()))
	op := agentOperation(t, rec)
	result := movesOf(t, op)
	if got := fmt.Sprintf("%d %s %s %d %d", rec.Code, op.State, op.Target.Kind, result.Rev, result.MovesTotal); got != "200 succeeded site 0 1" || result.Moves[0].ModelID != model {
		t.Fatalf("the rule: %s: %s", got, rec.Body)
	}
	f.expectMovedBy667(result, "pricing_rule.create")
}

// TestOperationsPricingRuleOutranks: a rule the agent adds for one model and everyone wins over an account's own rule
// for every model (a more specific target), so that account's price moves even though everyone else's does not: the
// account paid 1.4 times the reference and now pays 1.5, a move of 1/14 (714.29 basis points, counted as 715).
func TestOperationsPricingRuleOutranks(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	account := accounts.AccountID(uuid.MustParse(f.text(`SELECT account_id::text FROM accounts.users WHERE email = 'rich@example.com'`)))
	own := strings.Replace(multiplied(`{"kind":"all"}`, "1.4"), `"audience":{"kind":"all"}`, `"audience":{"kind":"account","id":"`+account.TypeID()+`"}`, 1)
	expectAdmin(t, "the account's own rule", f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+own+`,"reason":"a discount"}`, newKey()), http.StatusCreated, "")
	f.refresh()
	model := f.w.models[pricedModel].String()
	rec := f.agentSend(f.token, http.MethodPost, "/pricing/rules", `{"rule":`+multiplied(`{"kind":"model","id":"`+model+`"}`, "1.5")+`,"reason":"r"}`, idempotent(newKey()))
	op := agentOperation(t, rec)
	var moved []string
	for _, m := range movesOf(t, op).Moves {
		if m.ModelID == model && m.ChangeBps != nil {
			moved = append(moved, m.Audience.Kind+":"+strconv.FormatInt(*m.ChangeBps, 10))
		}
	}
	if got := fmt.Sprint(rec.Code, " ", op.State, " ", moved); got != "200 succeeded [tier:0 account:715]" {
		t.Errorf("the moves: %s: %s", got, rec.Body)
	}
}

// refresh lets the publisher propose and publish from the rules as they are, as its job would.
func (f priceFixture) refresh() {
	f.t.Helper()
	site, _ := f.svc.MainSite()
	if err := f.w.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return refreshSalePrices(f.t.Context(), tx, f.w.site, site.Currency) }); err != nil {
		f.t.Fatal(err)
	}
}

// servedOnly takes the call world's model that nothing serves off sale, so a rule for every model moves only prices that
// are on sale now: a price for a model nothing serves would wait for the owner (TestOperationsPricingRuleUnsold).
func (f priceFixture) servedOnly() {
	f.t.Helper()
	f.run(`UPDATE catalog.model SET status = 'retired' WHERE slug = 'acme/unserved'`)
	f.refresh()
}

// TestOperationsPricingRuleUnsold: a rule that prices a model nothing can be sold at yet changes no price on sale, but
// the publisher would publish that price by itself as soon as the model could be served, outside any envelope; so the
// change is weighed as giving a price no share measures, waits for the owner and is not kept, even where the envelope
// allows no change at all today. A rule for every model that reaches such a model waits the same way.
func TestOperationsPricingRuleUnsold(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	expectAdmin(t, "the envelope", f.setEnvelope(`{"min_margin_bps":0,"model_daily_change_bps":2500,"daily_changes":0,"daily_models":5}`, "0"), http.StatusOK, "")
	unserved := f.w.models["acme/unserved"].String()
	fixed := `{"enabled":true,"target":{"kind":"model","id":"` + unserved + `"},"audience":{"kind":"all"},` +
		`"method":{"kind":"fixed","schedule":{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015"}}},"constraints":{}}`
	rec := f.agentSend(f.token, http.MethodPost, "/pricing/rules", `{"rule":`+fixed+`,"reason":"a first price"}`, idempotent(newKey()))
	expectAdmin(t, "a price for a model nothing serves", rec, http.StatusAccepted, "")
	waits := agentOperation(t, rec)
	expectReasons(t, "a first price", *waits.Approval.Reasons, `[{"code":"envelope_unmeasured_change"},{"code":"envelope_unknown_cost"},{"code":"envelope_daily_changes"}]`)
	if got := f.text(`SELECT count(*)::text FROM billing.pricing_rules`); got != "1" {
		t.Errorf("a rule that waits was kept: %s rules", got)
	}
	rule := f.rule()
	rec = f.changeRule(rule.Id, rule.Rev, "1.6", newKey(), "")
	expectAdmin(t, "a rule for every model, one of them unserved", rec, http.StatusAccepted, "")
}

// TestOperationsPricingReadsKeepToSites: a credential with the platform grant but kept to another site reads neither
// the main site's pricing rules nor its prices: both are answered as for nothing there.
func TestOperationsPricingReadsKeepToSites(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	child := f.childSite()
	_, token := f.makeCredential(f.s, `{"name":"elsewhere","permissions":["pricing.view"],"platform":true,"sites":["`+
		accounts.SiteID(child).TypeID()+`"],"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"r"}`)
	expectAdmin(t, "the rules", f.agent(token, because("/pricing/rules", "r")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "the prices", f.agent(token, because("/prices", "r")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "the rules, reached", f.agent(f.token, because("/pricing/rules", "r")), http.StatusOK, "")
}

// TestOperationsPricingRuleApproval: a change whose moves are over the envelope together is taken back and waits for
// the owner, who sees every move; approved, it is made again as the agent's and counted.
func TestOperationsPricingRuleApproval(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	f.servedOnly()
	expectAdmin(t, "the envelope", f.setEnvelope(narrowEnvelope, "0"), http.StatusOK, "")
	rule := f.rule()
	versions := f.versions()
	waits := f.waitingRule(rule.Id, rule.Rev, "1.6")
	kept := fmt.Sprintf("%d %v %q %s", f.rule().Rev, f.versions() == versions, f.counters(), f.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'pricing_rule.update'`))
	if kept != fmt.Sprintf(`%d true "" 0`, rule.Rev) {
		t.Fatalf("a change that waits was kept: %s", kept)
	}
	rec := f.do(http.MethodGet, "/operations/approvals/"+waits.Approval.ApprovalId, "", as(f.s))
	var asked struct {
		Reasons []adminapi.OperationsApprovalReason `json:"reasons"`
		Summary struct {
			RuleID     string `json:"rule_id"`
			RuleRev    int64  `json:"rule_rev"`
			MovesTotal int    `json:"moves_total"`
			Envelope   struct {
				ModelDailyChangeBps int64 `json:"model_daily_change_bps"`
			} `json:"envelope"`
			Moves []struct {
				Slug      string `json:"slug"`
				ChangeBps int64  `json:"change_bps"`
			} `json:"moves"`
		} `json:"summary"`
	}
	decode(t, rec, &asked)
	s := asked.Summary
	if s.MovesTotal == 0 || len(s.Moves) != s.MovesTotal {
		t.Fatalf("the owner sees %s", rec.Body)
	}
	if got := fmt.Sprintf("%s %d %d %v %d", s.RuleID, s.RuleRev, s.Envelope.ModelDailyChangeBps, s.Moves[0].Slug != "", s.Moves[0].ChangeBps); got != fmt.Sprintf("%s %d 500 true 667", rule.Id, rule.Rev) {
		t.Errorf("the owner sees %s: %s", got, rec.Body)
	}
	expectReasons(t, "a 6.67% move", asked.Reasons, `[{"code":"envelope_model_daily_change"}]`)
	if approved := f.approve(waits.Approval.ApprovalId); approved.OperationState != adminapi.OperationStateSucceeded {
		t.Fatalf("the approval: %+v", approved)
	}
	if f.rule().Rev != rule.Rev+1 {
		t.Errorf("approved: rev %d", f.rule().Rev)
	}
	f.expectMovedBy667(ruleMoves{MovesTotal: s.MovesTotal}, "pricing_rule.update")
}

// narrowEnvelope lets a model's prices move 5% a day, less than a rule moving 1.5 to 1.6.
const narrowEnvelope = `{"min_margin_bps":0,"model_daily_change_bps":500,"daily_changes":5,"daily_models":5}`

// waitingRule asks for the call world's rule to price at multiplier as of rev, which waits for the owner.
func (f priceFixture) waitingRule(id string, rev int64, multiplier string) operationsapi.Operation {
	f.t.Helper()
	rec := f.changeRule(id, rev, multiplier, newKey(), "")
	expectAdmin(f.t, "a change over the envelope", rec, http.StatusAccepted, "")
	return agentOperation(f.t, rec)
}

// TestOperationsPricingRuleApprovalStale: a change whose rule the owner changed before approving it ends stale, and
// changes nothing.
func TestOperationsPricingRuleApprovalStale(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	expectAdmin(t, "the envelope", f.setEnvelope(narrowEnvelope, "0"), http.StatusOK, "")
	rule := f.rule()
	stale := f.waitingRule(rule.Id, rule.Rev, "1.6")
	if err := pgUpdate(f, `UPDATE billing.pricing_rules SET rev = rev + 1`); err != nil { // the owner's own edit
		t.Fatal(err)
	}
	approved := f.approve(stale.Approval.ApprovalId)
	if approved.OperationState != adminapi.OperationStateRejected || *approved.ErrorCode != string(codeStaleRevision) || f.counters() != "" {
		t.Errorf("an approval of a changed rule: %+v, counters %q", approved, f.counters())
	}
}

func pgUpdate(f priceFixture, statement string) error {
	return f.w.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(f.t.Context(), statement)
		return err
	})
}

// TestOperationsPricingRuleRetryRace: a retry sent while the first request still runs waits for it on the site's price
// policy and answers with its operation; the rule is changed and its moves counted once.
func TestOperationsPricingRuleRetryRace(t *testing.T) {
	t.Parallel()
	f := newPriceFixture(t)
	f.servedOnly()
	expectAdmin(t, "the envelope", f.setEnvelope(priceEnvelope, "0"), http.StatusOK, "")
	rule := f.rule()
	key := newKey()
	answers := make(chan *httptest.ResponseRecorder, 2)
	if err := f.w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM billing.price_policies WHERE site_id = $1 FOR UPDATE`, f.w.site); err != nil {
			return err
		}
		for range 2 {
			go func() { answers <- f.changeRule(rule.Id, rule.Rev, "1.6", key, "") }()
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
	if f.rule().Rev != rule.Rev+1 || !strings.HasSuffix(f.counters(), " 667#1") || strings.Contains(f.counters(), "#2") {
		t.Errorf("rev %d, counters %s", f.rule().Rev, f.counters())
	}
}

// TestSaleMoves: a rule change moves what a buyer pays, as billing selects it from the audience layers, each distinct
// change of a model counted once however many buyers it reaches. A layer republished moves every buyer it serves, once.
// A new model-specific layer for everyone wins over an account's own cheaper layer for every model, so that account
// pays the new price: 0.5 to 1.5 is a 200% move, while everyone else's price does not move. An account layer that now
// matches but is blocked stops the account's sales: neither a move nor a margin. A first price has a margin but no
// measured move, and a model whose layers did not change does not move.
func TestSaleMoves(t *testing.T) {
	t.Parallel()
	schedule := func(input string) jsonx.Raw {
		return jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"` + input + `"}}`)
	}
	tier, account := accounts.TierID(uuid.NewV7()), accounts.AccountID(uuid.NewV7())
	buyers := []buyer{
		{billing.Audience{Tier: tier}, billing.Selector{Kind: "tier", ID: tier.TypeID()}},
		{billing.Audience{Account: account, Tier: tier}, billing.Selector{Kind: "account", ID: account.TypeID()}},
	}
	everyone, theAccount := billing.Audience{}, billing.Audience{Account: account}
	view := func(models map[catalog.ModelID][]layer) priceView { return layeredView(models, schedule) }
	repriced, preferred, blocked, first, steady := catalog.ModelID(uuid.NewV7()), catalog.ModelID(uuid.NewV7()), catalog.ModelID(uuid.NewV7()),
		catalog.ModelID(uuid.NewV7()), catalog.ModelID(uuid.NewV7())
	before := view(map[catalog.ModelID][]layer{
		repriced:  {{everyone, "all", "0.000002"}},
		preferred: {{everyone, "all", "0.0000015"}, {theAccount, "all", "0.0000005"}},
		blocked:   {{everyone, "all", "0.000002"}},
		steady:    {{everyone, "all", "0.000002"}},
	})
	after := view(map[catalog.ModelID][]layer{
		repriced:  {{everyone, "all", "0.0000025"}},
		preferred: {{everyone, "model", "0.0000015"}, {theAccount, "all", "0.0000005"}},
		first:     {{everyone, "all", "0.000004"}},
	})
	// steady's layer, and blocked's for everyone, are the same version after: carried over, not republished.
	after.sold[steady] = before.sold[steady]
	after.sold[blocked] = billing.Sold{Audiences: billing.CompilePrices([]billing.AudiencePrice{
		{Target: billing.Selector{Kind: "all"}, Sold: before.sold[blocked].For(everyone)},
		{Audience: theAccount, Target: billing.Selector{Kind: "model"}},
	})}
	moves, err := saleMoves(uuid.NewV7(), buyers, before, after)
	if err != nil {
		t.Fatal(err)
	}
	got := map[catalog.ModelID][]string{}
	for i, m := range moves {
		audience := m.Summary["audience"].(map[string]any)["kind"]
		got[catalog.ModelID(m.Model)] = append(got[catalog.ModelID(m.Model)], fmt.Sprint(audience, " ", ratText(m.Change), " ", ratText(m.Margin)))
		if i > 0 && bytes.Compare(moves[i-1].Model[:], m.Model[:]) > 0 {
			t.Errorf("the moves are not in the order of their models")
		}
	}
	want := map[catalog.ModelID][]string{
		repriced:  {"tier 1/4 24/25"},
		preferred: {"tier 0 14/15", "account 2 14/15"},
		blocked:   {"account - -"},
		first:     {"tier - 39/40"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the moves:\n got %v\nwant %v", got, want)
	}
}

// layer is an audience layer of a model's prices in a test: whom it is for, its target's kind, and its input price, ""
// for a layer that matches but sells nothing, as a blocked one.
type layer struct {
	audience billing.Audience
	target   string
	input    string
}

// layeredView is the prices of models, each its layers; every sold layer is a publication of its own version, costing
// a tenth of a millionth a token.
func layeredView(models map[catalog.ModelID][]layer, schedule func(string) jsonx.Raw) priceView {
	v := priceView{sold: map[catalog.ModelID]billing.Sold{}}
	for model, layers := range models {
		var compiled []billing.AudiencePrice
		for _, l := range layers {
			price := billing.AudiencePrice{Audience: l.audience, Target: billing.Selector{Kind: l.target}}
			if l.input != "" {
				version := uuid.NewV7()
				price.Sold.Version = version
				v.publications = append(v.publications, billing.Publication{
					Model: model, Status: "published", Current: &version, Schedule: schedule(l.input), CurrentSchedule: schedule(l.input), Cost: schedule("0.0000001"),
				})
			}
			compiled = append(compiled, price)
		}
		v.sold[model] = billing.Sold{Audiences: billing.CompilePrices(compiled)}
	}
	return v
}

func ratText(r *big.Rat) string {
	if r == nil {
		return "-"
	}
	return r.RatString()
}
