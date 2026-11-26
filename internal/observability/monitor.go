package observability

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// latencyBounds are the latency bins' edges in milliseconds, ten a decade on the 1-1.2-1.5-2-2.5-3-4-5-6-8 series:
// bin 0 is below the first edge, bin i is [latencyBounds[i-1], latencyBounds[i]), and the last bin is open. The rollup
// passes them to width_bucket, so this array is the only definition. Adjacent edges are at most a third apart, so a
// percentile interpolated inside its bin is off by less than that, and the round edges let the completion-time
// distribution show whole seconds (docs/domains/observability.md「监控汇总」).
var latencyBounds = [...]int32{
	10, 12, 15, 20, 25, 30, 40, 50, 60, 80,
	100, 120, 150, 200, 250, 300, 400, 500, 600, 800,
	1000, 1200, 1500, 2000, 2500, 3000, 4000, 5000, 6000, 8000,
	10000, 12000, 15000, 20000, 25000, 30000, 40000, 50000, 60000, 80000,
	100000, 120000, 150000, 200000, 250000, 300000, 400000, 500000, 600000, 800000,
	1000000,
}

// Histogram counts calls by latency bin.
type Histogram [len(latencyBounds) + 1]int64

// Add adds o's counts to h.
func (h *Histogram) Add(o *Histogram) {
	for i, n := range o {
		h[i] += n
	}
}

// Count is how many calls h holds.
func (h *Histogram) Count() int64 {
	var n int64
	for _, c := range h {
		n += c
	}
	return n
}

// Percentile is the latency in milliseconds below which percent of the calls fell: the nearest rank's bin, read
// linearly between its edges with the bin's calls spread evenly, each at the middle of its share, so a lone call is at
// the bin's middle. The open last bin answers its lower edge. False when h is empty.
func (h *Histogram) Percentile(percent int64) (int64, bool) {
	total := h.Count()
	if total == 0 {
		return 0, false
	}
	rank := max((total*percent+99)/100, 1)
	var below int64
	for bin, n := range h {
		if below+n < rank {
			below += n
			continue
		}
		lo, hi := binEdges(bin)
		if hi < 0 {
			return lo, true
		}
		return lo + (hi-lo)*(2*(rank-below)-1)/(2*n), true
	}
	panic("unreachable: the rank is within the total")
}

// Below is how many calls took less than ms, which must be one of latencyBounds.
func (h *Histogram) Below(ms int32) int64 {
	i, ok := slices.BinarySearch(latencyBounds[:], ms)
	if !ok {
		panic(fmt.Sprintf("observability: %d ms is not a latency bin edge", ms))
	}
	var n int64
	for _, c := range h[:i+1] {
		n += c
	}
	return n
}

// binEdges is bin's lower and upper edge; the open last bin's upper edge is -1.
func binEdges(bin int) (lo, hi int64) {
	if bin > 0 {
		lo = int64(latencyBounds[bin-1])
	}
	if bin == len(latencyBounds) {
		return lo, -1
	}
	return lo, int64(latencyBounds[bin])
}

// Monitor is every site's calls over a window, as the monitor rollups hold them (docs/domains/observability.md「监控汇总」).
type Monitor struct {
	Buckets         []MonitorBucket // every bucket of the window, oldest first, empty ones included
	Switched        int64           // calls that moved to another endpoint after an attempt failed
	FirstToken      Histogram       // calls that produced a first token
	Total           Histogram       // calls that succeeded, by their whole time
	Causes          map[string]int64
	Endpoints       []MonitorEndpoint // by the last attempt's endpoint, most calls first
	Calls, Failures int64
	Tokens          int64
	Width           time.Duration
	Since, Until    time.Time
}

// MonitorBucket is one minute or hour of the window.
type MonitorBucket struct {
	At              time.Time
	Calls, Failures int64
	Tokens          int64
	FirstToken      Histogram
}

// MonitorEndpoint is the calls whose last attempt went to one endpoint.
type MonitorEndpoint struct {
	Endpoint        uuid.UUID
	Calls, Failures int64
	FirstToken      Histogram
}

// The monitor's widths: minutes over the last hour, hours over the last day. The rollup keeps no other.
const (
	MonitorMinute = time.Minute
	MonitorHour   = time.Hour
)

// MonitorWindow is the window of width ending with the bucket now is in: 60 minutes or 24 hours.
func MonitorWindow(width time.Duration, now time.Time) (since, until time.Time) {
	until = now.UTC().Truncate(width).Add(width)
	if width == MonitorMinute {
		return until.Add(-time.Hour), until
	}
	return until.Add(-24 * time.Hour), until
}

const monitorCountsSQL = `SELECT bucket, endpoint_id, error_kind, calls, switched, tokens::bigint
 FROM observability.monitor_counts WHERE width = $1 AND bucket >= $2 AND bucket < $3`

const monitorLatencySQL = `SELECT measure, bucket, bin, sum(calls)::bigint
 FROM observability.monitor_latency WHERE width = $1 AND bucket >= $2 AND bucket < $3
 GROUP BY measure, bucket, bin`

const monitorEndpointLatencySQL = `SELECT endpoint_id, bin, sum(calls)::bigint
 FROM observability.monitor_latency WHERE width = $1 AND bucket >= $2 AND bucket < $3 AND measure = 1
 GROUP BY endpoint_id, bin`

