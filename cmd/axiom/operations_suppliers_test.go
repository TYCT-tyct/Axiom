package main

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// byokOn makes an account's own provider key on site the way the console does, with a name waiting for a model as if
// a sync had listed one, and returns the supplier's ID.
func (f supplierFixture) byokOn(site uuid.UUID) string {
	f.t.Helper()
	account := uuid.NewV7()
	f.run(fmt.Sprintf(`INSERT INTO platform.tenants (site_id, tenant_id) VALUES ('%[1]s', '%[1]s'), ('%[1]s', '%[2]s')
		ON CONFLICT DO NOTHING`, site, account))
	admin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: masterKeys(f.t), Byok: true})
	if err != nil {
		f.t.Fatal(err)
	}
	var key suppliers.ByokKey
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		key, err = admin.CreateByok(f.t.Context(), tx, suppliers.ByokRequest{
			Site: site, Account: account, Name: "Bob's own key", Provider: "openai", Key: "sk-proj-abcdefghijklmnop",
			Fallback: true, Actor: audit.User(uuid.NewV7()),
		}, []suppliers.ByokModel{{
			ID: uuid.NewV7(), Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`),
		}})
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats)
		SELECT supplier_id, id, 'bob-name', '{chat}' FROM suppliers.supplier_groups WHERE supplier_id = '%s'`, key.ID))
	return key.ID.String()
}

// TestOperationsSupplierReadsKeepToThePlatform: a credential limited to site A holds the platform grant, which reaches
// the platform's own suppliers and nothing of a site B account's own key: the list and the waiting names have none of
// it and its detail is not found, while the platform's supplier is read as before.
func TestOperationsSupplierReadsKeepToThePlatform(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	platform := f.connect().Id
	f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats)
		VALUES ('%s', '%s', 'platform-name', '{chat}')`, platform, f.group(platform, "1")))
	byok := f.byokOn(uuid.NewV7())
	_, token := f.makeCredential(f.s, `{"name":"site a","permissions":["suppliers.view"],"platform":true,
		"sites":["`+f.siteID()+`"],"proposer_vendors":["deepseek"],"expires_at":"2099-01-01T00:00:00Z","reason":"patrol"}`)

	rec := f.agent(token, because("/suppliers", "patrol"))
	expectAdmin(t, "suppliersList", rec, http.StatusOK, "")
	var list operationsapi.SupplierList
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Id != platform {
		t.Errorf("the suppliers listed: %s", rec.Body)
	}
	expectAdmin(t, "the account's key", f.agent(token, because("/suppliers/"+byok, "patrol")), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "the platform's supplier", f.agent(token, because("/suppliers/"+platform, "patrol")), http.StatusOK, "")

	rec = f.agent(token, because("/pending-mappings", "patrol"))
	expectAdmin(t, "pendingMappingsList", rec, http.StatusOK, "")
	var pending operationsapi.PendingMappingList
	decode(t, rec, &pending)
	if !slices.EqualFunc(pending.Data, []string{"platform-name"}, func(m operationsapi.PendingMapping, name string) bool {
		return m.UpstreamName == name && m.SupplierId == platform
	}) {
		t.Errorf("the names waiting: %s", rec.Body)
	}
}
