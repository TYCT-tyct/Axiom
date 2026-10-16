package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"axiom/internal/platform/egress"
)

// The limits README.md「参考价的录入」sets for a fetch; the datasets are a few MB.
const (
	fetchTimeout    = 60 * time.Second
	maxDatasetBytes = 32 << 20
)

// datasetURLs are the only addresses the sync reads.
var datasetURLs = map[PriceSource]string{
	SourceLiteLLM:    "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json",
	SourceOpenRouter: "https://openrouter.ai/api/v1/models",
}

// Fetcher reads the datasets through the egress client: a registered address, a deadline, a size limit, and no
// redirect followed, since the egress client leaves a redirect as the answer and only a 200 is taken.
type Fetcher struct {
	client   *egress.Client
	urls     map[PriceSource]string
	maxBytes int64
	now      func() time.Time
}

// NewFetcher fetches the datasets over client.
func NewFetcher(client *egress.Client) *Fetcher {
	return &Fetcher{client: client, urls: datasetURLs, maxBytes: maxDatasetBytes, now: time.Now}
}

// Fetch downloads one dataset.
func (f *Fetcher) Fetch(ctx context.Context, src PriceSource) (Fetched, error) {
	url, ok := f.urls[src]
	if !ok {
		return Fetched{}, fmt.Errorf("%w: %s is not a dataset", ErrSourceUnsupported, src)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Fetched{}, err
	}
	at := f.now()
	resp, err := f.client.Do(req)
	if err != nil {
		return Fetched{}, fmt.Errorf("fetch %s: %w", src, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Fetched{}, errors.Join(fmt.Errorf("fetch %s: status %d", src, resp.StatusCode), resp.Body.Close())
	}
	body, err := egress.ReadBody(resp.Body, f.maxBytes)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return Fetched{}, fmt.Errorf("fetch %s: %w", src, err)
	}
	return Fetched{Source: src, Body: body, At: at}, nil
}

// datasetSources are the datasets a round reads, in the order it reads them.
var datasetSources = []PriceSource{SourceLiteLLM, SourceOpenRouter}

// SyncAll runs a round over each dataset. A dataset that cannot be fetched or read is an error in the result and leaves
// its models at their last prices; the other dataset's round still runs. This is the function a scheduler calls.
func (c *Catalog) SyncAll(ctx context.Context, f *Fetcher, publish bool) ([]SyncReport, error) {
	var reports []SyncReport
	var errs []error
	for _, src := range datasetSources {
		fetched, err := f.Fetch(ctx, src)
		if err == nil {
			var report SyncReport
			report, err = c.SyncDataset(ctx, fetched, publish)
			reports = append(reports, report)
		}
		if err != nil {
			c.cfg.Logger.Error("catalog sync failed; the prices stay as they were", "dataset", src, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", src, err))
		}
	}
	return reports, errors.Join(errs...)
}
