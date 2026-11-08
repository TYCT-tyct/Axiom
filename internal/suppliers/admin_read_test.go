package suppliers

import (
	"context"
	"fmt"
	"testing"
	"time"
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

// TestSupplierDetailShowsPool: the group carries what the pool's management API last said of it, and a group no read has
// reported has none rather than a zeroed capacity.
func TestSupplierDetailShowsPool(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	pooled := a.group(id, "pooled", true)
	a.group(id, "empty", false)
	a.sql(fmt.Sprintf(`UPDATE suppliers.supplier_groups
SET pool_accounts = 10, pool_failed = 2, pool_usable = 8, pool_headroom = 40, pool_capacity = 32, pool_low = false,
    pool_observed_at = '2026-10-15T12:00:00Z' WHERE id = '%s'`, pooled))
	detail, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) {
		return a.admin.Supplier(ctx, tx, id, PlatformOwned)
	})
	refuses(t, "the detail", err, nil)
	if len(detail.Groups) != 2 {
		t.Fatalf("groups %+v, want two", detail.Groups)
	}
	headroom, capacity := 40, 32
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, g := range detail.Groups {
		if g.UpstreamRef == "pooled" {
			same(t, "the pooled group", &Pool{Accounts: 10, Failed: 2, Usable: 8, Headroom: &headroom, Capacity: &capacity, ObservedAt: at}, g.Pool)
		} else {
			same(t, "the group no read reported", (*Pool)(nil), g.Pool)
		}
	}
}

// TestSupplierDetail: one supplier with its groups, keys and endpoints (by upstream name) as the sync stored them; a
// supplier that is not there is ErrNoSupplier.
func TestSupplierDetail(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "g", true)
	key, zeta, alpha, one, two := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	a.sql(fmt.Sprintf(`UPDATE suppliers.supplier_groups SET facts = '{"platform":"anthropic"}' WHERE id = '%s'`, group),
		fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
			VALUES ('%s', '%s', '%s', 'k1', 'binding', 4)`, key, id, group),
		fmt.Sprintf(`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats, enabled)
			VALUES ('%s', '%[2]s', '%[3]s', '%[4]s', 'zeta', ARRAY['messages', 'chat'], true),
				('%[5]s', '%[2]s', '%[3]s', '%[6]s', 'alpha', ARRAY['responses'], false)`, zeta, id, group, one, alpha, two))
	detail, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) {
		return a.admin.Supplier(ctx, tx, id, PlatformOwned)
	})
	refuses(t, "the detail", err, nil)
	same(t, "its groups", []Group{{ID: group, UpstreamRef: "g", Name: "Group g", Ratio: "1.5", Present: true, Facts: map[string]any{"platform": "anthropic"}, KeyState: "pending"}}, detail.Groups)
	same(t, "its keys", []Credential{{ID: key, Group: group, UpstreamKeyRef: "k1", Status: "binding", MaxConcurrency: 4}}, detail.Credentials)
	same(t, "its endpoints", []Endpoint{
		{ID: alpha, Group: group, Model: two, UpstreamName: "alpha", Formats: []string{"responses"}, CostState: "unknown"},
		{ID: zeta, Group: group, Model: one, UpstreamName: "zeta", Formats: []string{"messages", "chat"}, CostState: "unknown", Enabled: true},
	}, detail.Endpoints)
	if _, err := a.choose(id, group, true); err != nil {
		t.Fatal(err)
	}
	a.sql(fmt.Sprintf(`UPDATE suppliers.supplier_groups SET key_error = 'not_permitted', key_retry_at = now() WHERE id = '%s'`, group))
	detail, err = inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) {
		return a.admin.Supplier(ctx, tx, id, PlatformOwned)
	})
	refuses(t, "the detail after a failed binding", err, nil)
	if g := detail.Groups[0]; g.KeyState != "failed" || g.KeyReason != "not_permitted" {
		t.Errorf("a binding whose read failed: %q, %q", g.KeyState, g.KeyReason)
	}
	_, err = inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) {
		return a.admin.Supplier(ctx, tx, uuid.NewV7(), PlatformOwned)
	})
	refuses(t, "a supplier that is not there", err, ErrNoSupplier)
}

// TestReadsSeeTheOwnersAsked: in the platform scope, which lets every owner's row through, the reads see an account's
// own key only when the caller asks for every owner: the platform's own suppliers are what PlatformOwned lists, reads
// and has waiting names of, and the account's supplier is not there at all.
func TestReadsSeeTheOwnersAsked(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	platform := a.connect("https://api.example.com", "ops@example.com").ID
	a.model(platform, a.group(platform, "g", true), "platform-name", time.Minute)
	byok := byokFixture(t, a, uuid.NewV7(), uuid.NewV7()).ID
	byokGroup := uuid.MustParse(a.text(`SELECT id::text FROM suppliers.supplier_groups WHERE supplier_id = $1`, byok))
	a.model(byok, byokGroup, "account-name", 2*time.Minute) // a supplier the sync never reads, so only a test lists one
	for _, c := range []struct {
		what   string
		owners Owners
		want   []uuid.UUID
		names  []string
		detail error
	}{
		{"PlatformOwned", PlatformOwned, []uuid.UUID{platform}, []string{"platform-name"}, ErrNoSupplier},
		{"EveryOwner", EveryOwner, []uuid.UUID{byok, platform}, []string{"account-name", "platform-name"}, nil},
	} {
		listed, _ := a.list(SupplierQuery{Owners: c.owners, PageSize: 10})
		same(t, "the suppliers of "+c.what, c.want, supplierIDs(listed))
		names, _ := a.pending(PendingQuery{Owners: c.owners, PageSize: 10})
		same(t, "the waiting names of "+c.what, c.names, namesOf(names))
		_, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Detail, error) { return a.admin.Supplier(ctx, tx, byok, c.owners) })
		refuses(t, "the account's key read for "+c.what, err, c.detail)
	}
}
