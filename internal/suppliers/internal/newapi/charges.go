package newapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

const logPageSize = 100

type (
	scanState = adapter.WindowScan
	pass      = adapter.WindowPass
)

func decodeScan(raw adapter.ScanState, now time.Time) (scanState, error) {
	if len(raw) == 0 { // the first scan: nothing before the connection is ours
		return adapter.NewWindowScan(now), nil
	}
	var s scanState
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil || s.V != 1 || s.Span <= 0 {
		return scanState{}, fmt.Errorf("%w: the saved scan position", adapter.ErrUnrecognised)
	}
	return s, nil
}

// logPage is the answer to one page of /api/log/self.
//
// Everything a charge is made of is a pointer or a list that may be nil, so that a member the supplier left out can be
// told from a zero, an empty list and a null: an answer without them is not a page with no records.
type logPage struct {
	Data      *logData `json:"data"`
	uncounted bool     // one-api's: a list of records without their total
}

type logData struct {
	Total *int      `json:"total"`
	Items []logItem `json:"items"`
}

type logItem struct {
	Type      *int    `json:"type"`
	CreatedAt *int64  `json:"created_at"`
	Model     string  `json:"model_name"`
	Quota     number  `json:"quota"`
	Prompt    *int64  `json:"prompt_tokens"`
	Output    *int64  `json:"completion_tokens"`
	TokenID   *int64  `json:"token_id"`
	RequestID string  `json:"request_id"`
	Other     *string `json:"other"`
}

// valid reports whether the answer has the structure of a page, whatever it holds.
func (p logPage) valid() bool {
	return p.Data != nil && (p.Data.Total != nil || p.uncounted) && p.Data.Items != nil
}

// Charges implements adapter.Manager: one page of the pass under way, or the start of a new one. The state it returns
// is the one to keep with the page's records; on an error it is the state it was given, so nothing is skipped. A call
// with nothing to read yet returns an empty page and Done.
func (m *manager) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	now := m.now()
	s, err := decodeScan(scan, now)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	if s.Pass == nil {
		if s.Pass = s.Next(now); s.Pass == nil {
			state, err := s.Encode()
			return adapter.ChargePage{Done: true, Through: time.Unix(s.Watermark, 0).UTC()}, state, err
		}
	}
	page, err := m.readPage(ctx, s.Pass)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	charges, err := convert(page)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	if err := m.fundUnstated(ctx, page, charges); err != nil {
		return adapter.ChargePage{}, scan, err
	}
	s, done := m.turnPage(s, page, now)
	state, err := s.Encode()
	page2 := adapter.ChargePage{Charges: charges, Done: done}
	if done {
		page2.Through = time.Unix(s.Watermark, 0).UTC()
	}
	return page2, state, err
}

// turnPage moves the pass past the page it read: to its next page, or, after a short page, to its end. A pass's length
// in pages is new-api's total; one-api states none, so the pages it read are the length.
func (m *manager) turnPage(s scanState, page logPage, now time.Time) (scanState, bool) {
	if s.Pass.Page == 1 && !page.uncounted {
		s.Pass.Pages = (*page.Data.Total + logPageSize - 1) / logPageSize
	}
	if len(page.Data.Items) >= m.logPageSize() {
		s.Pass.Page++
		return s, false
	}
	if page.uncounted {
		s.Pass.Pages = s.Pass.Page
	}
	return s.Finish(now), true
}

func (m *manager) readPage(ctx context.Context, p *pass) (logPage, error) {
	if m.sys == oneAPI {
		return m.oneAPILogPage(ctx, p)
	}
	var page logPage
	path := "/api/log/self?type=2&page_size=" + strconv.Itoa(logPageSize) + "&p=" + strconv.Itoa(p.Page) +
		"&start_timestamp=" + strconv.FormatInt(p.Start, 10) + "&end_timestamp=" + strconv.FormatInt(p.End, 10)
	if err := m.api.do(ctx, http.MethodGet, path, global, &page); err != nil {
		return logPage{}, err
	}
	return page, nil
}

