package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// usagePageSize is how many charge records one request lists. A page also carries the key, user and group of each
// record, so a page of 1000 would be megabytes; 200 keeps an answer well under the response limit.
const usagePageSize = 200

// scanState is the adapter's position in a pass: the engine's window (adapter.Scan) and where the pass has got to.
// The list is newest first and paged by offset, so a record that arrives during the pass pushes older ones to later
// pages: a page may repeat the end of the one before (the engine removes it by the record's id) but never skips a
// record. The list only takes days, so a pass reads every record from the window's first day on, key by key, and
// stops a key at the first page that is short or whose oldest record is before the window.
type scanState struct {
	adapter.Scan
	Keys   []string `json:"keys,omitempty"`
	Listed bool     `json:"listed,omitempty"`
	Key    int      `json:"key"`
	Page   int      `json:"page"`
}

// Charges reads one page of the pass in scan and returns the position after it.
func (m *manager) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	var st scanState
	if err := jsonx.UnmarshalKnown(scan, &st); err != nil || st.Start.IsZero() || st.End.Before(st.Start) {
		return adapter.ChargePage{}, nil, fmt.Errorf("%w: a scan that was not begun with adapter.BeginScan", adapter.ErrUnrecognised)
	}
	if !st.Listed {
		keys, err := m.listKeys(ctx)
		if err != nil {
			return adapter.ChargePage{}, nil, err
		}
		for _, k := range keys {
			st.Keys = append(st.Keys, string(k.ID))
		}
		st.Listed, st.Page = true, 1
	}
	var out adapter.ChargePage
	if st.Key < len(st.Keys) {
		charges, end, err := m.usagePage(ctx, st)
		if err != nil {
			return adapter.ChargePage{}, nil, err
		}
		out.Charges = charges
		if end {
			st.Key, st.Page = st.Key+1, 1
		} else {
			st.Page++
		}
	}
	out.Done = st.Key >= len(st.Keys)
	next, err := jsonx.MarshalCanonical(st)
	return out, next, err
}

// usageItem is a charge record. Count fields are pointers: one that is absent is not reported, which is not the same
// as reported as zero.
type usageItem struct {
	RequestID   string `json:"request_id"`
	KeyID       number `json:"api_key_id"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`
	Actual      number `json:"actual_cost"`
	BillingType *int   `json:"billing_type"`
	BillingMode string `json:"billing_mode"`
	Input       number `json:"input_tokens"`
	Output      number `json:"output_tokens"`
	CacheRead   number `json:"cache_read_tokens"`
	CacheWrite  number `json:"cache_creation_tokens"`
	CacheWrite5 number `json:"cache_creation_5m_tokens"`
	CacheWrite1 number `json:"cache_creation_1h_tokens"`
}

// usagePage reads the page st points at and reports whether it was the last of its key.
func (m *manager) usagePage(ctx context.Context, st scanState) ([]adapter.Charge, bool, error) {
	id, err := pathID(st.Keys[st.Key])
	if err != nil {
		return nil, false, err
	}
	q := url.Values{
		"page": {strconv.Itoa(st.Page)}, "page_size": {strconv.Itoa(usagePageSize)}, "api_key_id": {id},
		"start_date": {st.Start.UTC().Format(time.DateOnly)}, "end_date": {st.End.UTC().Format(time.DateOnly)},
		"timezone": {"UTC"}, "sort_by": {"created_at"}, "sort_order": {"desc"},
	}
	resp, err := m.get(ctx, "/api/v1/usage?"+q.Encode(), bucketUsage, bucketPanel)
	if err != nil {
		return nil, false, err
	}
	if resp.Status != http.StatusOK {
		return nil, false, statusError("usage", resp)
	}
	pg, err := open[page[usageItem]](resp)
	if err != nil {
		return nil, false, err
	}
	charges := make([]adapter.Charge, 0, len(pg.Items))
	end := len(pg.Items) < usagePageSize
	for _, it := range pg.Items {
		c, at, err := it.charge()
		if err != nil {
			return nil, false, err
		}
		charges = append(charges, c)
		end = end || at.Before(st.Start)
	}
	return charges, end, nil
}

// charge turns a record into a Charge. Only the fields the reconciliation needs are kept; the key, user and address
// the record also carries are never read.
func (it usageItem) charge() (adapter.Charge, time.Time, error) {
	at, err := time.Parse(time.RFC3339, it.CreatedAt)
	if err != nil || it.RequestID == "" || !it.KeyID.present() {
		return adapter.Charge{}, time.Time{}, fmt.Errorf("%w: a charge record without id, key or time", adapter.ErrUnrecognised)
	}
	amount, err := it.Actual.amount()
	if err != nil {
		return adapter.Charge{}, time.Time{}, err
	}
	counts, reported, err := it.usage()
	if err != nil {
		return adapter.Charge{}, time.Time{}, err
	}
	var unknown []catalog.UnknownUsage
	if it.BillingMode != "token" {
		// Per-request, image and video charges are not in tokens: the amount stands, the usage does not explain it.
		unknown = append(unknown, catalog.UnknownUsage{Field: "billing_mode", Value: strconv.Quote(it.BillingMode)})
	}
	usage, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, unknown)
	if err != nil {
		return adapter.Charge{}, time.Time{}, fmt.Errorf("%w: usage out of range", adapter.ErrUnrecognised)
	}
	funding := adapter.FundingOther
	if it.BillingType != nil && *it.BillingType == 0 {
		funding = adapter.FundingWallet
	}
	return adapter.Charge{
		Ref: adapter.ChargeRef(it.RequestID), Key: adapter.KeyRef(it.KeyID), OccurredAt: at, Amount: amount,
		Usage: usage, Reported: reported, Funding: funding, Model: it.Model,
	}, at, nil
}

// usage maps the token fields onto billing items that do not overlap: sub2api states input apart from cache reads and
// writes. The five-minute and one-hour writes are separate fields; a record that gives only the total counts it as the
// five-minute kind.
func (it usageItem) usage() (map[catalog.Item]int64, catalog.ItemSet, error) {
	counts := map[catalog.Item]int64{}
	var reported catalog.ItemSet
	fields := []struct {
		item catalog.Item
		n    number
	}{
		{catalog.ItemInput, it.Input},
		{catalog.ItemOutput, it.Output},
		{catalog.ItemCacheRead, it.CacheRead},
		{catalog.ItemCacheWrite, it.CacheWrite5},
		{catalog.ItemCacheWrite1h, it.CacheWrite1},
	}
	if !it.CacheWrite5.present() && !it.CacheWrite1.present() {
		fields[3].n = it.CacheWrite
	}
	for _, f := range fields {
		if !f.n.present() {
			continue
		}
		v, err := f.n.int64()
		if err != nil {
			return nil, reported, err
		}
		counts[f.item] = v
		reported.Add(f.item)
	}
	return counts, reported, nil
}
