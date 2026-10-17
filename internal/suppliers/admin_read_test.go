package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func (a admins) list(q SupplierQuery) ([]Supplier, bool) {
	a.t.Helper()
	type page struct {
		suppliers []Supplier
		more      bool
	}
	p, err := inTx(a, func(ctx context.Context, tx pg.Tx) (page, error) {
		suppliers, more, err := a.admin.Suppliers(ctx, tx, q)
		return page{suppliers, more}, err
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return p.suppliers, p.more
}

func supplierIDs(list []Supplier) []uuid.UUID {
	out := []uuid.UUID{}
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

// three is three suppliers, the second active with a balance and two unit values.
func (a admins) three() (first, second, third Supplier) {
	first = a.connect("https://one.example.com", "a")
	second = a.connect("https://two.example.com", "a")
	third = a.connect("https://three.example.com", "a")
	a.setStatus(second.ID, "active")
	a.sql(fmt.Sprintf(`UPDATE suppliers.suppliers SET balance_native = 12.5, low_balance_threshold = 2, generation = '1.0',
		detected_version = 'v1' WHERE id = '%s'`, second.ID),
		fmt.Sprintf(`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ('%s', 7, 'system:t', 'r')`, second.ID),
		fmt.Sprintf(`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ('%s', 7.3, 'system:t', 'r')`, second.ID))
	return first, second, third
}

// TestSuppliersNewestFirst: suppliers newest first in pages that follow each other, filtered by status, each with the
// latest unit value and the figures the sync last stored.
func TestSuppliersNewestFirst(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	first, second, third := a.three()
	all, more := a.list(SupplierQuery{PageSize: 10})
	same(t, "the suppliers", []any{[]uuid.UUID{third.ID, second.ID, first.ID}, false}, []any{supplierIDs(all), more})
	same(t, "a supplier with its figures", []string{"7.3", "12.5", "2", "1.0", "v1", "active"},
		[]string{all[1].UnitValue, all[1].BalanceNative, all[1].LowBalanceThreshold, all[1].Generation, all[1].DetectedVersion, string(all[1].Status)})
	same(t, "a supplier with none", []string{"", "", ""}, []string{all[0].UnitValue, all[0].BalanceNative, all[0].Generation})
	page, more := a.list(SupplierQuery{PageSize: 2})
	rest, moreRest := a.list(SupplierQuery{PageSize: 2, After: page[1].ID})
	same(t, "pages of 2", []any{2, true, []uuid.UUID{first.ID}, false}, []any{len(page), more, supplierIDs(rest), moreRest})
	active, _ := a.list(SupplierQuery{Status: Active, PageSize: 10})
	same(t, "the active suppliers", []uuid.UUID{second.ID}, supplierIDs(active))
}

// TestSupplierDetail: one supplier with its groups and keys as the sync stored them; a supplier that is not there is
// ErrNoSupplier.
func TestSupplierDetail(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "g", true)
	key := uuid.NewV7()
	a.sql(fmt.Sprintf(`UPDATE suppliers.supplier_groups SET facts = '{"platform":"anthropic"}' WHERE id = '%s'`, group),
		fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
			VALUES ('%s', '%s', '%s', 'k1', 'binding', 4)`, key, id, group))
	detail, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) { return a.admin.Supplier(ctx, tx, id) })
	refuses(t, "the detail", err, nil)
	same(t, "its groups", []Group{{ID: group, UpstreamRef: "g", Name: "Group g", Ratio: "1.5", Present: true, Facts: map[string]any{"platform": "anthropic"}}}, detail.Groups)
	same(t, "its keys", []Credential{{ID: key, Group: group, UpstreamKeyRef: "k1", Status: "binding", MaxConcurrency: 4}}, detail.Credentials)
	_, err = inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) { return a.admin.Supplier(ctx, tx, uuid.NewV7()) })
	refuses(t, "a supplier that is not there", err, ErrNoSupplier)
}
