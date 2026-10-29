package catalog_test

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestPageReviewKeepsUnreviewedPriceChange(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	if err := s.observe(t.Context(), page, "input $1 date Monday"); err != nil {
		t.Fatal(err)
	}
	baseline := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page)
	review := query[uuid.UUID](s, platformScope, "SELECT id FROM catalog.review WHERE page_id=$1 AND status='open'", page)
	s.must(s.c.ReviewPage(t.Context(), catalog.ReviewPageRequest{Op: op(), Review: review, ContentSeq: 1, Status: "done"}))
	for _, text := range []string{"input $3 date Monday", "input $3 date Tuesday"} {
		if err := s.observe(t.Context(), page, text); err != nil {
			t.Fatal(err)
		}
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT previous_snapshot_id FROM catalog.review WHERE status='open'"); got != baseline {
		t.Fatal("lost original baseline", got)
	}
	if got := query[string](s, platformScope, "SELECT removed || '/' || added FROM catalog.review WHERE status='open'"); got != "1 date Mon/3 date Tues" {
		t.Fatal("lost unreviewed price change", got)
	}
	review = query[uuid.UUID](s, platformScope, "SELECT id FROM catalog.review WHERE page_id=$1 AND status='open'", page)
	err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		operation := op()
		operation.Tx = &tx
		if _, err := s.c.ReviewPage(t.Context(), catalog.ReviewPageRequest{Op: operation, Review: review, ContentSeq: 2, Status: "done"}); err != nil {
			return err
		}
		err := s.exec(platformScope, "SELECT id FROM catalog.watched_page WHERE id=$1 FOR UPDATE NOWAIT", page)
		if err == nil {
			return errors.New("watcher could revise content before review recovery committed")
		}
		if !strings.Contains(err.Error(), "could not obtain lock") {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline = query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page)
	if err := s.observe(t.Context(), page, "input $4 date Tuesday"); err != nil {
		t.Fatal(err)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT previous_snapshot_id FROM catalog.review WHERE status='open'"); got != baseline {
		t.Fatal("new review did not start at reviewed content", got)
	}
}

func TestUnwatchPageResolvesHealthSignalsAtomically(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	if err := s.c.RecordPage(t.Context(), s.claimPage(t.Context(), page), catalog.PageContent{}, errors.New("broken markers")); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), s.database.Superuser, "UPDATE catalog.watched_page SET created_at=clock_timestamp()-interval '3 hours'"); err != nil {
		t.Fatal(err)
	}
	if err := s.c.ReportOverduePages(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"catalog.page_failed", "catalog.page_overdue"} {
		if query[bool](s, platformScope, "SELECT (body->>'resolved')::bool FROM platform.alert_signals WHERE body->>'kind'=$1 ORDER BY seq DESC LIMIT 1", kind) {
			t.Fatal("expected active signal", kind)
		}
	}
	if err := pgtest.Run(t.Context(), s.database.Superuser, "REVOKE INSERT ON platform.alert_signals FROM axiom_app"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.c.UnwatchPage(t.Context(), catalog.UnwatchPageRequest{Op: op(), Page: page}); err == nil {
		t.Fatal("disabled without restoring signals")
	}
	if !query[bool](s, platformScope, "SELECT enabled FROM catalog.watched_page WHERE id=$1", page) {
		t.Fatal("partial disable committed")
	}
	if err := pgtest.Run(t.Context(), s.database.Superuser, "GRANT INSERT ON platform.alert_signals TO axiom_app"); err != nil {
		t.Fatal(err)
	}
	s.must(s.c.UnwatchPage(t.Context(), catalog.UnwatchPageRequest{Op: op(), Page: page}))
	for _, kind := range []string{"catalog.page_failed", "catalog.page_overdue"} {
		if !query[bool](s, platformScope, "SELECT (body->>'resolved')::bool FROM platform.alert_signals WHERE body->>'kind'=$1 ORDER BY seq DESC LIMIT 1", kind) {
			t.Fatal("signal remained active", kind)
		}
	}
}

func TestPageSiteRateCanRecoverAfterRobotsChanges(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	s.watch(t.Context(), "acme", "USD")
	err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		q := catalog.New()
		for _, delay := range []int64{60, 1} {
			if err := q.RegisterPageSite(t.Context(), tx, catalog.RegisterPageSiteParams{Origin: "https://example.com", DelaySeconds: delay}); err != nil {
				return err
			}
		}
		return q.DelayPageSite(t.Context(), tx, catalog.DelayPageSiteParams{Origin: "https://example.com", Seconds: 12})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := query[int64](s, platformScope, "SELECT delay_seconds FROM catalog.page_site"); got != 1 {
		t.Fatal("previous configuration or robots delay became permanent", got)
	}
	err = s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := catalog.New().ReservePageRequest(t.Context(), tx, catalog.ReservePageRequestParams{Origin: "https://example.com", Seconds: 12})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !query[bool](s, platformScope, "SELECT abs(extract(epoch FROM next_request_at-last_request_at)-12) < 0.1 FROM catalog.page_site") {
		t.Fatal("next request lost the current robots delay")
	}
	if err := s.exec(platformScope, "UPDATE catalog.page_site SET last_request_at=clock_timestamp()-interval '20 seconds', next_request_at=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	err = s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		q := catalog.New()
		if err := q.DelayPageSite(t.Context(), tx, catalog.DelayPageSiteParams{Origin: "https://example.com", Seconds: 1}); err != nil {
			return err
		}
		_, err := q.ReservePageRequest(t.Context(), tx, catalog.ReservePageRequestParams{Origin: "https://example.com", Seconds: 1})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !query[bool](s, platformScope, "SELECT abs(extract(epoch FROM next_request_at-last_request_at)-1) < 0.1 FROM catalog.page_site") {
		t.Fatal("reduced robots delay was ignored")
	}
}
