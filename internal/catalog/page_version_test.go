package catalog_test

import (
	"crypto/sha256"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func (s *store) insertSnapshot(page uuid.UUID, version int, text string) uuid.UUID {
	s.t.Helper()
	sum := sha256.Sum256([]byte(text))
	var id uuid.UUID
	if err := s.db.Tx(s.t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(s.t.Context(), `INSERT INTO catalog.page_snapshot (page_id, extractor_version, text, text_sha256)
			VALUES ($1, $2, $3, $4) RETURNING id`, page, version, text, sum[:]).Scan(&id)
	}); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *store) installSnapshot(page uuid.UUID, version int, text string) uuid.UUID {
	s.t.Helper()
	id := s.insertSnapshot(page, version, text)
	if err := s.exec(platformScope, `UPDATE catalog.watched_page SET current_snapshot_id = $2 WHERE id = $1`, page, id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func TestPageSnapshotVersionCannotGoBackwards(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	newer := s.installSnapshot(page, 2, "alpha $2")
	older := s.insertSnapshot(page, 1, "alpha $1")

	if err := s.exec(platformScope, `UPDATE catalog.watched_page SET current_snapshot_id = $2 WHERE id = $1`, page, older); err == nil {
		t.Fatal("database accepted a move back to the older extractor")
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != newer {
		t.Fatalf("current snapshot %s, want %s", got, newer)
	}
}

func TestPageExtractorUpgradeDoesNotCollapseAdjacentCells(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	prior := s.installSnapshot(page, 1, "Model Input Output alpha 1 23")
	changed := "Model Input Output alpha 12 3"

	if err := s.observeLegacy(t.Context(), page, changed, changed); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot WHERE page_id=$1", page); got != 2 {
		t.Fatalf("changed cells stored %d snapshots, want 2", got)
	}
	if got := query[int](s, platformScope, "SELECT extractor_version FROM catalog.page_snapshot s JOIN catalog.watched_page p ON p.current_snapshot_id=s.id WHERE p.id=$1", page); got != 2 {
		t.Fatalf("current extractor version %d, want 2", got)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT previous_snapshot_id FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != prior {
		t.Fatalf("review baseline %s, want %s", got, prior)
	}
}

func TestPageExtractorUpgradeWithSameContentKeepsSnapshot(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	old := s.installSnapshot(page, 1, "alpha $1  \u200b")

	if err := s.observeLegacy(t.Context(), page, "alpha $1", "alpha $1  \u200b"); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot WHERE page_id=$1", page); got != 1 {
		t.Fatalf("upgrade stored a second snapshot: %d", got)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.review WHERE page_id=$1", page); got != 0 {
		t.Fatalf("upgrade opened %d reviews", got)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != old {
		t.Fatalf("upgrade moved current snapshot to %s, want %s", got, old)
	}
}

func TestPageExtractorUpgradeWithChangedContentReviewsAgainstPriorSnapshot(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	old := s.installSnapshot(page, 1, "alpha $1")

	if err := s.observeLegacy(t.Context(), page, "alpha $2", "alpha $2"); err != nil {
		t.Fatal(err)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot WHERE page_id=$1", page); got != 2 {
		t.Fatalf("changed content stored %d snapshots, want 2", got)
	}
	if got := query[int](s, platformScope, "SELECT extractor_version FROM catalog.page_snapshot s JOIN catalog.watched_page p ON p.current_snapshot_id=s.id WHERE p.id=$1", page); got != 2 {
		t.Fatalf("current extractor version %d, want 2", got)
	}
	if got := query[string](s, platformScope, "SELECT removed || '/' || added FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != "1/2" {
		t.Fatalf("review diff %q, want 1/2", got)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT previous_snapshot_id FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != old {
		t.Fatalf("review baseline %s, want %s", got, old)
	}
}

func TestPageOlderExtractorCannotMoveSnapshotBack(t *testing.T) {
	t.Parallel()
	// Version 3 stands in for a snapshot written by the newer extractor; a worker still on version 2 is the old worker.
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	newer := s.installSnapshot(page, 3, "alpha $3")

	if err := s.observe(t.Context(), page, "alpha $4"); err != nil {
		t.Fatal(err)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != newer {
		t.Fatalf("old worker moved current snapshot to %s, want %s", got, newer)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.page_snapshot WHERE page_id=$1", page); got != 1 {
		t.Fatalf("old worker stored %d snapshots, want 1", got)
	}
	if got := query[int](s, platformScope, "SELECT count(*) FROM catalog.review WHERE page_id=$1", page); got != 0 {
		t.Fatalf("old worker opened %d reviews", got)
	}
}

func TestPageExtractorUpgradeKeepsAnOpenReview(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	s.seed("acme/alpha", basicSpec, basic)
	page := s.watch(t.Context(), "acme", "USD")
	baseline := s.installSnapshot(page, 1, "alpha $1")
	changed := s.installSnapshot(page, 1, "alpha $2")
	if err := s.exec(platformScope, `INSERT INTO catalog.review (page_id, snapshot_id, previous_snapshot_id, removed, added)
		VALUES ($1, $2, $3, '$1', '$2')`, page, changed, baseline); err != nil {
		t.Fatal(err)
	}

	// The newer extractor sees the same content as the snapshot under review: the review and its baseline stay.
	if err := s.observeLegacy(t.Context(), page, "alpha $2", "alpha $2"); err != nil {
		t.Fatal(err)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT current_snapshot_id FROM catalog.watched_page WHERE id=$1", page); got != changed {
		t.Fatalf("upgrade moved the reviewed snapshot to %s, want %s", got, changed)
	}
	if got := query[int](s, platformScope, "SELECT content_seq FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != 1 {
		t.Fatalf("upgrade changed review content_seq to %d", got)
	}

	// A later real change is still compared with the pre-change baseline, so the unreviewed change is not lost.
	if err := s.observeLegacy(t.Context(), page, "alpha $3", "alpha $3"); err != nil {
		t.Fatal(err)
	}
	if got := query[string](s, platformScope, "SELECT removed || '/' || added FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != "1/3" {
		t.Fatalf("review diff %q, want 1/3", got)
	}
	if got := query[uuid.UUID](s, platformScope, "SELECT previous_snapshot_id FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != baseline {
		t.Fatalf("review baseline %s, want %s", got, baseline)
	}
	if got := query[int](s, platformScope, "SELECT content_seq FROM catalog.review WHERE page_id=$1 AND status='open'", page); got != 2 {
		t.Fatalf("review content_seq %d, want 2", got)
	}
}
