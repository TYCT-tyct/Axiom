package catalog_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func (s *store) watch(ctx context.Context, author, currency string) uuid.UUID {
	s.t.Helper()
	r := s.must(s.c.WatchPage(ctx, catalog.WatchPageRequest{
		Op:     op(),
		Author: author, URL: "https://example.com/" + uuid.NewV7().String(), Currency: currency, SectionStart: "Start", SectionEnd: "End", RateSeconds: 1,
	}))
	id, err := uuid.Parse(r.Subject)
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *store) claimPage(ctx context.Context, id uuid.UUID) catalog.CatalogWatchedPage {
	s.t.Helper()
	var page catalog.CatalogWatchedPage
	if err := s.db.Tx(ctx, platformScope, func(tx pg.Tx) (err error) {
		if _, err := tx.Exec(ctx, "UPDATE catalog.watched_page SET next_check_at = clock_timestamp() - interval '1 minute' WHERE id = $1", id); err != nil {
			return err
		}
		page, err = catalog.New().ClaimWatchedPage(ctx, tx)
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	if page.ID != id {
		s.t.Fatalf("claimed %s, want %s", page.ID, id)
	}
	return page
}

func (s *store) observe(ctx context.Context, id uuid.UUID, text string) error {
	s.t.Helper()
	return s.c.RecordPage(ctx, s.claimPage(ctx, id), catalog.PageContent{Text: text, SHA256: sha256.Sum256([]byte(text))}, nil)
}

func (s *store) officialEvidence(ctx context.Context, author, currency string) jsonx.Raw {
	s.t.Helper()
	id := s.watch(ctx, author, currency)
	if err := s.observe(ctx, id, "alpha input $1 output $2"); err != nil {
		s.t.Fatal(err)
	}
	var snapshot uuid.UUID
	if err := s.db.ReadTx(ctx, platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id = $1", id).Scan(&snapshot)
	}); err != nil {
		s.t.Fatal(err)
	}
	body, err := jsonx.MarshalCanonical(catalog.PageEvidence{PageID: id, SnapshotID: snapshot, Excerpts: []string{"input $1"}})
	if err != nil {
		s.t.Fatal(err)
	}
	return body
}

func TestPageHistoryReviewsAndSignalsCommitTogether(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	model, _ := s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	for _, text := range []string{"alpha $1", "alpha $1", "alpha $2", "alpha $1"} {
		if err := s.observe(t.Context(), page, text); err != nil {
			t.Fatal(err)
		}
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot"); got != 2 {
		t.Fatal("snapshot reuse", got)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_observation"); got != 3 {
		t.Fatal("observations", got)
	}
	if got := query[int](s, platformScope, "SELECT content_seq FROM catalog.review WHERE status='open'"); got != 3 {
		t.Fatal("review updates", got)
	}
	if got := query[string](s, platformScope, "SELECT removed || '/' || added FROM catalog.review WHERE status='open'"); got != "/alpha $1" {
		t.Fatal("diff", got)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='catalog.page_changed'"); got != 3 {
		t.Fatal("change signals", got)
	}
	if got := s.priceCount(model); got != 1 {
		t.Fatal("watcher changed reference prices", got)
	}
	if ok := query[bool](s, platformScope, `SELECT next_check_at >= last_ok_at + interval_seconds * interval '1 second'
        AND next_check_at <= last_ok_at + interval_seconds * interval '1.1 seconds' + interval '1 second'
        FROM catalog.watched_page WHERE id=$1`, page); !ok {
		t.Fatal("next check violates the page interval and positive jitter")
	}
	verifyPageFailureAndRollback(t, s, page)
}

func verifyPageFailureAndRollback(t *testing.T, s *store, page uuid.UUID) {
	t.Helper()
	current := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page)
	if err := s.c.RecordPage(t.Context(), s.claimPage(t.Context(), page), catalog.PageContent{}, errors.New("network failed")); err != nil {
		t.Fatal(err)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != current {
		t.Fatal("failure replaced evidence")
	}
	if got := query[int](s, platformScope, "SELECT failures FROM catalog.watched_page WHERE id=$1", page); got != 1 {
		t.Fatal("failure streak", got)
	}
	if err := pgtest.Run(t.Context(), s.database.Superuser, "REVOKE INSERT ON platform.alert_signals FROM axiom_app"); err != nil {
		t.Fatal(err)
	}
	if err := s.observe(t.Context(), page, "alpha $3"); err == nil {
		t.Fatal("expected signal persistence failure")
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot"); got != 2 {
		t.Fatal("partial snapshot committed", got)
	}
	if got := query[int](s, platformScope, "SELECT content_seq FROM catalog.review WHERE status='open'"); got != 3 {
		t.Fatal("partial review committed", got)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != current {
		t.Fatal("partial current pointer committed")
	}
}

func TestExpiredAndDisabledPageWorkersCannotWrite(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.must(s.c.SaveAuthor(t.Context(), catalog.SaveAuthorRequest{Op: op(), Slug: "acme", Name: "Acme"}))
	page := s.watch(t.Context(), "acme", "USD")
	old := s.claimPage(t.Context(), page)
	if err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { _, err := catalog.New().ClaimWatchedPage(t.Context(), tx); return err }); !errors.Is(err, pg.ErrNoRows) {
		t.Fatalf("lease was claimed twice: %v", err)
	}
	if err := s.exec(platformScope, "UPDATE catalog.watched_page SET lease_until='-infinity' WHERE id=$1", page); err != nil {
		t.Fatal(err)
	}
	replacement := s.claimPage(t.Context(), page)
	content := catalog.PageContent{Text: "alpha $1", SHA256: sha256.Sum256([]byte("alpha $1"))}
	if err := s.c.RecordPage(t.Context(), old, content, nil); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot"); got != 0 {
		t.Fatal("expired generation wrote evidence")
	}
	if err := s.c.RecordPage(t.Context(), replacement, content, nil); err != nil {
		t.Fatal(err)
	}
	// A duplicated completion after the lease was released must also be harmless.
	if err := s.c.RecordPage(t.Context(), replacement, content, nil); err != nil {
		t.Fatal(err)
	}
	replacement = s.claimPage(t.Context(), page)
	s.must(s.c.UnwatchPage(t.Context(), catalog.UnwatchPageRequest{Op: op(), Page: page}))
	if err := s.c.RecordPage(t.Context(), replacement, content, nil); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot"); got != 1 {
		t.Fatal("disabled page wrote evidence")
	}
	for _, table := range []string{"page_site", "watched_page", "page_snapshot", "page_observation", "review"} {
		if got := query[int](s, pg.NewScope(false, nil, nil), "SELECT count(*) FROM catalog."+table); got != 0 {
			t.Fatalf("unscoped access to %s", table)
		}
	}
}

