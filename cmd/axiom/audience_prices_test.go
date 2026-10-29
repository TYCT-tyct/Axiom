package main

import (
	"fmt"
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestAudiencePreviewMatchesNodeCharge(t *testing.T) {
	for _, kind := range []string{"tier", "account"} {
		t.Run(kind, func(t *testing.T) { assertAudienceNodeCharge(t, kind == "account") })
	}
}

func assertAudienceNodeCharge(t *testing.T, withAccount bool) {
	t.Helper()
	f := newPublicationView(t)
	w := f.world
	account := accounts.AccountID(uuid.MustParse(w.query("SELECT account_id::text FROM accounts.users WHERE email='rich@example.com'")))
	tier := accounts.TierID(uuid.MustParse(w.query("SELECT tier_id::text FROM accounts.accounts WHERE id=$1", uuid.UUID(account))))
	if _, err := f.node.user("", "grant-platform", "--email", "rich@example.com", "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	session := f.node.verified("rich@example.com")
	seedAudiencePrices(t, f, account, tier, withAccount)
	n := w.start()
	defer func() {
		if err := n.stop(); err != nil {
			t.Error(err)
		}
	}()
	id := w.models["anthropic/claude-opus-5-5"]
	rec := f.node.do(http.MethodPost, "/pricing/previews", fmt.Sprintf(`{"model_ids":[%q],"account_id":%q}`, id.String(), account.TypeID()), as(session))
	expectAdmin(t, "audience preview", rec, http.StatusOK, "")
	var preview adminapi.PricingPreview
	decode(t, rec, &preview)
	if len(preview.Data) != 1 || preview.Data[0].CurrentSale == nil || preview.Data[0].CurrentVersion == nil {
		t.Fatalf("missing current preview: %+v", preview)
	}
	raw, err := jsonx.MarshalCanonical(preview.Data[0].CurrentSale)
	if err != nil {
		t.Fatal(err)
	}
	price, err := catalog.ParseSchedule(raw)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 25, catalog.ItemOutput: 15}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := price.Cost(usage)
	if err != nil {
		t.Fatal(err)
	}
	response := w.call(n, "/v1/messages", w.richKey, messagesCall)
	if response.status != http.StatusOK {
		t.Fatalf("call: %d %s", response.status, response.body)
	}
	w.checkSettled(response, expected.CeilAmount())
	generation := response.header.Get("X-Generation-Id")
	if got := w.query("SELECT price_versions[entry+1]::text FROM billing.reservations WHERE request_id::text=$1", generation); got != *preview.Data[0].CurrentVersion {
		t.Fatalf("charged version %s, preview %s", got, *preview.Data[0].CurrentVersion)
	}
}

func seedAudiencePrices(t *testing.T, f publicationViewFixture, account accounts.AccountID, tier accounts.TierID, withAccount bool) {
	t.Helper()
	w := f.world
	for _, c := range []struct{ kind, id, multiplier string }{{"tier", tier.TypeID(), "1.6"}, {"account", account.TypeID(), "1.7"}} {
		if c.kind == "account" && !withAccount {
			continue
		}
		in := billing.RuleInput{Enabled: true, Target: billing.Selector{Kind: "all"}, Audience: billing.Selector{Kind: c.kind, ID: c.id}, Method: billing.Method{Kind: "reference_multiplier", Multiplier: &c.multiplier, CurrencyConversion: "face"}}
		if err := w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			site, ok := f.node.svc.MainSite()
			if !ok {
				t.Fatal("missing main site")
			}
			if _, err := billing.CreateRule(t.Context(), tx, audit.System("audience.test"), w.site, site.Currency, in, "audience price"); err != nil {
				return err
			}
			return refreshSalePrices(t.Context(), tx, w.site, site.Currency)
		}); err != nil {
			t.Fatal(err)
		}
	}
}
