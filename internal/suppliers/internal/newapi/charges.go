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

// The scan of the consumption log (docs of the design: internal/suppliers/README.md「new-api 适配器」).
//
// /api/log/self lists the account's records newest first and by offset. A record created while a scan runs goes to the
// head of the list and pushes the rest back, so a later page repeats the end of the one before and never skips a
// record: the reader dedupes on the request id, and a page number can be kept between calls. The server filters by
// second timestamps, so a pass covers a fixed [start, end] and ends at the first page with fewer records than asked.
const (
	logPageSize = 100
	// overlap is how far a pass reaches back before the watermark, to take records the supplier wrote late.
	overlap = 5 * time.Minute
	// lag keeps a pass from asking for records still being written.
	lag = 30 * time.Second
	// initialSpan, minSpan and maxSpan bound how much time a forward pass covers: it shrinks when a pass is long, so
	// a backlog is taken in pieces, and grows when passes are short.
	initialSpan = time.Hour
	minSpan     = time.Minute
	maxSpan     = 24 * time.Hour
	// longPass is the number of pages after which the next pass covers half the time; fastPasses short passes in a row
	// double it.
	longPass   = 20
	fastPasses = 3
	// lateEvery is how often the last day is read again for records that appeared later than the passes expected.
	lateEvery = 24 * time.Hour
)

// scanState is the adapter's own position, kept by the module as opaque bytes between calls: it is written in the
// transaction that stores the page's records, so a restart continues at the next page.
type scanState struct {
	V         int   `json:"v"`
	Watermark int64 `json:"watermark"` // everything up to here has been read; only ever raised
	Span      int64 `json:"span"`      // seconds a forward pass covers
	Fast      int   `json:"fast"`      // short passes in a row
	LastLate  int64 `json:"last_late"` // when the last late pass finished
	LateEnd   int64 `json:"late_end"`  // the end of the time the late passes have covered, so the next starts there
	Connected int64 `json:"connected"` // when the scan began: nothing before it is ours, so no pass reads from before
	Pass      *pass `json:"pass,omitempty"`
}

// pass is the pass under way: the time it covers and the next page. Late passes read the last day again and leave the
// watermark alone.
type pass struct {
	Late  bool  `json:"late"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Page  int   `json:"page"`
	Pages int   `json:"pages"`
}

func decodeScan(raw adapter.ScanState, now time.Time) (scanState, error) {
	if len(raw) == 0 { // the first scan: nothing before the connection is ours
		return scanState{V: 1, Watermark: now.Unix(), Span: int64(initialSpan / time.Second), LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Unix()}, nil
	}
	var s scanState
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil || s.V != 1 || s.Span <= 0 {
		return scanState{}, fmt.Errorf("%w: the saved scan position", adapter.ErrUnrecognised)
	}
	return s, nil
}

// lateEnd is where the late passes stand; a position written before they kept it stands at the watermark.
func (s scanState) lateEnd() int64 {
	if s.LateEnd == 0 {
		return s.Watermark
	}
	return s.LateEnd
}

func (s scanState) encode() (adapter.ScanState, error) { return jsonx.MarshalCanonical(s) }

// logPage is the answer to one page of /api/log/self.
//
// Everything a charge is made of is a pointer or a list that may be nil, so that a member the supplier left out can be
// told from a zero, an empty list and a null: an answer without them is not a page with no records.
type logPage struct {
	Data *struct {
		Total *int      `json:"total"`
		Items []logItem `json:"items"`
	} `json:"data"`
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
func (p logPage) valid() bool { return p.Data != nil && p.Data.Total != nil && p.Data.Items != nil }

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
		if s.Pass = nextPass(s, now); s.Pass == nil {
			state, err := s.encode()
			return adapter.ChargePage{Done: true}, state, err
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
	if s.Pass.Page == 1 {
		s.Pass.Pages = (*page.Data.Total + logPageSize - 1) / logPageSize
	}
	done := len(page.Data.Items) < logPageSize
	if done {
		s = finish(s, now)
	} else {
		s.Pass.Page++
	}
	state, err := s.encode()
	return adapter.ChargePage{Charges: charges, Done: done}, state, err
}

// nextPass picks what to read next: the last day again when that is due, otherwise the stretch after the watermark,
// which starts a little before it. Nothing is returned while less than a minute has passed beyond the watermark.
func nextPass(s scanState, now time.Time) *pass {
	if now.Sub(time.Unix(s.LastLate, 0)) >= lateEvery {
		// From a day before where the last late pass ended, so every stretch is read again a day after, whatever time the
		// passes run at: a record that appears up to a day late is read by the pass after it, and the stretches overlap, so
		// no time goes unread however late a pass is run.
		return &pass{Late: true, Start: max(s.lateEnd()-int64(lateEvery/time.Second), s.Connected), End: now.Add(-lag).Unix(), Page: 1}
	}
	end := min(s.Watermark+s.Span, now.Add(-lag).Unix())
	if end-s.Watermark < int64(minSpan/time.Second) {
		return nil
	}
	return &pass{Start: s.Watermark - int64(overlap/time.Second), End: end, Page: 1}
}

// finish records a finished pass: the watermark moves to the pass's end, so it only ever rises, and the next forward
// pass is shorter after a long one and longer after several short ones.
func finish(s scanState, now time.Time) scanState {
	p := s.Pass
	s.Pass = nil
	if p.Late {
		s.LastLate, s.LateEnd = now.Unix(), max(s.lateEnd(), p.End)
		return s
	}
	s.Watermark = max(s.Watermark, p.End)
	if p.Pages > longPass {
		s.Span, s.Fast = max(s.Span/2, int64(minSpan/time.Second)), 0
	} else if s.Fast++; s.Fast >= fastPasses {
		s.Span, s.Fast = min(s.Span*2, int64(maxSpan/time.Second)), 0
	}
	return s
}

func (m *manager) readPage(ctx context.Context, p *pass) (logPage, error) {
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

// charge reads one consumption record, which must state everything a charge is made of.
func charge(it logItem) (adapter.Charge, error) {
	if it.RequestID == "" || it.TokenID == nil || *it.TokenID <= 0 || !it.Quota.isInteger() || it.CreatedAt == nil ||
		it.Prompt == nil || it.Output == nil || it.Other == nil {
		return adapter.Charge{}, fmt.Errorf("%w: a record without its request id, key, time, quota, tokens or details", adapter.ErrUnrecognised)
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

// funding is where the charge was drawn from, per the record's billing source.
func funding(o otherInfo) adapter.Funding {
	if o.BillingSource == "wallet" {
		return adapter.FundingWallet
	}
	return adapter.FundingOther
}
