package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// The scan of the usage list (internal/suppliers/README.md 「sub2api」). The scan state is the adapter's own, kept by
// the module as opaque bytes between calls and stored with each page's records, so a restart continues at the next
// page. It follows the same protocol as the other adapters: an empty state is the first scan, Charges returns Done when
// a pass has ended (also when there was nothing to read yet), and the next call starts the next pass.
//
// /api/v1/usage lists newest first and by offset, and takes dates, not times. A record created while a pass runs goes
// to the head and pushes the rest back: a later page may repeat the end of the one before but never skips a record, so
// the reader removes repeats by the request id and a page number can be kept. A pass covers a window and reads every
// record from its first day on, key by key, ending a key at the first page that is short or whose oldest record is
// before the window.
const (
	usagePageSize = 200 // each record also carries its key, user and group, so a larger page would not fit a response
	overlap       = 5 * time.Minute
	lag           = 30 * time.Second // keeps a pass from asking for records still being written
	minPass       = time.Minute      // less than this beyond the watermark is not worth a pass
	lateEvery     = 24 * time.Hour   // how often the last day is read again, for records that appeared later than expected
)

// scanState is the adapter's position. Watermark is everything read so far and only rises; LastLate and LateEnd keep
// the late passes, which read the last day again and leave the watermark alone.
type scanState struct {
	V         int   `json:"v"`
	Watermark int64 `json:"watermark"`
	LastLate  int64 `json:"last_late"`
	LateEnd   int64 `json:"late_end"`
	Pass      *pass `json:"pass,omitempty"`
}

// pass is the pass under way: the time it covers, the keys it reads and where in them it is. Page is the key page while
// the keys are being listed, and the page of a key's usage records once they are: a member of its own for the key page
// would be one a node of the version before refuses, and the two run side by side during a rollout.
type pass struct {
	Late   bool     `json:"late"`
	Start  int64    `json:"start"`
	End    int64    `json:"end"`
	Keys   []string `json:"keys,omitempty"`
	Listed bool     `json:"listed,omitempty"`
	Key    int      `json:"key"`
	Page   int      `json:"page"`
}

func decodeScan(raw adapter.ScanState, now time.Time) (scanState, error) {
	if len(raw) == 0 { // the first scan: nothing before the connection is ours
		return scanState{V: 1, Watermark: now.Unix(), LastLate: now.Unix(), LateEnd: now.Unix()}, nil
	}
	var s scanState
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil || s.V != 1 {
		return scanState{}, fmt.Errorf("%w: the saved scan position", adapter.ErrUnrecognised)
	}
	return s, nil
}

func (s scanState) encode() (adapter.ScanState, error) { return jsonx.MarshalCanonical(s) }

// nextPass picks what to read next: the last day again when that is due, otherwise the stretch after the watermark,
// which starts a little before it. Nothing is returned while less than a minute has passed beyond the watermark.
func nextPass(s scanState, now time.Time) *pass {
	if now.Sub(time.Unix(s.LastLate, 0)) >= lateEvery {
		// From a day before where the last late pass ended, so every stretch is read again a day after, whatever time
		// the passes run at.
		return &pass{Late: true, Start: s.LateEnd - int64(lateEvery/time.Second), End: now.Add(-lag).Unix(), Page: 1}
	}
	end := now.Add(-lag).Unix()
	if end-s.Watermark < int64(minPass/time.Second) {
		return nil
	}
	return &pass{Start: s.Watermark - int64(overlap/time.Second), End: end, Page: 1}
}

// finish records a finished pass: the watermark moves to the pass's end, so it only ever rises.
func finish(s scanState, now time.Time) scanState {
	p := s.Pass
	s.Pass = nil
	if p.Late {
		s.LastLate, s.LateEnd = now.Unix(), max(s.LateEnd, p.End)
		return s
	}
	s.Watermark = max(s.Watermark, p.End)
	return s
}

