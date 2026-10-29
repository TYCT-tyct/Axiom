package accounts_test

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	axmail "axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func noticeTx(t *testing.T, db *pg.DB, f func(pg.Tx) error) {
	t.Helper()
	if err := db.Tx(t.Context(), platform, f); err != nil {
		t.Fatal(err)
	}
}

func TestUserNoticeCrossingsPreferencesAndDedupe(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	var prefs accounts.NotificationPreferences
	noticeTx(t, db, func(tx pg.Tx) (err error) {
		prefs, err = accounts.GetNotificationPreferences(t.Context(), tx, u1)
		return err
	})
	if !prefs.LowBalance || !prefs.TopUp || !prefs.FactorChanged || prefs.Threshold != 5_000_000_000 {
		t.Fatalf("defaults: %+v", prefs)
	}
	prefs.Threshold = 100
	noticeTx(t, db, func(tx pg.Tx) error { return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, prefs) })
	same := uuid.NewV7()
	for _, n := range []struct {
		id            uuid.UUID
		from, to, top int64
	}{
		{same, 100, 99, 0}, {same, 100, 99, 0}, {uuid.NewV7(), 99, 80, 0}, {uuid.NewV7(), 80, 200, 120}, {uuid.NewV7(), 200, 0, 0},
	} {
		noticeTx(t, db, func(tx pg.Tx) error {
			return accounts.RecordBalanceNotices(t.Context(), tx, []accounts.BalanceNotice{{ID: n.id, Site: s1, Account: a1, Previous: n.from, Balance: n.to, TopUp: n.top}})
		})
	}
	got := texts(t, db, platform, "SELECT kind||':'||count(*) FROM accounts.user_notices GROUP BY kind ORDER BY kind")
	if len(got) != 2 || got[0] != "low_balance:2" || got[1] != "top_up:1" {
		t.Fatalf("crossings/dedupe: %v", got)
	}
	prefs.LowBalance, prefs.TopUp = false, false
	noticeTx(t, db, func(tx pg.Tx) error { return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, prefs) })
	noticeTx(t, db, func(tx pg.Tx) error {
		return accounts.RecordBalanceNotices(t.Context(), tx, []accounts.BalanceNotice{{ID: uuid.NewV7(), Site: s1, Account: a1, Previous: 200, Balance: 0, TopUp: 5}})
	})
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.user_notices"); got[0] != "3" {
		t.Fatalf("disabled: %v", got)
	}
}