// otherInfo is what the record's "other" text, itself JSON, says about how the charge was made.
type otherInfo struct {
	BillingSource       string `json:"billing_source"`
	CacheTokens         int64  `json:"cache_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
}

// convert turns a page into charges. A record that cannot be read stops the pass instead of being left out: a charge
// dropped here is money that nothing matches.
func convert(page logPage) ([]adapter.Charge, error) {
	if !page.valid() {
		return nil, fmt.Errorf("%w: the answer is not a page of the log", adapter.ErrUnrecognised)
	}
	charges := make([]adapter.Charge, 0, len(page.Data.Items))
	for _, it := range page.Data.Items {
		if it.Type == nil {
			return nil, fmt.Errorf("%w: a record without a type", adapter.ErrUnrecognised)
		}
		if *it.Type != 2 {
			continue
		}
		c, err := charge(it)
		if err != nil {
			return nil, err
		}
		charges = append(charges, c)
	}
	return charges, nil
}

// charge reads one consumption record, which must state everything a charge is made of. A record without a request id
// is from a release that does not log one (new-api before v0.10.8, one-api before v0.6.11): nothing ties it to the call
// it was for, so such a supplier's charges cannot be reconciled, and the pass stops rather than guess.
func charge(it logItem) (adapter.Charge, error) {
	if it.RequestID == "" {
		return adapter.Charge{}, fmt.Errorf("%w: a record without a request id: this release does not log one, so its charges cannot be matched to calls", adapter.ErrUnrecognised)
	}
	if it.TokenID == nil || *it.TokenID <= 0 || !it.Quota.isInteger() || it.CreatedAt == nil ||
		it.Prompt == nil || it.Output == nil || it.Other == nil {
		return adapter.Charge{}, fmt.Errorf("%w: a record without its key, time, quota, tokens or details", adapter.ErrUnrecognised)
	}
	var other otherInfo
	if jsonx.Unmarshal([]byte(*it.Other), &other) != nil {
		return adapter.Charge{}, fmt.Errorf("%w: a record's details", adapter.ErrUnrecognised)
	}
	usage, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: *it.Prompt, catalog.ItemOutput: *it.Output}, catalog.ServiceTierStandard, nil)
	if err != nil {
		return adapter.Charge{}, fmt.Errorf("%w: a record's token counts", adapter.ErrUnrecognised)
	}
	return adapter.Charge{
		Ref: adapter.ChargeRef(it.RequestID), Key: adapter.KeyRef(strconv.FormatInt(*it.TokenID, 10)),
		OccurredAt: time.Unix(*it.CreatedAt, 0).UTC(), Amount: adapter.Amount(it.Quota),
		Usage: usage, Reported: reported(other), Funding: funding(other), Model: it.Model,
	}, nil
}

// reported is the items the record states as billing items. The log's prompt_tokens includes the cached tokens for
// OpenAI-shaped usage and leaves them out for Anthropic-shaped, and the record does not say which, so with any cached or
// cache-writing token the input is not a number that can correct an estimate. The completion tokens include the
// reasoning tokens and the record does not split them, so the output is never reported: correcting the output item
// with a total that holds the reasoning would count the reasoning twice, once there and once in the item it stays in.
// The amount corrects those.
func reported(o otherInfo) (set catalog.ItemSet) {
	if o.CacheTokens == 0 && o.CacheCreationTokens == 0 {
		set.Add(catalog.ItemInput)
	}
	return set
}

// funding is where the charge was drawn from, per the record's billing source; a record without one is taken for the
// wallet here, and fundUnstated corrects that for an account with an active subscription.
func funding(o otherInfo) adapter.Funding {
	if o.BillingSource == "wallet" || o.BillingSource == "" {
		return adapter.FundingWallet
	}
	return adapter.FundingOther
}

// fundUnstated decides where the records of the page that state no billing source were drawn from. new-api writes the
// source only where a billing session chose between wallet and subscription (service/log_info_generate.go
// appendBillingInfo). A record without it is from a release before subscriptions (commit ddeacccc0), which had only
// the wallet, from its path without a session, which draws on the wallet, or a fee it takes after the request (a
// violation fee, service/violation_fee.go) from whatever the request drew on, a subscription included
// (service/quota.go postConsumeQuotaWithResult). Such a record names no request it followed, so it is a wallet charge
// only while the account has no active subscription; with one it is not, which stops the supplier rather than letting
// it route as if it were paid from the wallet (docs/domains/suppliers.md「决策与理由」). The subscriptions are read
// only for a page that has such a record.
func (m *manager) fundUnstated(ctx context.Context, page logPage, charges []adapter.Charge) error {
	unstated := map[adapter.ChargeRef]bool{}
	for _, it := range page.Data.Items {
		var other otherInfo
		if *it.Type == 2 && jsonx.Unmarshal([]byte(*it.Other), &other) == nil && other.BillingSource == "" {
			unstated[adapter.ChargeRef(it.RequestID)] = true
		}
	}
	if len(unstated) == 0 {
		return nil
	}
	subscribed, err := m.subscribed(ctx)
	if err != nil || !subscribed {
		return err
	}
	for i := range charges {
		if unstated[charges[i].Ref] {
			charges[i].Funding = adapter.FundingOther
		}
	}
	return nil
}