func TestPageReviewRejectsStaleResolutionAndRemembersDismissals(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	if err := s.observe(t.Context(), page, "alpha $1"); err != nil {
		t.Fatal(err)
	}
	review := query[uuid.UUID](s, platformScope, "SELECT id FROM catalog.review WHERE page_id=$1", page)
	if err := s.observe(t.Context(), page, "alpha $2"); err != nil {
		t.Fatal(err)
	}
	_, err := s.c.ReviewPage(t.Context(), catalog.ReviewPageRequest{Op: op(), Review: review, ContentSeq: 1, Status: "dismissed"})
	if !errors.Is(err, catalog.ErrStaleRev) {
		t.Fatalf("stale resolution: %v", err)
	}
	s.must(s.c.ReviewPage(t.Context(), catalog.ReviewPageRequest{Op: op(), Review: review, ContentSeq: 2, Status: "dismissed"}))
	for _, text := range []string{"alpha $1", "alpha $2"} {
		if err := s.observe(t.Context(), page, text); err != nil {
			t.Fatal(err)
		}
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.review WHERE status='open'"); got != 0 {
		t.Fatal("dismissed content reopened or left a stale review")
	}
}

func TestOfficialEvidenceMustMatchCurrentPageAuthorAndCurrency(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	good := s.officialEvidence(t.Context(), "acme", "USD")
	var evidence catalog.PageEvidence
	if err := jsonx.Unmarshal(good, &evidence); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*catalog.PageEvidence)
	}{
		{"stale snapshot", func(e *catalog.PageEvidence) { e.SnapshotID = uuid.NewV7() }},
		{"missing page", func(e *catalog.PageEvidence) { e.PageID = uuid.NewV7() }},
		{"empty excerpts", func(e *catalog.PageEvidence) { e.Excerpts = nil }},
		{"blank excerpt", func(e *catalog.PageEvidence) { e.Excerpts = []string{" "} }},
		{"invented excerpt", func(e *catalog.PageEvidence) { e.Excerpts = []string{"input $9"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := evidence
			tc.edit(&changed)
			body, err := jsonx.MarshalCanonical(changed)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
				Op: op(), Model: id, Rev: 0, Source: catalog.SourceOfficial,
				Bases: s.bases(t.Context(), id), Prices: []catalog.SourcePrice{{Schedule: raw(basic), Evidence: body}},
			})
			if !errors.Is(err, catalog.ErrNoEvidence) {
				t.Fatalf("accepted evidence: %v", err)
			}
		})
	}
	_, err := s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
		Op: op(), Model: id, Rev: 0, Source: catalog.SourceOfficial,
		Bases: s.bases(t.Context(), id), Prices: []catalog.SourcePrice{{Schedule: raw(basicCNY), Evidence: good}},
	})
	if !errors.Is(err, catalog.ErrNoEvidence) {
		t.Fatalf("cross-currency evidence: %v", err)
	}
	other, _ := s.seed("other/alpha", basicSpec, basic)
	_, err = s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
		Op: op(), Model: other, Rev: 0, Source: catalog.SourceOfficial,
		Bases: s.bases(t.Context(), other), Prices: []catalog.SourcePrice{{Schedule: raw(basic), Evidence: good}},
	})
	if !errors.Is(err, catalog.ErrNoEvidence) {
		t.Fatalf("cross-author evidence: %v", err)
	}
	s.must(s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
		Op: op(), Model: id, Rev: 0, Source: catalog.SourceOfficial,
		Bases: s.bases(t.Context(), id), Prices: []catalog.SourcePrice{{Schedule: raw(basic), Evidence: good}},
	}))
	if err := s.observe(t.Context(), evidence.PageID, "alpha input $3 output $4"); err != nil {
		t.Fatal(err)
	}
	head := s.head(id, "USD")
	_, err = s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{Op: op(), Model: id, Source: catalog.SourceOfficial, Base: &head, Schedule: raw(basic), Evidence: good})
	if !errors.Is(err, catalog.ErrNoEvidence) {
		t.Fatalf("published obsolete snapshot: %v", err)
	}
	checks, err := s.c.Check(t.Context(), []string{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !containsProblem(checks[0].Problems, "official snapshot") {
		t.Fatalf("check missed stale evidence: %+v", checks)
	}
}

func containsProblem(problems []string, part string) bool {
	for _, p := range problems {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

func TestPageJobStartsOnQueueAndCancelsInflightFetch(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	requested := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		close(requested)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer client.CloseIdleConnections()
	s.must(s.c.WatchPage(t.Context(), catalog.WatchPageRequest{
		Op:     op(),
		Author: "acme", URL: server.URL + "/price", Currency: "USD", SectionStart: "Start", SectionEnd: "End", RateSeconds: 1,
	}))
	queue := s.db.NewQueue(slog.Default(), platformScope, nil)
	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := catalog.RegisterPages(lifetime, catalog.PageJobConfig{DB: s.db, Scope: platformScope, Queue: queue, Client: client, Logger: slog.Default()}); err != nil {
		t.Fatal(err)
	}
	startQueue(t, queue)
	select {
	case <-requested:
	case <-time.After(30 * time.Second):
		t.Fatal("no startup page fetch")
	}
	if got := query[string](s, platformScope, "SELECT queue FROM river.river_job WHERE kind='catalog.pages'"); got != pg.QueueSyncs {
		t.Fatal("watcher ran on", got)
	}
	if got := query[int64](s, platformScope, "SELECT delay_seconds FROM catalog.page_site WHERE origin=$1", server.URL); got != 1 {
		t.Fatal("worker replaced the registered site rate with a default", got)
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("node cancellation did not cancel egress")
	}
	if err := queue.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !query[bool](s, platformScope, "SELECT failures=0 AND NOT EXISTS (SELECT FROM platform.alert_signals WHERE body->>'kind'='catalog.page_failed') FROM catalog.watched_page") {
		t.Fatal("node cancellation counted as page failure or emitted a failure signal")
	}
	if !query[bool](s, platformScope, "SELECT next_check_at <= clock_timestamp() AND lease_until > clock_timestamp() FROM catalog.watched_page") {
		t.Fatal("node cancellation deferred the next check or released its lease")
	}
}

func TestPageSiteReservationsAndOverdueSignalsAreShared(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		q := catalog.New()
		if _, err := q.ReservePageRequest(t.Context(), tx, catalog.ReservePageRequestParams{Origin: "https://example.com", Seconds: 1}); err != nil {
			return err
		}
		_, err := q.ReservePageRequest(t.Context(), tx, catalog.ReservePageRequestParams{Origin: "https://example.com", Seconds: 1})
		if !errors.Is(err, pg.ErrNoRows) {
			return fmt.Errorf("second request was admitted: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.exec(platformScope, "UPDATE catalog.watched_page SET created_at=clock_timestamp()-interval '3 hours' WHERE id=$1", page); err == nil {
		t.Fatal("application rewrote registration creation time")
	}
	if err := pgtest.Run(t.Context(), s.database.Superuser, "UPDATE catalog.watched_page SET created_at=clock_timestamp()-interval '3 hours'"); err != nil {
		t.Fatal(err)
	}
	if err := s.c.ReportOverduePages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM platform.alert_signals WHERE body->>'kind'='catalog.page_overdue'"); got != 1 {
		t.Fatal("no overdue signal", got)
	}
	if err := s.observe(t.Context(), page, "alpha $1"); err != nil {
		t.Fatal(err)
	}
	if got := query[bool](s, platformScope, "SELECT (body->>'resolved')::bool FROM platform.alert_signals WHERE body->>'kind'='catalog.page_overdue' ORDER BY seq DESC LIMIT 1"); !got {
		t.Fatal("success did not resolve overdue")
	}
}

func TestDisabledPageCanBeReplacedWithNewExtractionScope(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	address := query[string](s, platformScope, "SELECT url FROM catalog.watched_page WHERE id=$1", page)
	request := catalog.WatchPageRequest{Op: op(), Author: "acme", URL: address, Currency: "USD", SectionStart: "New start", SectionEnd: "New end"}
	if _, err := s.c.WatchPage(t.Context(), request); err == nil {
		t.Fatal("two active registrations share a URL")
	}
	s.must(s.c.UnwatchPage(t.Context(), catalog.UnwatchPageRequest{Op: op(), Page: page}))
	s.must(s.c.WatchPage(t.Context(), request))
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.watched_page WHERE enabled"); got != 1 {
		t.Fatal("replacement registration", got)
	}
}

func TestOfficialPublishHoldsCurrentPageUntilCommit(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	model, _ := s.seed("acme/alpha", basicSpec, basic)
	evidence := s.officialEvidence(t.Context(), "acme", "USD")
	var page catalog.PageEvidence
	if err := jsonx.Unmarshal(evidence, &page); err != nil {
		t.Fatal(err)
	}
	s.must(s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{Op: op(), Model: model, Rev: 0, Source: catalog.SourceOfficial, Bases: s.bases(t.Context(), model), Prices: []catalog.SourcePrice{{Schedule: raw(basic), Evidence: evidence}}}))
	head := s.head(model, "USD")
	err := s.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		operation := op()
		operation.Tx = &tx
		if _, err := s.c.PublishPrice(t.Context(), catalog.PublishPriceRequest{Op: operation, Model: model, Base: &head, Source: catalog.SourceOfficial, Schedule: raw(basic), Evidence: evidence}); err != nil {
			return err
		}
		err := s.exec(platformScope, "SELECT id FROM catalog.watched_page WHERE id=$1 FOR UPDATE NOWAIT", page.PageID)
		if err == nil {
			return errors.New("watcher could replace the current evidence before publish committed")
		}
		if !strings.Contains(err.Error(), "could not obtain lock") {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.exec(platformScope, "SELECT id FROM catalog.watched_page WHERE id=$1 FOR UPDATE NOWAIT", page.PageID); err != nil {
		t.Fatal("page lock survived commit", err)
	}
}

func TestDisabledPageCannotSupportNewOfficialPrices(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	model, _ := s.seed("acme/alpha", basicSpec, basic)
	evidence := s.officialEvidence(t.Context(), "acme", "USD")
	var page catalog.PageEvidence
	if err := jsonx.Unmarshal(evidence, &page); err != nil {
		t.Fatal(err)
	}
	s.must(s.c.UnwatchPage(t.Context(), catalog.UnwatchPageRequest{Op: op(), Page: page.PageID}))
	_, err := s.c.SelectPriceSource(t.Context(), catalog.SelectPriceSourceRequest{
		Op: op(), Model: model, Rev: 0, Source: catalog.SourceOfficial,
		Bases: s.bases(t.Context(), model), Prices: []catalog.SourcePrice{{Schedule: raw(basic), Evidence: evidence}},
	})
	if !errors.Is(err, catalog.ErrNoEvidence) {
		t.Fatalf("disabled evidence accepted: %v", err)
	}
}
