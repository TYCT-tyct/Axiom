package payments

import (
	"log/slog"
	"net/mail"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

type siteNotice struct {
	site, account        uuid.UUID
	source, kind, detail string
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

// addChild adds a second secondary site under the same owner account.
func (w *world) addChild(main, owner uuid.UUID, name, prefix string) uuid.UUID {
	w.t.Helper()
	child, tier := uuid.NewV7(), uuid.NewV7()
	err := w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts.sites(id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
			 VALUES($1,$2,$3,$4,'active',$5,'invite',$6)`, []any{child, main, owner, name, prefix, tier}},
			{`INSERT INTO accounts.tiers(id,site_id,name) VALUES($1,$2,'Default')`, []any{tier, child}},
			{`INSERT INTO platform.tenants(site_id,tenant_id) VALUES($1,$1)`, []any{child}},
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
	return child
}

// notices reads a user's site notices oldest first.
func (w *world) notices(user uuid.UUID) []siteNotice {
	w.t.Helper()
	var rows []siteNotice
	err := w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		results, err := tx.Query(w.t.Context(), `SELECT site_id,account_id,source,kind,detail FROM accounts.user_notices WHERE user_id=$1 ORDER BY id`, user)
		if err != nil {
			return err
		}
		defer results.Close()
		for results.Next() {
			var n siteNotice
			if err = results.Scan(&n.site, &n.account, &n.source, &n.kind, &n.detail); err != nil {
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
// notice for the owner account's user, naming that site, in the save's transaction; a main site inserts none.
func TestSecondarySitePaymentSaveNotifiesOwnerOncePerChange(t *testing.T) {
	w := newWorld(t)
	main, child, owner, user := w.ownerSite()
	in := w.input(child)
	m := w.mustSave(pg.PlatformScope(), in)
	notices := w.notices(user)
	if len(notices) != 1 || notices[0].kind != "site_payment_changed" || notices[0].site != main || notices[0].account != owner || notices[0].detail != "Child" {
		t.Fatalf("notices %+v", notices)
	}
	in.ID, in.Rev = MerchantID(m.ID), m.Rev
	w.mustSave(pg.PlatformScope(), in)
	notices = w.notices(user)
	if len(notices) != 2 || notices[0].source == notices[1].source || notices[1].detail != "Child" {
		t.Fatalf("a second change did not make a second notice: %+v", notices)
	}
	w.mustSave(pg.PlatformScope(), w.input(main))
	if got := w.notices(user); len(got) != 2 {
		t.Fatalf("a main site's change notified the owner: %+v", got)
	}
}

// TestSitePaymentNoticeNamesTheChangedChild: one owner with two secondary sites gets a notice that names the site that
// changed, through the real dispatcher and mail outbox, not just a row with the parent site.
func TestSitePaymentNoticeNamesTheChangedChild(t *testing.T) {
	w := newWorld(t)
	main, childA, owner, user := w.ownerSite()
	childB := w.addChild(main, owner, "Child B", "childb-")
	w.mustSave(pg.PlatformScope(), w.input(childA))
	w.mustSave(pg.PlatformScope(), w.input(childB))
	notices := w.notices(user)
	details := map[string]bool{}
	for _, n := range notices {
		details[n.detail] = true
	}
	if len(notices) != 2 || !details["Child"] || !details["Child B"] {
		t.Fatalf("notices %+v", notices)
	}
	logger := slog.New(slog.DiscardHandler)
	q := w.p.db.NewQueue(logger, pg.PlatformScope(), nil)
	m, err := axmail.Register(t.Context(), w.p.db, q, axmail.Config{
		Host: "localhost", Port: 1, From: mail.Address{Address: "notices@example.com"},
	}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(t.Context()) //nolint:errcheck // the test only inserts into the outbox.
	if err = w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := accounts.DispatchUserNotices(t.Context(), tx, q, m)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got []string
	err = w.tx(pg.PlatformScope(), func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT subject||'|'||text_body FROM platform.mail_outbox ORDER BY created_at,id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				return err
			}
			got = append(got, line)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	var seenChildA, seenChildB bool
	for _, line := range got {
		if strings.Contains(line, "Site: Child B") {
			seenChildB = true
		} else if strings.Contains(line, "Site: Child\n") {
			seenChildA = true
		}
	}
	if len(got) != 2 || !seenChildA || !seenChildB {
		t.Fatalf("mail %+v", got)
	}
}

// TestPaymentSaveRollsBackWithItsNotice: the notice and the change are one transaction in both directions. A failing
// audit leaves no notice, and a failing notice leaves no merchant and no audit.
func TestPaymentSaveRollsBackWithItsNotice(t *testing.T) {
	t.Run("audit", func(t *testing.T) {
		w := newWorld(t)
		_, child, _, user := w.ownerSite()
		w.exec(`CREATE FUNCTION payments.refuse_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit refused'; END $$`,
			`CREATE TRIGGER refuse_audit BEFORE INSERT ON platform.audit_log FOR EACH ROW EXECUTE FUNCTION payments.refuse_audit()`)
		if _, err := w.save(pg.PlatformScope(), w.input(child)); err == nil {
			t.Fatal("the save succeeded")
		}
		if got := w.notices(user); len(got) != 0 {
			t.Fatalf("a failed audit left a notice: %+v", got)
		}
		if n := w.count(pg.PlatformScope(), `SELECT count(*) FROM payments.merchant_configs`); n != 0 {
			t.Fatalf("a failed audit left a merchant: %d", n)
		}
	})
	t.Run("notice", func(t *testing.T) {
		w := newWorld(t)
		_, child, _, _ := w.ownerSite()
		w.exec(`CREATE FUNCTION payments.refuse_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'notice refused'; END $$`,
			`CREATE TRIGGER refuse_notice BEFORE INSERT ON accounts.user_notices FOR EACH ROW EXECUTE FUNCTION payments.refuse_notice()`)
		if _, err := w.save(pg.PlatformScope(), w.input(child)); err == nil {
			t.Fatal("the save succeeded")
		}
		if n := w.count(pg.PlatformScope(), `SELECT count(*) FROM payments.merchant_configs`); n != 0 {
			t.Fatalf("a failed notice left a merchant: %d", n)
		}
		if n := w.count(pg.PlatformScope(), `SELECT count(*) FROM platform.audit_log WHERE action='merchant.create' AND target_id<>''`); n != 0 {
			t.Fatalf("a failed notice left an audit: %d", n)
		}
	})
}
