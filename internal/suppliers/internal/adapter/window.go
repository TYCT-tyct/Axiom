package adapter

import (
	"time"

	"axiom/internal/platform/jsonx"
)

const (
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
	// maxRange is the most time one pass asks for, from its start to its end. new-api answers an ordinary user's log
	// search that spans more than its log_setting.max_range_days with 200 success:false, and that setting may be as
	// low as one day (setting/operation_setting/log_setting.go, controller/log.go limitLogTimeRange); a day is within
	// every setting. A late pass, which covers up to two days, is read as passes of a day each.
	maxRange = 24 * time.Hour
)

// WindowScan is the adapter's own position, kept by the module as opaque bytes between calls: it is written in the
// transaction that stores the page's records, so a restart continues at the next page.
type WindowScan struct {
	V         int         `json:"v"`
	Watermark int64       `json:"watermark"`          // everything up to here has been read; only ever raised
	Span      int64       `json:"span"`               // seconds a forward pass covers
	Fast      int         `json:"fast"`               // short passes in a row
	LastLate  int64       `json:"last_late"`          // when the last late pass finished
	LateEnd   int64       `json:"late_end"`           // the end of the time the late passes have covered, so the next starts there
	Connected int64       `json:"connected"`          // when the scan began: nothing before it is ours, so no pass reads from before
	LateFrom  int64       `json:"late_from,omitzero"` // where the rest of a late read that maxRange cut short begins
	Pass      *WindowPass `json:"pass,omitempty"`
}

// WindowPass is the pass under way: the time it covers and the next page. Late passes read the last day again and leave the
// watermark alone; More marks a late pass that maxRange cut short, whose rest the next pass reads.
type WindowPass struct {
	Late  bool  `json:"late"`
	More  bool  `json:"more,omitzero"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Page  int   `json:"page"`
	Pages int   `json:"pages"`
}

// NewWindowScan starts at the connection time; earlier records do not belong to this scan.
func NewWindowScan(now time.Time) WindowScan {
	return WindowScan{V: 1, Watermark: now.Unix(), Span: int64(initialSpan / time.Second), LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Unix()}
}

// lateEnd is where the late passes stand; a position written before they kept it stands at the watermark.
func (s WindowScan) lateEnd() int64 {
	if s.LateEnd == 0 {
		return s.Watermark
	}
	return s.LateEnd
}

// Encode returns the canonical JSON position to persist between calls.
func (s WindowScan) Encode() (ScanState, error) { return jsonx.MarshalCanonical(s) }

// Next picks what to read next: the last day again when that is due, otherwise the stretch after the watermark,
// which starts a little before it. Nothing is returned while less than a minute has passed beyond the watermark.
func (s WindowScan) Next(now time.Time) *WindowPass {
	if s.LateFrom != 0 {
		return latePass(s.LateFrom, now)
	}
	if now.Sub(time.Unix(s.LastLate, 0)) >= lateEvery {
		// From a day before where the last late pass ended, so every stretch is read again a day after, whatever time the
		// passes run at: a record that appears up to a day late is read by the pass after it, and the stretches overlap, so
		// no time goes unread however late a pass is run.
		return latePass(max(s.lateEnd()-int64(lateEvery/time.Second), s.Connected), now)
	}
	start := max(s.Watermark-int64(overlap/time.Second), s.Connected)
	end := min(s.Watermark+s.Span, now.Add(-lag).Unix(), start+int64(maxRange/time.Second))
	if end-s.Watermark < int64(minSpan/time.Second) {
		return nil
	}
	return &WindowPass{Start: start, End: end, Page: 1}
}

// latePass reads from start up to now, or maxRange of it, with More set when that leaves some for the next pass.
func latePass(start int64, now time.Time) *WindowPass {
	p := &WindowPass{Late: true, Start: start, End: now.Add(-lag).Unix(), Page: 1}
	if limit := start + int64(maxRange/time.Second); p.End > limit {
		p.End, p.More = limit, true
	}
	return p
}

// Finish records a finished pass: the watermark moves to the pass's end, so it only ever rises, and the next forward
// pass is shorter after a long one and longer after several short ones.
func (s WindowScan) Finish(now time.Time) WindowScan {
	p := s.Pass
	s.Pass = nil
	if p.Late {
		s.LateEnd, s.LateFrom = max(s.lateEnd(), p.End), 0
		if p.More {
			s.LateFrom = p.End
		} else {
			s.LastLate = now.Unix()
		}
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
