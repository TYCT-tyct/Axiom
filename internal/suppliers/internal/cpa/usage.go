package cpa

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// usageBatch is how many records one pop asks for. The endpoint has no acknowledgement, so a smaller batch bounds what
// a crash between the pop and the staging insert can lose.
const usageBatch = 100

// storedUsage is the sanitised form of one CPA usage record: the record without its API key, client address, response
// headers or upstream error body. The collector stores this form before it parses or ingests it.
type storedUsage struct {
	V              int       `json:"v"`
	RequestID      string    `json:"request_id"`
	Timestamp      string    `json:"timestamp"`
	Model          string    `json:"model"`
	Alias          string    `json:"alias"`
	Provider       string    `json:"provider"`
	AuthIndex      string    `json:"auth_index"`
	APIKeyHash     string    `json:"api_key_sha256,omitempty"`
	TokenBreakdown jsonx.Raw `json:"token_breakdown"`
	Failed         bool      `json:"failed"`
}

type tokenBreakdown struct {
	SchemaVersion int    `json:"schema_version"`
	Quality       string `json:"quality"`
	TotalTokens   int64  `json:"total_tokens"`
	Input         struct {
		TotalTokens      int64 `json:"total_tokens"`
		UncachedTokens   int64 `json:"uncached_tokens"`
		CacheReadTokens  int64 `json:"cache_read_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	} `json:"input"`
	Output struct {
		TotalTokens        int64 `json:"total_tokens"`
		NonReasoningTokens int64 `json:"non_reasoning_tokens"`
		ReasoningTokens    int64 `json:"reasoning_tokens"`
	} `json:"output"`
	UnclassifiedTokens int64 `json:"unclassified_tokens"`
}

// DrainUsage implements adapter.UsageQueue: it pops at most limit records and returns each in the form the collector
// may store. Records that cannot be read as JSON are returned as a marker, so that the collector keeps the batch and a
// person can see that a record was lost rather than losing the whole batch silently.
func (m *manager) DrainUsage(ctx context.Context, limit int) ([]adapter.UsageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = usageBatch
	}
	path := fmt.Sprintf("/v0/management/usage-queue?count=%d", limit)
	resp, err := m.get(ctx, path, bucketUsage)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"))
		if wait <= 0 {
			wait = time.Minute
		}
		return nil, adapter.Blocked{Until: m.now().Add(wait)}
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("cpa: read the usage queue: status %d", resp.Status)
	}
	var records *[]jsonx.Raw
	if err := jsonx.Unmarshal(resp.Body, &records); err != nil || records == nil {
		return nil, fmt.Errorf("%w: the usage queue is not an array", adapter.ErrUnrecognised)
	}
	out := make([]adapter.UsageRecord, 0, len(*records))
	for _, raw := range *records {
		stored, ref, err := sanitizeUsage(raw)
		if err != nil {
			out = append(out, adapter.UsageRecord{Raw: []byte(`{"v":1,"unreadable":true}`)})
			continue
		}
		out = append(out, adapter.UsageRecord{Ref: adapter.ChargeRef(ref), Raw: stored})
	}
	return out, nil
}

// sanitizeUsage keeps only the fields reconciliation needs and replaces the API key with its ref. It never returns the
// raw record: that record holds the caller's CPA key, which must not reach a database or a log.
func sanitizeUsage(raw []byte) ([]byte, string, error) {
	var src struct {
		RequestID      string    `json:"request_id"`
		Timestamp      time.Time `json:"timestamp"`
		Model          string    `json:"model"`
		Alias          string    `json:"alias"`
		Provider       string    `json:"provider"`
		AuthIndex      string    `json:"auth_index"`
		APIKey         string    `json:"api_key"`
		TokenBreakdown jsonx.Raw `json:"token_breakdown"`
		Failed         bool      `json:"failed"`
	}
	if err := jsonx.Unmarshal(raw, &src); err != nil {
		return nil, "", err
	}
	d := storedUsage{
		V: 1, RequestID: strings.TrimSpace(src.RequestID), Model: strings.TrimSpace(src.Model),
		Alias: strings.TrimSpace(src.Alias), Provider: strings.TrimSpace(src.Provider),
		AuthIndex: strings.TrimSpace(src.AuthIndex), TokenBreakdown: src.TokenBreakdown, Failed: src.Failed,
	}
	if !src.Timestamp.IsZero() {
		d.Timestamp = src.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if key := strings.TrimSpace(src.APIKey); key != "" {
		d.APIKeyHash = string(keyRef(key))
	}
	if request, err := uuid.Parse(d.RequestID); err == nil {
		d.RequestID = request.String()
	} else {
		d.RequestID = "" // a record without a usable id is kept, and ParseUsage rejects it for a person
	}
	body, err := jsonx.MarshalCanonical(d)
	if err != nil {
		return nil, "", err
	}
	return body, d.RequestID, nil
}

// ParseUsage implements adapter.UsageQueue. It reads the sanitised form only, so a record can be parsed after a restart
// with no CPA request and no key.
func (*manager) ParseUsage(raw []byte) (adapter.Charge, error) {
	d, err := decodeStored(raw)
	if err != nil {
		return adapter.Charge{}, err
	}
	request, err := uuid.Parse(d.RequestID)
	if err != nil {
		return adapter.Charge{}, fmt.Errorf("%w: a stored usage record whose request id is not a UUID", adapter.ErrUnrecognised)
	}
	at, err := time.Parse(time.RFC3339Nano, d.Timestamp)
	if err != nil {
		return adapter.Charge{}, fmt.Errorf("%w: a stored usage record with an unreadable time", adapter.ErrUnrecognised)
	}
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = strings.TrimSpace(d.Alias)
	}
	if model == "" {
		return adapter.Charge{}, fmt.Errorf("%w: a stored usage record without a model", adapter.ErrUnrecognised)
	}
	b, err := decodeBreakdown(d.TokenBreakdown)
	if err != nil {
		return adapter.Charge{}, err
	}
	usage, reported, err := chargeUsage(b)
	if err != nil {
		return adapter.Charge{}, err
	}
	return adapter.Charge{
		Ref: adapter.ChargeRef(request.String()), Key: adapter.KeyRef(d.APIKeyHash), OccurredAt: at.UTC(),
		Amount: "0", Usage: usage, Reported: reported, Funding: adapter.FundingWallet, Model: model,
	}, nil
}

func decodeStored(raw []byte) (storedUsage, error) {
	var d storedUsage
	if err := jsonx.UnmarshalKnown(raw, &d); err != nil || d.V != 1 {
		return storedUsage{}, fmt.Errorf("%w: a stored usage record", adapter.ErrUnrecognised)
	}
	if strings.TrimSpace(d.RequestID) == "" || strings.TrimSpace(d.Timestamp) == "" {
		return storedUsage{}, fmt.Errorf("%w: a stored usage record without a request id or time", adapter.ErrUnrecognised)
	}
	return d, nil
}

func decodeBreakdown(raw jsonx.Raw) (tokenBreakdown, error) {
	var b tokenBreakdown
	if len(raw) == 0 || jsonx.Unmarshal(raw, &b) != nil {
		return tokenBreakdown{}, fmt.Errorf("%w: a stored usage record without a token breakdown", adapter.ErrUnrecognised)
	}
	if b.SchemaVersion != 2 || !validQuality(b.Quality) {
		return tokenBreakdown{}, fmt.Errorf("%w: a token breakdown this adapter cannot read", adapter.ErrUnrecognised)
	}
	for _, n := range []int64{
		b.TotalTokens, b.UnclassifiedTokens, b.Input.TotalTokens, b.Input.UncachedTokens, b.Input.CacheReadTokens,
		b.Input.CacheWriteTokens, b.Output.TotalTokens, b.Output.NonReasoningTokens, b.Output.ReasoningTokens,
	} {
		if n < 0 {
			return tokenBreakdown{}, fmt.Errorf("%w: a token breakdown this adapter cannot read", adapter.ErrUnrecognised)
		}
	}
	if b.Input.TotalTokens != b.Input.UncachedTokens+b.Input.CacheReadTokens+b.Input.CacheWriteTokens ||
		b.Output.TotalTokens != b.Output.NonReasoningTokens+b.Output.ReasoningTokens ||
		b.TotalTokens != b.Input.TotalTokens+b.Output.TotalTokens+b.UnclassifiedTokens {
		return tokenBreakdown{}, fmt.Errorf("%w: a token breakdown whose parts do not add up", adapter.ErrUnrecognised)
	}
	return b, nil
}

func chargeUsage(b tokenBreakdown) (catalog.Usage, catalog.ItemSet, error) {
	counts := map[catalog.Item]int64{
		catalog.ItemInput:      b.Input.UncachedTokens,
		catalog.ItemCacheRead:  b.Input.CacheReadTokens,
		catalog.ItemCacheWrite: b.Input.CacheWriteTokens,
		catalog.ItemOutput:     b.Output.NonReasoningTokens,
		catalog.ItemReasoning:  b.Output.ReasoningTokens,
	}
	var reported catalog.ItemSet
	var unknown []catalog.UnknownUsage
	if b.Quality == "complete" && b.UnclassifiedTokens == 0 {
		for _, item := range []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemOutput, catalog.ItemReasoning} {
			reported.Add(item)
		}
	} else {
		unknown = append(unknown, catalog.UnknownUsage{Field: "cpa.token_breakdown.quality", Value: strconv.Quote(b.Quality)})
		if b.UnclassifiedTokens > 0 {
			unknown = append(unknown, catalog.UnknownUsage{
				Field: "cpa.token_breakdown.unclassified_tokens", Value: strconv.FormatInt(b.UnclassifiedTokens, 10),
			})
		}
	}
	usage, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, unknown)
	if err != nil {
		return catalog.Usage{}, 0, fmt.Errorf("%w: a token count outside the billing range", adapter.ErrUnrecognised)
	}
	return usage, reported, nil
}

func validQuality(q string) bool {
	return q == "complete" || q == "inconsistent" || q == "unclassified"
}
