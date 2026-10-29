package catalog

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// PageEvidence binds an official schedule to quotations from the page's current extracted price section.
type PageEvidence struct {
	PageID     uuid.UUID `json:"page_id"`
	SnapshotID uuid.UUID `json:"snapshot_id"`
	Excerpts   []string  `json:"excerpts"`
}

func parsePageEvidence(raw jsonx.Raw) (PageEvidence, error) {
	var e PageEvidence
	if err := jsonx.UnmarshalKnown(raw, &e); err != nil || e.PageID == (uuid.UUID{}) || e.SnapshotID == (uuid.UUID{}) || len(e.Excerpts) == 0 {
		return e, fmt.Errorf("%w: official evidence needs page_id, snapshot_id and excerpts", ErrNoEvidence)
	}
	return e, nil
}

func checkPageEvidence(e PageEvidence, currency, author, pageCurrency string, snapshot *uuid.UUID, text string) (string, error) {
	if snapshot == nil || *snapshot != e.SnapshotID || currency != pageCurrency {
		return "", fmt.Errorf("%w: official evidence is not the current snapshot in this currency", ErrNoEvidence)
	}
	for _, excerpt := range e.Excerpts {
		if strings.TrimSpace(excerpt) == "" || !strings.Contains(text, excerpt) {
			return "", fmt.Errorf("%w: official excerpt is absent from the current snapshot", ErrNoEvidence)
		}
	}
	return author, nil
}

func lockOfficialEvidence(ctx context.Context, tx pg.Tx, raw jsonx.Raw, currency string) (string, error) {
	e, err := parsePageEvidence(raw)
	if err != nil {
		return "", err
	}
	row, err := New().LockPageEvidence(ctx, tx, e.PageID)
	if err != nil {
		return "", fmt.Errorf("%w: current official page unavailable: %w", ErrNoEvidence, err)
	}
	return checkPageEvidence(e, currency, row.Author, row.Currency, row.CurrentSnapshotID, row.Text)
}

func lockOfficialPrices(ctx context.Context, tx pg.Tx, source PriceSource, news map[string]pricedFrom) (string, error) {
	if source != SourceOfficial {
		return "", nil
	}
	var author string
	// A page's currency is immutable, so this also orders page locks across concurrent switches.
	for _, currency := range slices.Sorted(maps.Keys(news)) {
		got, err := lockOfficialEvidence(ctx, tx, news[currency].evidence, currency)
		if err != nil {
			return "", err
		}
		if author != "" && author != got {
			return "", fmt.Errorf("%w: official pages have different authors", ErrNoEvidence)
		}
		author = got
	}
	return author, nil
}

func checkOfficialPrice(ctx context.Context, tx pg.Tx, m syncModel, check *ModelCheck) error {
	for _, currency := range check.Currencies {
		problems, err := checkOfficialCurrency(ctx, tx, m.row.Author, currency, m.heads[currency].id)
		if err != nil {
			return err
		}
		for _, problem := range problems {
			check.Problems = append(check.Problems, currency+": "+problem)
		}
	}
	return nil
}

func readOfficialEvidence(ctx context.Context, tx pg.Tx, currency string, id uuid.UUID) (PageEvidence, error) {
	record, err := New().OfficialPriceEvidence(ctx, tx, OfficialPriceEvidenceParams{Currency: currency, PriceID: id.String()})
	if errors.Is(err, pg.ErrNoRows) {
		return PageEvidence{}, ErrNoEvidence
	}
	if err != nil {
		return PageEvidence{}, err
	}
	evidence := jsonx.Raw(record.Evidence)
	if record.Kind != "price.publish" {
		var currencies map[string]jsonx.Raw
		if err := jsonx.Unmarshal(evidence, &currencies); err != nil {
			return PageEvidence{}, err
		}
		evidence = currencies[currency]
	}
	return parsePageEvidence(evidence)
}

func checkOfficialCurrency(ctx context.Context, tx pg.Tx, author, currency string, id uuid.UUID) ([]string, error) {
	e, err := readOfficialEvidence(ctx, tx, currency, id)
	if errors.Is(err, ErrNoEvidence) {
		return []string{"official price has no valid recorded evidence"}, nil
	}
	if err != nil {
		return nil, err
	}
	page, err := New().ReadPageEvidence(ctx, tx, e.PageID)
	if errors.Is(err, pg.ErrNoRows) {
		return []string{"official current snapshot unavailable"}, nil
	}
	if err != nil {
		return nil, err
	}
	var problems []string
	got, err := checkPageEvidence(e, currency, page.Author, page.Currency, page.CurrentSnapshotID, page.Text)
	if err != nil || got != author {
		problems = append(problems, "official snapshot, currency, author or excerpts differ")
	}
	pending, err := New().PendingPageReviews(ctx, tx, e.PageID)
	if pending > 0 {
		problems = append(problems, "page has an unresolved review")
	}
	return problems, err
}