func TestFactorNoticeRollsBack(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	// A downstream rollback cannot consume the source or leave half a notice behind.
	sentinel := errors.New("rollback")
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE accounts.users SET factor_version=factor_version+1 WHERE id=$1`, u1); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.user_notices WHERE kind='factor_changed'"); got[0] != "0" {
		t.Fatalf("rollback: %v", got)
	}
}

func TestFactorNoticeAtomicForEveryWriterAndMailDedupe(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword}); err != nil {
		t.Fatal(err)
	}
	notices := texts(t, x.db, platform, "SELECT kind||':'||email_done FROM accounts.user_notices WHERE user_id='"+uuid.UUID(x.user).String()+"'")
	if len(notices) != 1 || notices[0] != "factor_changed:true" {
		t.Fatalf("password: %v", notices)
	}
	// The CLI shares this version mutation, and all intermediate versions remain durable.
	if _, err = exec(t, x.db, platform, "UPDATE accounts.users SET factor_version=factor_version+1 WHERE id='"+uuid.UUID(x.user).String()+"'", "UPDATE accounts.users SET factor_version=factor_version+1 WHERE id='"+uuid.UUID(x.user).String()+"'"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	q := x.db.NewQueue(logger, platform, nil)
	m, err := axmail.Register(t.Context(), x.db, q, axmail.Config{Host: "localhost", Port: 1, From: mail.Address{Address: "notices@example.com"}}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(t.Context()) //nolint:errcheck // Fixture queue is paused; only insertion is used.
	for range 2 {
		noticeTx(t, x.db, func(tx pg.Tx) error { _, err := accounts.DispatchUserNotices(t.Context(), tx, q, m); return err })
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox WHERE account_id='"+uuid.UUID(p.AccountID()).String()+"'"); got[0] != "3" {
		t.Fatalf("one mail per factor version, password not duplicated: %v", got)
	}
}

func TestAnnouncementsWindowWithdrawalAndTenantScope(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	active := accounts.AnnouncementID(uuid.NewV7())
	for _, a := range []accounts.Announcement{
		{ID: active, Site: s1, Title: "<script>plain text</script>", Body: "维护 🛠", StartsAt: now, EndsAt: now.Add(time.Hour)},
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Title: "future", Body: "x", StartsAt: now.Add(time.Second), EndsAt: now.Add(time.Hour)},
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Title: "expired", Body: "x", StartsAt: now.Add(-time.Hour), EndsAt: now},
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s2, Title: "other tenant", Body: "x", StartsAt: now, EndsAt: now.Add(time.Hour)},
	} {
		noticeTx(t, db, func(tx pg.Tx) error { return accounts.PublishAnnouncement(t.Context(), tx, a) })
	}
	noticeTx(t, db, func(tx pg.Tx) error {
		rows, err := accounts.ActiveAnnouncements(t.Context(), tx, s1, now)
		if err == nil && (len(rows) != 1 || rows[0].ID != active) {
			t.Fatalf("active: %+v", rows)
		}
		return err
	})
	scope := pg.NewScope(false, nil, []uuid.UUID{a2})
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := accounts.ActiveAnnouncements(t.Context(), tx, s1, now)
		if len(rows) != 0 {
			t.Fatal("cross-site announcement visible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	noticeTx(t, db, func(tx pg.Tx) error { return accounts.WithdrawAnnouncement(t.Context(), tx, active) })
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return accounts.WithdrawAnnouncement(t.Context(), tx, active) }); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("second withdrawal: %v", err)
	}
	if n, err := exec(t, db, platform, "UPDATE accounts.announcements SET withdrawn_at=NULL WHERE id='"+uuid.UUID(active).String()+"'"); err != nil || n != 0 {
		t.Fatalf("withdrawal terminal: %d %v", n, err)
	}
}

func TestNotificationPreferencesStayInTheirTenant(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	p := accounts.NotificationPreferences{LowBalance: false, TopUp: false, FactorChanged: false, Threshold: 123, Language: "zh"}
	noticeTx(t, db, func(tx pg.Tx) error { return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, p) })
	other := pg.NewScope(false, nil, []uuid.UUID{a2})
	if err := db.ReadTx(t.Context(), other, func(tx pg.Tx) error {
		got, err := accounts.GetNotificationPreferences(t.Context(), tx, u1)
		if got.Threshold == 123 || got.Language == "zh" {
			t.Fatal("other tenant preferences visible")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := db.Tx(t.Context(), other, func(tx pg.Tx) error { return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, p) })
	if state(err) != "42501" {
		t.Fatalf("other tenant wrote preferences: %v", err)
	}
}

func TestAnnouncementBoundaryValidation(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	now := time.Now()
	for _, a := range []accounts.Announcement{
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Body: "body", StartsAt: now, EndsAt: now.Add(time.Hour)},
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Title: "title", StartsAt: now, EndsAt: now.Add(time.Hour)},
		{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Title: "title", Body: "body", StartsAt: now, EndsAt: now},
	} {
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return accounts.PublishAnnouncement(t.Context(), tx, a) })
		if !errors.Is(err, accounts.ErrAnnouncement) {
			t.Fatalf("invalid announcement: %v", err)
		}
	}
	err := db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{a1}), func(tx pg.Tx) error {
		return accounts.PublishAnnouncement(t.Context(), tx, accounts.Announcement{ID: accounts.AnnouncementID(uuid.NewV7()), Site: s1, Title: "title", Body: "body", StartsAt: now, EndsAt: now.Add(time.Hour)})
	})
	if state(err) != "42501" {
		t.Fatalf("ordinary user published: %v", err)
	}
}

func TestPasswordSecurityMailCannotBeDisabled(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.UpdateNotificationPreferences(t.Context(), p, accounts.NotificationPreferences{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = x.s.ChangePassword(t.Context(), p, accounts.PasswordChange{CurrentPassword: password, NewPassword: changedPassword}); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "1" {
		t.Fatalf("mandatory password mail missing: %v", got)
	}
}

func TestPasswordResetSecurityMailCannotBeDisabled(t *testing.T) {
	t.Parallel()
	x := recoverySite(t)
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	if err = x.s.UpdateNotificationPreferences(t.Context(), p, accounts.NotificationPreferences{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if err = x.reset(t, console, x.token(t), changedPassword); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "2" {
		t.Fatalf("mandatory password mail missing: %v", got)
	}
}

func TestUserNoticesWaitForMailConfiguration(t *testing.T) {
	t.Parallel()
	db, database := open(t)
	logger := slog.New(slog.DiscardHandler)
	q := db.NewQueue(logger, platform, nil)
	disabled, err := axmail.Register(t.Context(), db, q, axmail.Config{}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exec(t, db, platform, ids.Replace(`UPDATE accounts.users SET factor_version=factor_version+1 WHERE id=:u1`)); err != nil {
		t.Fatal(err)
	}
	noticeTx(t, db, func(tx pg.Tx) error { _, err := accounts.DispatchUserNotices(t.Context(), tx, q, disabled); return err })
	if got := texts(t, db, platform, "SELECT dispatched::text FROM accounts.user_notices"); len(got) != 1 || got[0] != "false" {
		t.Fatalf("mail disabled lost notice: %v", got)
	}
	enabled, err := axmail.Register(t.Context(), db, q, axmail.Config{Host: "localhost", Port: 1, From: mail.Address{Address: "notices@example.com"}}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exec(t, db, platform, `INSERT INTO river.river_queue(name,paused_at) VALUES('notifications',now())`); err != nil {
		t.Fatal(err)
	}
	if err = q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	noticeTx(t, db, func(tx pg.Tx) error { _, err := accounts.DispatchUserNotices(t.Context(), tx, q, enabled); return err })
	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.mail_outbox"); got[0] != "1" {
		t.Fatalf("configured node did not enqueue: %v", got)
	}
	if err = pgtest.Run(t.Context(), database.Superuser, ids.Replace(`UPDATE accounts.users SET email='first,last@x.com',factor_version=factor_version+1 WHERE id=:u1`)); err != nil {
		t.Fatal(err)
	}
	noticeTx(t, db, func(tx pg.Tx) error { _, err := accounts.DispatchUserNotices(t.Context(), tx, q, enabled); return err })
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.user_notices WHERE dispatched AND email_failed"); got[0] != "1" {
		t.Fatalf("invalid legacy mailbox was not isolated: %v", got)
	}
}

func TestBalanceNoticeBatchResolvesEachTenantAndDefaults(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	noticeTx(t, db, func(tx pg.Tx) error {
		return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, accounts.NotificationPreferences{Language: "zh"})
	})
	var notices []accounts.BalanceNotice
	for range 1200 {
		for _, tenant := range [][2]uuid.UUID{{s1, a1}, {s2, a2}, {s1, uuid.NewV7()}} {
			notices = append(notices, accounts.BalanceNotice{ID: uuid.NewV7(), Site: tenant[0], Account: tenant[1], Previous: 5_000_000_000, Balance: 1, TopUp: 3})
		}
	}
	notices = append(notices, notices[:100]...)
	for range 2 {
		noticeTx(t, db, func(tx pg.Tx) error { return accounts.RecordBalanceNotices(t.Context(), tx, notices) })
	}
	got := texts(t, db, platform, "SELECT kind||':'||count(*) FROM accounts.user_notices GROUP BY kind ORDER BY kind")
	if len(got) != 2 || got[0] != "low_balance:1200" || got[1] != "top_up:1200" {
		t.Fatalf("bulk preferences/defaults/dedupe: %v", got)
	}
	if got := texts(t, db, pg.AccountScope(a1), "SELECT count(*)::text FROM accounts.user_notices"); got[0] != "0" {
		t.Fatalf("disabled tenant got notices: %v", got)
	}
}

func TestNotificationPreferencesCannotStoreNullValues(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	noticeTx(t, db, func(tx pg.Tx) error {
		return accounts.SetNotificationPreferences(t.Context(), tx, s1, a1, u1, accounts.NotificationPreferences{Language: "en"})
	})
	for _, column := range []string{"low_balance", "top_up", "factor_changed", "threshold"} {
		_, err := exec(t, db, platform, "UPDATE accounts.notification_preferences SET "+column+"=NULL")
		if state(err) != "23502" {
			t.Fatalf("%s accepted NULL: %v", column, err)
		}
	}
}
