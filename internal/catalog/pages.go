package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

// WatchPageRequest registers an immutable extraction scope, after the operator reviews the site's access terms.
type WatchPageRequest struct {
	Op
	PageTarget
}

// WatchPage records the allowlisted URL and its extraction scope. Changing either requires a new registration.
func (c *Catalog) WatchPage(ctx context.Context, r WatchPageRequest) (Result, error) {
	origin, err := r.validate()
	if err != nil {
		return Result{}, err
	}
	return c.apply(ctx, r.Op, "page.watch", func(tx pg.Tx) (outcome, error) {
		q := New()
		if err := q.RegisterPageSite(ctx, tx, RegisterPageSiteParams{Origin: origin, DelaySeconds: r.RateSeconds}); err != nil {
			return outcome{}, err
		}
		id, err := q.InsertWatchedPage(ctx, tx, InsertWatchedPageParams{
			Author: r.Author, Url: r.URL, Origin: origin, Currency: r.Currency,
			SectionStart: r.SectionStart, SectionEnd: r.SectionEnd, IntervalSeconds: r.IntervalSeconds,
		})
		return outcome{targetKind: "page", subject: id.String(), after: r.PageTarget}, err
	})
}

// UnwatchPageRequest stops new reads and fences any worker that already fetched the old registration.
type UnwatchPageRequest struct {
	Op
	Page uuid.UUID `json:"page"`
}

// UnwatchPage preserves the last evidence and all history.
func (c *Catalog) UnwatchPage(ctx context.Context, r UnwatchPageRequest) (Result, error) {
	return c.apply(ctx, r.Op, "page.unwatch", func(tx pg.Tx) (outcome, error) {
		id, err := New().DisableWatchedPage(ctx, tx, r.Page)
		if err != nil {
			return outcome{}, err
		}
		if err := pageSignal(ctx, tx, "catalog.page_failed", id, true); err != nil {
			return outcome{}, err
		}
		err = pageSignal(ctx, tx, "catalog.page_overdue", id, true)
		return outcome{targetKind: "page", subject: id.String(), after: map[string]bool{"enabled": false}}, err
	})
}

// ReviewPageRequest binds a resolution to the exact review content the operator read.
type ReviewPageRequest struct {
	Op
	Review     uuid.UUID `json:"review"`
	ContentSeq int64     `json:"content_seq"`
	Status     string    `json:"status"`
}

// ReviewPage resolves a page review without publishing prices.
func (c *Catalog) ReviewPage(ctx context.Context, r ReviewPageRequest) (Result, error) {
	if r.Status != "done" && r.Status != "dismissed" {
		return Result{}, ErrInvalid
	}
	return c.apply(ctx, r.Op, "page.review", func(tx pg.Tx) (outcome, error) {
		// Share the watcher's page-first lock order so a new revision cannot race the recovery signal.
		page, err := New().LockPageForReview(ctx, tx, r.Review)
		if errors.Is(err, pg.ErrNoRows) {
			return outcome{}, ErrStaleRev
		}
		if err != nil {
			return outcome{}, err
		}
		_, err = New().ResolvePageReview(ctx, tx, ResolvePageReviewParams{ID: r.Review, ContentSeq: r.ContentSeq, Status: r.Status, Resolution: &r.Reason})
		if errors.Is(err, pg.ErrNoRows) {
			return outcome{}, ErrStaleRev
		}
		if err != nil {
			return outcome{}, err
		}
		err = pageSignal(ctx, tx, "catalog.page_changed", page, true)
		return outcome{targetKind: "page", subject: page.String(), after: r.Status}, err
	})
}

func (c *Catalog) reservePageRequest(ctx context.Context, origin string, delay time.Duration) error {
	for {
		var wait float64
		var reserved bool
		err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) (err error) {
			wait, reserved, err = reservePageRequestIn(ctx, tx, origin, delay)
			return err
		})
		if err != nil {
			return err
		}
		if reserved {
			return nil
		}
		if err := waitPage(ctx, time.Duration(wait*float64(time.Second))); err != nil {
			return err
		}
	}
}

func (c *Catalog) backoffPageSite(ctx context.Context, origin string, delay time.Duration) error {
	return c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		return New().BackoffPageSite(ctx, tx, BackoffPageSiteParams{Origin: origin, Seconds: int64(math.Ceil(delay.Seconds()))})
	})
}

func pageSignal(ctx context.Context, tx pg.Tx, kind string, page uuid.UUID, resolved bool) error {
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: kind, Key: page.String(), Severity: "warning", Resolved: resolved})
}

func nextPageCheck(interval int64) int64 { return interval + rand.Int64N(interval/10+1) } //nolint:gosec // scheduling jitter, not a secret

func (c *Catalog) recordPage(ctx context.Context, page CatalogWatchedPage, content PageContent, fetchErr error) error {
	return c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		q := New()
		current, err := q.LockWatchedPage(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		// Generation fences both an expired worker and a registration disabled during a download.
		var live bool
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp() < $1", current.LeaseUntil).Scan(&live); err != nil {
			return err
		}
		if !current.Enabled || current.LeaseGen != page.LeaseGen || !live {
			return nil
		}
		seconds := nextPageCheck(page.IntervalSeconds)
		if fetchErr != nil {
			if err := q.RecordPageFailure(ctx, tx, RecordPageFailureParams{ID: page.ID, Seconds: seconds}); err != nil {
				return err
			}
			return pageSignal(ctx, tx, "catalog.page_failed", page.ID, false)
		}
		return savePageContent(ctx, tx, current, content, seconds)
	})
}

