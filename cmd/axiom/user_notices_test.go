package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/riverqueue/river"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
)

func TestNotificationPreferencesAPIIsolationAndValidation(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	expect(t, "signed out", f.do(http.MethodGet, "/notification-preferences", ""), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	r := f.do(http.MethodGet, "/notification-preferences", "", as(s))
	expect(t, "defaults", r, http.StatusOK, "")
	var p consoleapi.NotificationPreferences
	decode(t, r, &p)
	if p.Threshold == nil || *p.Threshold != "5000000000" {
		t.Fatalf("defaults: %s", r.Body)
	}
	body := `{"low_balance":false,"top_up":false,"factor_changed":false,"threshold":"0","webhook_url":"","language":"zh"}`
	expect(t, "replace", f.do(http.MethodPut, "/notification-preferences", body, as(s)), http.StatusOK, "")
	r = f.do(http.MethodGet, "/notification-preferences", "", as(s))
	decode(t, r, &p)
	if p.FactorChanged == nil || *p.FactorChanged || p.Threshold == nil || *p.Threshold != "0" {
		t.Fatalf("saved: %s", r.Body)
	}
	for _, url := range []string{"http://example.com/", "https://127.0.0.1/", "https://user:password@example.com/"} {
		input := fmt.Sprintf(`{"low_balance":true,"top_up":true,"factor_changed":true,"threshold":"0","webhook_url":%q,"language":"en"}`, url)
		expect(t, "invalid destination", f.do(http.MethodPut, "/notification-preferences", input, as(s)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
	}
	expect(t, "threshold overflow", f.do(http.MethodPut, "/notification-preferences", `{"low_balance":true,"top_up":true,"factor_changed":true,"threshold":"9999999999999999999","webhook_url":"","language":"en"}`, as(s)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeUnprocessable)
}

func TestAnnouncementPublishReadWithdrawAndReplay(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	site, ok := f.svc.MainSite()
	if !ok {
		t.Fatal("no main site")
	}
	now := time.Now().UTC()
	body := fmt.Sprintf(`{"site_id":%q,"title":"Maintenance","body":"Plain <b>text</b>","starts_at":%q,"ends_at":%q,"reason":"test publication"}`, site.ID.TypeID(), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	key := newKey()
	r := f.do(http.MethodPost, "/announcements", body, as(s), idempotent(key))
	expectAdmin(t, "publish", r, http.StatusCreated, "")
	var a adminapi.Announcement
	decode(t, r, &a)
	if a.Id == nil {
		t.Fatalf("no ID: %s", r.Body)
	}
	again := f.do(http.MethodPost, "/announcements", body, as(s), idempotent(key))
	expectAdmin(t, "replay", again, http.StatusCreated, "")
	if again.Body.String() != r.Body.String() {
		t.Fatal("replay changed announcement")
	}
	f.addUser("reader@example.com", "")
	console := startNode(f.commandLine, &testClock{now: now}, 0)
	signedIn := console.do(http.MethodPost, "/auth/sign-in", `{"email":"reader@example.com","password":"`+ownerPassword+`"}`)
	expect(t, "reader sign-in", signedIn, http.StatusOK, "")
	user := console.sessionOf(signedIn)
	r = console.do(http.MethodGet, "/announcements", "", as(user))
	expect(t, "read", r, http.StatusOK, "")
	var list consoleapi.AnnouncementList
	decode(t, r, &list)
	if list.Data == nil || len(*list.Data) != 1 || (*list.Data)[0].Body == nil || *(*list.Data)[0].Body != "Plain <b>text</b>" {
		t.Fatalf("active: %s", r.Body)
	}
	if _, err := accounts.ParseAnnouncementID(*a.Id); err != nil {
		t.Fatalf("announcement TypeID: %v", err)
	}
	if diff := cmp.Diff([]*string{new(site.ID.TypeID()), a.Id}, []*string{a.SiteId, (*list.Data)[0].Id}); diff != "" {
		t.Fatalf("site/console IDs (-want +got): %s", diff)
	}
	withdraw := "/announcements/" + *a.Id + "/withdraw"
	wkey := newKey()
	expectAdmin(t, "withdraw", f.do(http.MethodPost, withdraw, `{"reason":"finished"}`, as(s), idempotent(wkey)), http.StatusCreated, "")
	expectAdmin(t, "withdraw replay", f.do(http.MethodPost, withdraw, `{"reason":"finished"}`, as(s), idempotent(wkey)), http.StatusCreated, "")
	r = console.do(http.MethodGet, "/announcements", "", as(user))
	decode(t, r, &list)
	if list.Data == nil || len(*list.Data) != 0 {
		t.Fatalf("withdrawn visible: %s", r.Body)
	}
}

func TestUserNoticeEvaluationDoesNotAccumulateActiveJobs(t *testing.T) {
	f := newConsoleFixture(t)
	logger := slog.New(slog.DiscardHandler)
	q := f.db.NewQueue(logger, pg.PlatformScope(), nil)
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(func(context.Context, *river.Job[userNoticeArgs]) error { return nil })); err != nil {
		t.Fatal(err)
	}
	f.run(`INSERT INTO river.river_queue(name,paused_at) VALUES('notifications',now()) ON CONFLICT(name) DO UPDATE SET paused_at=now()`)
	if err := q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	inserted := 0
	err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		for range 2 {
			n, err := q.InsertManyTx(t.Context(), tx, []river.InsertManyParams{{Args: userNoticeArgs{}}})
			if err != nil {
				return err
			}
			inserted += n
		}
		return nil
	})
	if err != nil || inserted != 1 {
		t.Fatalf("active evaluation jobs: inserted=%d err=%v", inserted, err)
	}
}
