package payments

import (
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

type siteNotice struct {
	site, account uuid.UUID
	source, kind  string
}

// ownerSite adds a main site with a personal owner account and its user, and a secondary site owned by that account.
func (w *world) ownerSite() (main, child, owner, user uuid.UUID) {
	w.t.Helper()
	main, child, owner, user = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	tierMain, tierChild := uuid.NewV7(), uuid.NewV7()
	err := w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts.sites(id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
			 VALUES($1,NULL,NULL,'Main','active','main-','invite',$2)`, []any{main, tierMain}},
			{`INSERT INTO accounts.tiers(id,site_id,name) VALUES($1,$2,'Default')`, []any{tierMain, main}},
			{
				`INSERT INTO accounts.accounts(id,site_id,kind,name,status,tier_id) VALUES($1,$2,'personal','Owner','active',$3)`,
				[]any{owner, main, tierMain},
			},
			{`INSERT INTO accounts.users(id,site_id,account_id,email,password_hash,factor_version,status,invite_code)
			 VALUES($1,$2,$3,'owner@example.com','$argon2id$test',1,'active','owner1')`, []any{user, main, owner}},
			{`INSERT INTO accounts.sites(id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
			 VALUES($1,$2,$3,'Child','active','child-','invite',$4)`, []any{child, main, owner, tierChild}},
			{`INSERT INTO accounts.tiers(id,site_id,name) VALUES($1,$2,'Default')`, []any{tierChild, child}},
			{`INSERT INTO platform.tenants(site_id,tenant_id) VALUES($1,$1),($2,$2),($2,$3)`, []any{child, main, owner}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(w.t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return main, child, owner, user
}

// notices reads a user's site notices oldest first.
func (w *world) notices(user uuid.UUID) []siteNotice {
	w.t.Helper()
	var rows []siteNotice
	err := w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		results, err := tx.Query(w.t.Context(), `SELECT site_id,account_id,source,kind FROM accounts.user_notices WHERE user_id=$1 ORDER BY id`, user)
		if err != nil {
			return err
		}
		defer results.Close()
		for results.Next() {
			var n siteNotice
			if err = results.Scan(&n.site, &n.account, &n.source, &n.kind); err != nil {
				return err
			}
			rows = append(rows, n)
		}
		return results.Err()
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return rows
}

// TestSecondarySitePaymentSaveNotifiesOwnerOncePerChange: every save of a secondary site's merchant account inserts one
// notice for the owner account's user, in the save's transaction, and a main site inserts none.
func TestSecondarySitePaymentSaveNotifiesOwnerOncePerChange(t *testing.T) {
	w := newWorld(t)
	main, child, owner, user := w.ownerSite()
	in := w.input(child)
	m := w.mustSave(pg.PlatformScope(), in)
	notices := w.notices(user)
	if len(notices) != 1 || notices[0].kind != "site_payment_changed" || notices[0].site != main || notices[0].account != owner {
		t.Fatalf("notices %+v", notices)
	}
	in.ID, in.Rev = MerchantID(m.ID), m.Rev
	w.mustSave(pg.PlatformScope(), in)
	notices = w.notices(user)
	if len(notices) != 2 || notices[0].source == notices[1].source {
		t.Fatalf("a second change did not make a second notice: %+v", notices)
	}
	w.mustSave(pg.PlatformScope(), w.input(main))
	if got := w.notices(user); len(got) != 2 {
		t.Fatalf("a main site's change notified the owner: %+v", got)
	}
}

// TestPaymentSaveRollsBackWithItsNotice: the notice and the change are one transaction in both directions. A failing
// audit leaves no notice, and a failing notice leaves no merchant and no audit.
func TestPaymentSaveRollsBackWithItsNotice(t *testing.T) {
	t.Run("audit", func(t *testing.T) {
		w := newWorld(t)
		_, child, _, user := w.ownerSite()
		w.exec(`CREATE FUNCTION payments.refuse_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`,
			`CREATE TRIGGER refuse_audit BEFORE INSERT ON platform.audit_log FOR EACH ROW EXECUTE FUNCTION payments.refuse_audit()`)
		if _, err := w.save(pg.PlatformScope(), w.input(child)); err == nil {
			t.Fatal("the save succeeded although its audit failed")
		}
		if got := w.notices(user); len(got) != 0 {
			t.Fatalf("the failed save left a notice: %+v", got)
		}
		if n := w.count(pg.PlatformScope(), "SELECT count(*) FROM payments.merchants WHERE site_id=$1", child); n != 0 {
			t.Fatalf("the failed save left %d merchants", n)
		}
	})
	t.Run("notice", func(t *testing.T) {
		w := newWorld(t)
		_, child, _, _ := w.ownerSite()
		w.exec(`CREATE FUNCTION payments.refuse_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`,
			`CREATE TRIGGER refuse_notice BEFORE INSERT ON accounts.user_notices FOR EACH ROW EXECUTE FUNCTION payments.refuse_notice()`)
		if _, err := w.save(pg.PlatformScope(), w.input(child)); err == nil {
			t.Fatal("the save succeeded although its notice failed")
		}
		if n := w.count(pg.PlatformScope(), "SELECT count(*) FROM payments.merchants WHERE site_id=$1", child); n != 0 {
			t.Fatalf("the failed save left %d merchants", n)
		}
		if n := w.count(pg.PlatformScope(), "SELECT count(*) FROM platform.audit_log WHERE action='merchant.create' AND target_id<>''"); n != 0 {
			t.Fatalf("the failed save left %d audits", n)
		}
	})
}