func observePageChange(ctx context.Context, tx pg.Tx, page CatalogWatchedPage, snapshot uuid.UUID, text string) error {
	q := New()
	if err := q.ObservePage(ctx, tx, ObservePageParams{PageID: page.ID, SnapshotID: snapshot}); err != nil {
		return err
	}
	dismissed, err := q.PageWasDismissed(ctx, tx, PageWasDismissedParams{PageID: page.ID, SnapshotID: snapshot})
	if err != nil {
		return err
	}
	if dismissed {
		if err := q.CloseSupersededPageReview(ctx, tx, page.ID); err != nil {
			return err
		}
		return pageSignal(ctx, tx, "catalog.page_changed", page.ID, true)
	}
	var before string
	baseline, err := q.OpenPageReviewBaseline(ctx, tx, page.ID)
	if errors.Is(err, pg.ErrNoRows) {
		baseline = page.CurrentSnapshotID
	} else if err != nil {
		return err
	}
	if baseline != nil {
		before, err = q.PageSnapshotText(ctx, tx, *baseline)
		if err != nil {
			return err
		}
	}
	removed, added := pageDifference(before, text)
	if _, err := q.UpsertPageReview(ctx, tx, UpsertPageReviewParams{
		PageID: page.ID, SnapshotID: snapshot,
		PreviousSnapshotID: baseline, Removed: removed, Added: added,
	}); err != nil {
		return err
	}
	return pageSignal(ctx, tx, "catalog.page_changed", page.ID, false)
}

func (c *Catalog) reportOverduePages(ctx context.Context) error {
	return c.db.Tx(ctx, c.scope, func(tx pg.Tx) error {
		pages, err := New().OverduePages(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range pages {
			if err := pageSignal(ctx, tx, "catalog.page_overdue", id, false); err != nil {
				return fmt.Errorf("page overdue: %w", err)
			}
		}
		return nil
	})
}

func reservePageRequestIn(ctx context.Context, tx pg.Tx, origin string, delay time.Duration) (float64, bool, error) {
	q := New()
	if err := q.DelayPageSite(ctx, tx, DelayPageSiteParams{Origin: origin, Seconds: int64(math.Ceil(delay.Seconds()))}); err != nil {
		return 0, false, err
	}
	_, err := q.ReservePageRequest(ctx, tx, ReservePageRequestParams{Origin: origin, Seconds: int64(math.Ceil(delay.Seconds()))})
	if err == nil {
		return 0, true, nil
	}
	if !errors.Is(err, pg.ErrNoRows) {
		return 0, false, err
	}
	wait, err := q.PageRequestWait(ctx, tx, origin)
	return wait, false, err
}

func recordFetchedPage(ctx context.Context, tx pg.Tx, page, snapshot uuid.UUID, seconds int64) error {
	if err := New().RecordPageSuccess(ctx, tx, RecordPageSuccessParams{ID: page, CurrentSnapshotID: &snapshot, Seconds: seconds}); err != nil {
		return err
	}
	if err := pageSignal(ctx, tx, "catalog.page_failed", page, true); err != nil {
		return err
	}
	return pageSignal(ctx, tx, "catalog.page_overdue", page, true)
}

// keepsCurrentSnapshot reports whether the stored snapshot stays: a worker still on the old extractor must not move the
// page back after a newer one stored a snapshot, and an upgrade that changes only layout normalization is not a page
// change. Keeping it also keeps official evidence and any open review pointed at the text a reviewer already saw; the
// first real change stores the new version.
func keepsCurrentSnapshot(current CatalogWatchedPage, prior PageSnapshotContentRow, content PageContent) bool {
	if current.CurrentSnapshotID == nil {
		return false
	}
	if pageExtractorVersion < prior.ExtractorVersion {
		return true
	}
	return pageExtractorVersion != prior.ExtractorVersion && samePageContent(prior.Text, content)
}

func savePageContent(ctx context.Context, tx pg.Tx, current CatalogWatchedPage, content PageContent, seconds int64) error {
	q := New()
	var prior PageSnapshotContentRow
	if current.CurrentSnapshotID != nil {
		var err error
		prior, err = q.PageSnapshotContent(ctx, tx, *current.CurrentSnapshotID)
		if err != nil {
			return err
		}
	}
	if keepsCurrentSnapshot(current, prior, content) {
		return recordFetchedPage(ctx, tx, current.ID, *current.CurrentSnapshotID, seconds)
	}
	snapshot, err := q.InsertPageSnapshot(ctx, tx, InsertPageSnapshotParams{PageID: current.ID, ExtractorVersion: pageExtractorVersion, Text: content.Text, TextSha256: content.SHA256[:]})
	if errors.Is(err, pg.ErrNoRows) {
		snapshot, err = q.FindPageSnapshot(ctx, tx, FindPageSnapshotParams{PageID: current.ID, ExtractorVersion: pageExtractorVersion, TextSha256: content.SHA256[:]})
	}
	if err != nil {
		return err
	}
	if current.CurrentSnapshotID == nil || *current.CurrentSnapshotID != snapshot {
		if err := observePageChange(ctx, tx, current, snapshot, content.Text); err != nil {
			return err
		}
	}
	return recordFetchedPage(ctx, tx, current.ID, snapshot, seconds)
}