// ReadMonitor reads the window MonitorWindow gives for width at now, in tx, which must hold the platform scope. It
// reads only the rollups, whose size is the window's buckets times the endpoints in use, never the call log.
func ReadMonitor(ctx context.Context, tx pg.Tx, width time.Duration, now time.Time) (Monitor, error) {
	if width != MonitorMinute && width != MonitorHour {
		return Monitor{}, fmt.Errorf("observability: the monitor has no %s buckets", width)
	}
	m := Monitor{Width: width, Causes: map[string]int64{}}
	m.Since, m.Until = MonitorWindow(width, now)
	for at := m.Since; at.Before(m.Until); at = at.Add(width) {
		m.Buckets = append(m.Buckets, MonitorBucket{At: at})
	}
	seconds := int(width / time.Second)
	if err := m.readCounts(ctx, tx, seconds); err != nil {
		return Monitor{}, err
	}
	if err := m.readLatency(ctx, tx, seconds); err != nil {
		return Monitor{}, err
	}
	return m, m.readLineLatency(ctx, tx, seconds)
}

// bucket is the window's bucket that starts at at, nil for one outside it.
func (m *Monitor) bucket(at time.Time) *MonitorBucket {
	i := int(at.Sub(m.Since) / m.Width)
	if at.Before(m.Since) || i >= len(m.Buckets) {
		return nil
	}
	return &m.Buckets[i]
}

// count adds one counts row to the window and its bucket, and returns its failures.
func (m *Monitor) count(at time.Time, kind string, calls, switched, tokens int64) int64 {
	failures := int64(0)
	if kind != "" {
		failures = calls
		m.Causes[kind] += calls
	}
	m.Calls, m.Failures, m.Tokens, m.Switched = m.Calls+calls, m.Failures+failures, m.Tokens+tokens, m.Switched+switched
	if b := m.bucket(at); b != nil {
		b.Calls, b.Failures, b.Tokens = b.Calls+calls, b.Failures+failures, b.Tokens+tokens
	}
	return failures
}

func (m *Monitor) readCounts(ctx context.Context, tx pg.Tx, seconds int) error {
	rows, err := tx.Query(ctx, monitorCountsSQL, seconds, m.Since, m.Until)
	if err != nil {
		return fmt.Errorf("observability: read the monitor's counts: %w", err)
	}
	defer rows.Close()
	endpoints := map[uuid.UUID]*MonitorEndpoint{}
	for rows.Next() {
		var at time.Time
		var endpoint uuid.UUID
		var kind string
		var calls, switched, tokens int64
		if err := rows.Scan(&at, &endpoint, &kind, &calls, &switched, &tokens); err != nil {
			return err
		}
		failures := m.count(at, kind, calls, switched, tokens)
		if endpoint == uuid.Nil() {
			continue // no attempt was made: the call failed before any line
		}
		e := endpoints[endpoint]
		if e == nil {
			e = &MonitorEndpoint{Endpoint: endpoint}
			endpoints[endpoint] = e
		}
		e.Calls, e.Failures = e.Calls+calls, e.Failures+failures
	}
	if err := rows.Err(); err != nil {
		return err
	}
	m.Endpoints = make([]MonitorEndpoint, 0, len(endpoints))
	for _, id := range slices.SortedFunc(maps.Keys(endpoints), uuid.UUID.Compare) {
		m.Endpoints = append(m.Endpoints, *endpoints[id])
	}
	slices.SortStableFunc(m.Endpoints, func(a, b MonitorEndpoint) int { return cmp.Compare(b.Calls, a.Calls) })
	return nil
}

func (m *Monitor) readLatency(ctx context.Context, tx pg.Tx, seconds int) error {
	rows, err := tx.Query(ctx, monitorLatencySQL, seconds, m.Since, m.Until)
	if err != nil {
		return fmt.Errorf("observability: read the monitor's latencies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var measure, bin int16
		var at time.Time
		var calls int64
		if err := rows.Scan(&measure, &at, &bin, &calls); err != nil {
			return err
		}
		if measure == 2 {
			m.Total[bin] += calls
			continue
		}
		m.FirstToken[bin] += calls
		if b := m.bucket(at); b != nil {
			b.FirstToken[bin] += calls
		}
	}
	return rows.Err()
}

func (m *Monitor) readLineLatency(ctx context.Context, tx pg.Tx, seconds int) error {
	byEndpoint := make(map[uuid.UUID]*MonitorEndpoint, len(m.Endpoints))
	for i := range m.Endpoints {
		byEndpoint[m.Endpoints[i].Endpoint] = &m.Endpoints[i]
	}
	rows, err := tx.Query(ctx, monitorEndpointLatencySQL, seconds, m.Since, m.Until)
	if err != nil {
		return fmt.Errorf("observability: read the lines' latencies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var endpoint uuid.UUID
		var bin int16
		var calls int64
		if err := rows.Scan(&endpoint, &bin, &calls); err != nil {
			return err
		}
		if e := byEndpoint[endpoint]; e != nil {
			e.FirstToken[bin] += calls
		}
	}
	return rows.Err()
}
