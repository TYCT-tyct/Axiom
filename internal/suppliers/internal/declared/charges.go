package declared

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

// The scan of the charge log. The declaration supplies the path and the fields; the pass algorithm is the module's,
// because it is what makes a log read safely: a pass covers a fixed window, reads pages newest first, overlaps the
// watermark for late records, and moves the watermark only over what it finished. A record that cannot be read stops
// the pass; a charge dropped here is money nothing matches.
const (
	scanOverlap     = 5 * time.Minute
	scanLag         = 30 * time.Second
	scanInitialSpan = time.Hour
	scanMinSpan     = time.Minute
	scanMaxSpan     = 24 * time.Hour
	scanLongPass    = 20
	scanFastPasses  = 3
	scanLateEvery   = 24 * time.Hour
)

const scanVersion = 1

type scanState struct {
	V         int       `json:"v"`
	Watermark int64     `json:"watermark"`
	Span      int64     `json:"span"`
	Fast      int       `json:"fast"`
	LastLate  int64     `json:"last_late"`
	LateEnd   int64     `json:"late_end"`
	Connected int64     `json:"connected"`
	Size      int       `json:"size"`
	Pass      *scanPass `json:"pass,omitempty"`
}

type scanPass struct {
	Late  bool  `json:"late"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Page  int   `json:"page"`
	Pages int   `json:"pages"`
}

func decodeScan(raw adapter.ScanState, now time.Time, size int) (scanState, error) {
	if len(raw) == 0 {
		return scanState{
			V: 1, Watermark: now.Unix(), Span: int64(scanInitialSpan / time.Second),
			LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Unix(), Size: size,
		}, nil
	}
	var s scanState
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return scanState{}, fmt.Errorf("%w: the saved scan position", adapter.ErrUnrecognised)
	}
	if !s.valid() {
		return scanState{}, fmt.Errorf("%w: the saved scan position", adapter.ErrUnrecognised)
	}
	if s.Pass != nil && !s.Pass.valid() {
		return scanState{}, fmt.Errorf("%w: the saved scan pass", adapter.ErrUnrecognised)
	}
	if s.Size != size {
		// A declaration's page size changed under a pass: start the pass again at the watermark it has not moved, so
		// pages are read twice rather than skipped.
		s.Size, s.Pass = size, nil
	}
	return s, nil
}

func (s scanState) valid() bool {
	return s.V == scanVersion && s.Span > 0 && s.Watermark >= 0 && s.LastLate >= 0 &&
		s.LateEnd >= 0 && s.Connected >= 0 && s.Fast >= 0
}

func (p scanPass) valid() bool {
	return p.Start >= 0 && p.End >= 0 && p.Start <= p.End && p.Page >= 1 && p.Pages >= 0
}

func (s scanState) lateEnd() int64 {
	if s.LateEnd == 0 {
		return s.Watermark
	}
	return s.LateEnd
}

func (s scanState) encode() (adapter.ScanState, error) { return jsonx.MarshalCanonical(s) }

// Charges implements adapter.Manager: one page of the pass under way, or the start of the next one. The state returned
// is kept with the page's records; on an error the state given is returned, so nothing is skipped.
func (m *manager) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	now := m.a.now()
	s, err := decodeScan(scan, now, m.a.charges.pageSize)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	if s.Pass == nil {
		if s.Pass = nextScanPass(s, now); s.Pass == nil {
			state, err := s.encode()
			return adapter.ChargePage{Done: true, Through: time.Unix(s.Watermark, 0).UTC()}, state, err
		}
	}
	charges, pageLen, err := m.readCharges(ctx, s.Pass)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	done := pageLen < m.a.charges.pageSize
	if done {
		s = finishScan(s, now)
	} else {
		s.Pass.Page++
		s.Pass.Pages++
	}
	state, err := s.encode()
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	page := adapter.ChargePage{Charges: charges, Done: done}
	if done {
		page.Through = time.Unix(s.Watermark, 0).UTC()
	}
	return page, state, nil
}

func nextScanPass(s scanState, now time.Time) *scanPass {
	if now.Sub(time.Unix(s.LastLate, 0)) >= scanLateEvery {
		start := max(s.lateEnd()-int64(scanLateEvery/time.Second), s.Connected)
		end := now.Add(-scanLag).Unix()
		if end <= start {
			return nil
		}
		return &scanPass{Late: true, Start: start, End: end, Page: 1, Pages: 1}
	}
	end := min(s.Watermark+s.Span, now.Add(-scanLag).Unix())
	if end-s.Watermark < int64(scanMinSpan/time.Second) {
		return nil
	}
	start := max(s.Watermark-int64(scanOverlap/time.Second), s.Connected)
	if end <= start {
		return nil
	}
	return &scanPass{Start: start, End: end, Page: 1, Pages: 1}
}

func finishScan(s scanState, now time.Time) scanState {
	p := s.Pass
	s.Pass = nil
	if p.Late {
		s.LastLate, s.LateEnd = now.Unix(), max(s.lateEnd(), p.End)
		return s
	}
	s.Watermark = max(s.Watermark, p.End)
	if p.Pages > scanLongPass {
		s.Span, s.Fast = max(s.Span/2, int64(scanMinSpan/time.Second)), 0
	} else if s.Fast++; s.Fast >= scanFastPasses {
		s.Span, s.Fast = min(s.Span*2, int64(scanMaxSpan/time.Second)), 0
	}
	return s
}

// readCharges reads one page and returns its charges and how many records the page held, which says whether it was the
// last page.
func (m *manager) readCharges(ctx context.Context, p *scanPass) ([]adapter.Charge, int, error) {
	values := map[string]string{
		"start": strconv.FormatInt(p.Start, 10),
		"end":   strconv.FormatInt(p.End, 10),
		"page":  strconv.Itoa(p.Page),
		"size":  strconv.Itoa(m.a.charges.pageSize),
	}
	resp, err := m.do(ctx, "charges", http.MethodGet, m.a.charges.path, values, m.a.charges.bucket, nil)
	if err != nil {
		return nil, 0, err
	}
	return parseCharges(m.a.charges, resp.Body)
}

func parseCharges(p chargesPlan, body []byte) ([]adapter.Charge, int, error) {
	raw, ok := p.items.lookup(body)
	if !ok {
		return nil, 0, missing("charges")
	}
	items, err := readArray(raw, "charges")
	if err != nil {
		return nil, 0, err
	}
	out := make([]adapter.Charge, 0, len(items))
	for _, item := range items {
		keep, err := chargeKept(p.filter, item)
		if err != nil {
			return nil, 0, err
		}
		if !keep {
			continue
		}
		c, err := chargeOf(p, item)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, len(items), nil
}

// chargeKept applies the declaration's filter, such as a log that also lists top-ups. A record without the filter's
// field is not a charge.
func chargeKept(f *filterPlan, item jsonx.Raw) (bool, error) {
	if f == nil {
		return true, nil
	}
	raw, ok := f.path.lookup(item)
	if !ok {
		return false, nil
	}
	value, ok := readText(raw)
	if !ok {
		return false, fmt.Errorf("%w: a charge filter is not text", adapter.ErrUnrecognised)
	}
	return value == f.value, nil
}

func chargeOf(p chargesPlan, item jsonx.Raw) (adapter.Charge, error) {
	c, err := chargeBase(p, item)
	if err != nil {
		return adapter.Charge{}, err
	}
	if p.model != nil {
		if c.Model, err = readTextAt(*p.model, item, "charge model"); err != nil {
			return adapter.Charge{}, err
		}
	}
	if p.funding != nil {
		c.Funding = fundingOf(p.funding, item)
	}
	if c.Usage, c.Reported, err = chargeUsage(p, item); err != nil {
		return adapter.Charge{}, err
	}
	return c, nil
}

func chargeBase(p chargesPlan, item jsonx.Raw) (adapter.Charge, error) {
	ref, err := readTextAt(p.ref, item, "charge ref")
	if err != nil {
		return adapter.Charge{}, err
	}
	if ref == "" {
		return adapter.Charge{}, missing("charge ref")
	}
	key, err := readTextAt(p.key, item, "charge key")
	if err != nil {
		return adapter.Charge{}, err
	}
	if key == "" {
		return adapter.Charge{}, missing("charge key")
	}
	raw, ok := p.occurred.lookup(item)
	if !ok {
		return adapter.Charge{}, missing("charge time")
	}
	occurred, ok := readTime(raw)
	if !ok {
		return adapter.Charge{}, fmt.Errorf("%w: a charge's time is not a time", adapter.ErrUnrecognised)
	}
	raw, ok = p.amount.lookup(item)
	if !ok {
		return adapter.Charge{}, missing("charge amount")
	}
	amount, ok := readAmount(raw)
	if !ok {
		return adapter.Charge{}, fmt.Errorf("%w: a charge's amount is not a decimal", adapter.ErrUnrecognised)
	}
	return adapter.Charge{
		Ref: adapter.ChargeRef(ref), Key: adapter.KeyRef(key), OccurredAt: occurred,
		Amount: amount, Funding: adapter.FundingWallet,
	}, nil
}

func chargeUsage(p chargesPlan, item jsonx.Raw) (catalog.Usage, catalog.ItemSet, error) {
	counts := make(map[catalog.Item]int64, len(p.usage))
	var reported catalog.ItemSet
	for it, field := range p.usage {
		raw, ok := field.lookup(item)
		if !ok {
			continue // not reported, so the item stays estimated
		}
		n, ok := readInteger(raw)
		if !ok {
			return catalog.Usage{}, 0, fmt.Errorf("%w: a charge's %s count is not a non-negative integer", adapter.ErrUnrecognised, it)
		}
		counts[it] = n
		reported.Add(it)
	}
	usage, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		return catalog.Usage{}, 0, fmt.Errorf("%w: a charge's counts: %w", adapter.ErrUnrecognised, err)
	}
	return usage, reported, nil
}