// Charges reads one page of the pass under way, or starts the next pass. The state it returns is the one to keep with
// the page's records; on an error it is the state it was given, so nothing is skipped.
func (m *manager) Charges(ctx context.Context, scan adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	now := m.s.now()
	s, err := decodeScan(scan, now)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	if s.Pass == nil {
		if s.Pass = nextPass(s, now); s.Pass == nil {
			state, err := s.encode()
			return adapter.ChargePage{Done: true, Through: time.Unix(s.Watermark, 0).UTC()}, state, err
		}
	}
	charges, done, err := m.passPage(ctx, s.Pass)
	if err != nil {
		return adapter.ChargePage{}, scan, err
	}
	if done {
		s = finish(s, now)
	}
	state, err := s.encode()
	out := adapter.ChargePage{Charges: charges, Done: done}
	if done {
		out.Through = time.Unix(s.Watermark, 0).UTC()
	}
	return out, state, err
}

// passPage reads one page of a pass and moves the pass's position on; done is true when the last key has ended. A pass
// begins by listing the keys it reads, one page of them per read, so that a supplier with more keys than a window allows
// is read over several reads.
func (m *manager) passPage(ctx context.Context, p *pass) ([]adapter.Charge, bool, error) {
	if !p.Listed {
		if err := m.listScanKeys(ctx, p); err != nil {
			return nil, false, err
		}
		if !p.Listed {
			return nil, false, nil
		}
	}
	p.skipDuplicateKeys()

	if p.Key >= len(p.Keys) {
		return nil, true, nil
	}
	charges, end, err := m.usagePage(ctx, p)
	if err != nil {
		return nil, false, err
	}
	if end {
		p.Key, p.Page = p.Key+1, 1
	} else {
		p.Page++
	}
	return charges, p.Key >= len(p.Keys), nil
}

func (m *manager) listScanKeys(ctx context.Context, p *pass) error {
	pos := keysProgress{V: 1, Page: p.Page, Total: -1}
	for _, id := range p.Keys {
		pos.Keys = append(pos.Keys, adapter.KeyState{Ref: adapter.KeyRef(id)})
	}
	page, more, err := m.keysPage(ctx, &pos)
	if err != nil {
		return err
	}
	if !more && !page.Done {
		return adapter.Blocked{Until: page.Until}
	}
	p.Keys = nil
	for _, key := range pos.Keys {
		p.Keys = append(p.Keys, string(key.Ref))
	}
	p.Page = pos.Page
	if more {
		return nil
	}
	p.Listed, p.Page = true, 1
	return nil
}

// A legacy node may append a full listing after IDs saved by a newer node.
// A repeated key must not carry its page number over to the next unread key.
func (p *pass) skipDuplicateKeys() {
	for p.Key < len(p.Keys) && slices.Contains(p.Keys[:p.Key], p.Keys[p.Key]) {
		p.Key++
		p.Page = 1
	}
}

// usageItem is a charge record. Count fields are numbers that may be absent: one that is absent is not reported, which
// is not the same as reported as zero.
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

// usagePage reads the page p points at and reports whether it was the last of its key.
func (m *manager) usagePage(ctx context.Context, p *pass) ([]adapter.Charge, bool, error) {
	id, err := pathID(p.Keys[p.Key])
	if err != nil {
		return nil, false, err
	}
	start, end := time.Unix(p.Start, 0).UTC(), time.Unix(p.End, 0).UTC()
	q := url.Values{
		"page": {strconv.Itoa(p.Page)}, "page_size": {strconv.Itoa(usagePageSize)}, "api_key_id": {id},
		"start_date": {start.Format(time.DateOnly)}, "end_date": {end.Format(time.DateOnly)},
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
	items, _, err := pg.checked()
	if err != nil {
		return nil, false, err
	}
	charges := make([]adapter.Charge, 0, len(items))
	last := len(items) < usagePageSize
	for _, it := range items {
		c, at, err := it.charge()
		if err != nil {
			return nil, false, err
		}
		charges = append(charges, c)
		last = last || at.Before(start)
	}
	return charges, last, nil
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
