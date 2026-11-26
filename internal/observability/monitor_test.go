package observability

import (
	"fmt"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestHistogramPercentile(t *testing.T) {
	t.Parallel()
	one := func(bin int, n int64) Histogram {
		var h Histogram
		h[bin] = n
		return h
	}
	var spread Histogram
	spread[1], spread[21] = 1, 3 // [10, 12) once, [1000, 1200) three times
	for _, c := range []struct {
		name    string
		h       Histogram
		percent int64
		want    int64
		ok      bool
	}{
		{"empty", Histogram{}, 50, 0, false},
		{"below the first edge", one(0, 4), 50, 3, true},
		{"a lone call is its bin's middle", one(21, 1), 50, 1100, true},
		{"the second of four calls in one bin", one(21, 4), 50, 1075, true},
		{"the open last bin answers its lower edge", one(len(latencyBounds), 3), 99, 1000000, true},
		{"the median is the second call, the first of the upper bin", spread, 50, 1033, true},
		{"the 25th percentile is the first call", spread, 25, 11, true},
		{"the 99th percentile is the last call", spread, 99, 1166, true},
	} {
		got, ok := c.h.Percentile(c.percent)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: p%d = %d, %v; want %d, %v", c.name, c.percent, got, ok, c.want, c.ok)
		}
	}
	if got := spread.Below(1000); got != 1 {
		t.Errorf("below 1000 ms = %d, want 1", got)
	}
	if got := spread.Below(1200); got != 4 {
		t.Errorf("below 1200 ms = %d, want 4", got)
	}
}

// TestMonitorWindowEndsWithTheCurrentBucket: an hour of minutes or a day of hours, the current one last.
func TestMonitorWindowEndsWithTheCurrentBucket(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 26, 10, 17, 42, 0, time.UTC)
	since, until := MonitorWindow(MonitorMinute, now)
	if !since.Equal(time.Date(2026, 11, 26, 9, 18, 0, 0, time.UTC)) || !until.Equal(time.Date(2026, 11, 26, 10, 18, 0, 0, time.UTC)) {
		t.Errorf("1h window = %s to %s", since, until)
	}
	since, until = MonitorWindow(MonitorHour, now)
	if !since.Equal(time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)) || !until.Equal(time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("24h window = %s to %s", since, until)
	}
}

// monitorCalls are four calls a few minutes ago through two endpoints: a switched success, a timeout, a call refused
// before any attempt, and a pending success with a web search, which the monitor counts and the usage rollup leaves out.
func monitorCalls(site uuid.UUID, at time.Time) (records []CallRecord, pending CallRecord, endpoint, other uuid.UUID) {
	account := uuid.NewV7()
	ok := record(account, at)
	ok.Site, ok.ErrorKind = site, ""
	endpoint = ok.Attempts[1].Endpoint
	timeout := record(account, at.Add(time.Second))
	timeout.Site, timeout.ErrorKind, timeout.FirstToken, timeout.Latency = site, "timeout", 0, 5*time.Second
	timeout.Attempts = timeout.Attempts[1:]
	timeout.Attempts[0].Seq, timeout.Attempts[0].Endpoint = 1, endpoint
	timeout.Attempts[0].Outcome, timeout.Attempts[0].ErrorKind, timeout.Attempts[0].FirstToken = AttemptFailed, "timeout", 0
	refused := record(account, at.Add(2*time.Second))
	refused.Site, refused.ErrorKind, refused.FirstToken, refused.Attempts = site, "invalid_request", 0, nil
	pending = record(account, at.Add(3*time.Second))
	pending.Site, pending.ErrorKind, pending.FirstToken, pending.Latency = site, "", 2*time.Second, 3*time.Second
	pending.Attempts = pending.Attempts[1:]
	pending.Attempts[0].Seq = 1
	other = pending.Attempts[0].Endpoint
	pending.Plugins = []PluginRun{{Plugin: PluginWeb, OK: true, Engine: "searxng", Uses: 1, Duration: 800 * time.Millisecond, PriceNano: 7}}
	return []CallRecord{ok, timeout, refused}, pendingOf(pending), endpoint, other
}

func readMonitor(t *testing.T, db *pg.DB, width time.Duration) Monitor {
	t.Helper()
	var m Monitor
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		m, err = ReadMonitor(t.Context(), tx, width, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestMonitorRollsUpEveryCall: the journal feeds both widths; a pending call is in the monitor and not in usage_hourly.
func TestMonitorRollsUpEveryCall(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	site := uuid.NewV7()
	at := time.Now().Add(-5 * time.Minute).UTC()
	records, pending, endpoint, other := monitorCalls(site, at)
	keep(t, db, records...)
	keepPending(t, db, pending)
	for usageBatch(t, db, 2) {
	}
	checkMonitorWindow(t, readMonitor(t, db, MonitorMinute), 60, endpoint, other)
	checkMonitorWindow(t, readMonitor(t, db, MonitorHour), 24, endpoint, other)
	var hourly int64
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce(sum(requests), 0)::bigint FROM observability.usage_hourly WHERE site_id = $1`, site).Scan(&hourly)
	}); err != nil {
		t.Fatal(err)
	}
	if hourly != 3 {
		t.Errorf("usage_hourly holds %d requests, want the 3 settled ones", hourly)
	}
}

// checkMonitorWindow checks one window over monitorCalls: every call in the totals and in its bucket, the causes, the
// first token of the two calls that had one, the two completions and the two endpoints.
func checkMonitorWindow(t *testing.T, m Monitor, buckets int, endpoint, other uuid.UUID) {
	t.Helper()
	type line struct {
		endpoint              uuid.UUID
		calls, failures, tfts int64
	}
	type summary struct {
		buckets, inBuckets                        int64
		first                                     bool
		calls, failures, switched, tokens, p50, n int64
		causes, lines                             string
	}
	got := summary{
		buckets: int64(len(m.Buckets)), first: m.Buckets[0].At.Equal(m.Since), calls: m.Calls,
		failures: m.Failures, switched: m.Switched, tokens: m.Tokens, n: m.Total.Count(), causes: fmt.Sprint(m.Causes),
	}
	for _, b := range m.Buckets {
		got.inBuckets += b.Calls
	}
	got.p50, _ = m.FirstToken.Percentile(50)
	lines := make([]line, len(m.Endpoints))
	for i, e := range m.Endpoints {
		lines[i] = line{e.Endpoint, e.Calls, e.Failures, e.FirstToken.Count()}
	}
	got.lines = fmt.Sprint(lines)
	// A pending call has no usage; the median is the middle of the 300 ms call's bin; two calls succeeded.
	want := summary{
		buckets: int64(buckets), inBuckets: 4, first: true, calls: 4, failures: 2, switched: 1, tokens: 3 * 150,
		p50: 350, n: 2, causes: fmt.Sprint(map[string]int64{"invalid_request": 1, "timeout": 1}),
		lines: fmt.Sprint([]line{{endpoint, 2, 1, 1}, {other, 1, 0, 1}}),
	}
	if got != want {
		t.Errorf("%s window = %+v, want %+v", m.Width, got, want)
	}
}

// keepPending writes rec as a call whose settlement never reached its record: the cleanup's path.
func keepPending(t *testing.T, db *pg.DB, rec CallRecord) {
	t.Helper()
	r, err := NewRecorder(t.Context(), db, platformScope, Config{
		Dir: t.TempDir(), Capacity: 64, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := r.Admit()
	if err != nil {
		t.Fatal(err)
	}
	commit(t, slot, rec)
	slot.Release()
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestSiteUsageAndPluginStats: the dashboard's rollup reads and the plugins' hours, and the plugin filter of the list.
func TestSiteUsageAndPluginStats(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	site := uuid.NewV7()
	hour := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	big, small := uuid.NewV7(), uuid.NewV7()
	var records []CallRecord
	for i, account := range []uuid.UUID{big, big, small} {
		r := record(account, hour.Add(time.Duration(i)*time.Minute))
		r.Site, r.ErrorKind, r.Currency = site, "", mustCurrency("USD")
		r.Usage = map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemCacheRead: 20, catalog.ItemCacheWrite: 5, catalog.ItemOutput: 10}
		records = append(records, r)
	}
	records[0].Plugins = []PluginRun{{Plugin: PluginWeb, OK: true, Engine: "searxng", Uses: 1, Duration: 400 * time.Millisecond, PriceNano: 5_000_000}}
	records[1].Plugins = []PluginRun{{Plugin: PluginWeb, OK: false, Engine: "searxng", Duration: 200 * time.Millisecond}}
	records[2].Plugins = []PluginRun{{Plugin: PluginFileParser, OK: true, Engine: "mistral-ocr", Uses: 3, Duration: time.Second, PriceNano: -1}}
	early := record(small, hour.Add(-30*time.Minute)) // in an hour that starts before the window
	early.Site = site
	keep(t, db, append(records, early)...)
	for usageBatch(t, db, 10) {
	}
	var usage SiteUsage
	var stats []PluginStat
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		if usage, err = ReadSiteUsage(t.Context(), tx, site, "USD", hour.Add(-10*time.Minute), hour.Add(2*time.Hour), []string{"claude-opus", "absent"}); err != nil {
			return err
		}
		stats, err = ReadPluginStats(t.Context(), tx, hour, hour.Add(2*time.Hour), "USD")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkSiteUsage(t, usage)
	checkPluginStats(t, stats)
	var listed []CallSummary
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		listed, _, _, err = ListAdmin(t.Context(), tx, AdminQuery{Since: hour, Until: hour.Add(time.Hour), Plugin: PluginWeb, PageSize: 10})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{records[1].ID, records[0].ID}
	if len(listed) != 2 || listed[0].ID != ids[0] || listed[1].ID != ids[1] || !slices.Equal(listed[1].Plugins, records[0].Plugins) {
		t.Errorf("web calls = %+v, want %v", listed, ids)
	}
}

// checkSiteUsage checks the three settled calls of the window, and the call of an earlier hour is left out.
func checkSiteUsage(t *testing.T, usage SiteUsage) {
	t.Helper()
	if want := (Tokens{Input: 300, CacheRead: 60, CacheWrite: 15, Output: 30}); usage.Tokens != want || usage.ActiveAccounts != 2 {
		t.Errorf("usage = %+v and %d accounts, want %+v and 2", usage.Tokens, usage.ActiveAccounts, want)
	}
	if m := usage.Models["claude-opus"]; m.Tokens != 405 || m.Spend != "15000000" || len(usage.Models) != 1 {
		t.Errorf("models = %+v", usage.Models)
	}
	if d := usage.Days[time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)]; d == nil || d.ActiveAccounts != 2 || d.FirstToken.Count() != 3 {
		t.Errorf("the day = %+v", d)
	}
}

// checkPluginStats checks a web search that ran, one that failed and a parse of three pages in the window's first hour
// whose charge is not known apart from the call's other per-use items, so the parser's revenue is unknown, not zero.
func checkPluginStats(t *testing.T, stats []PluginStat) {
	t.Helper()
	hour := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	hours := func(first int64) []PluginHour {
		return []PluginHour{{At: hour, Calls: first}, {At: hour.Add(time.Hour)}}
	}
	want := []PluginStat{
		{Plugin: PluginWeb, Calls: 2, Failures: 1, Uses: 1, MeanMS: 300, Timed: 2, Price: "5000000", Hours: hours(2)},
		{Plugin: PluginFileParser, Calls: 1, Uses: 3, MeanMS: 1000, Timed: 1, Price: "", Hours: hours(1)},
	}
	if fmt.Sprint(stats) != fmt.Sprint(want) {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

// ruleTokens is catalog.Usage's own count of a call's tokens: its InputTotal plus its OutputTotal. A negative physical
// is a prompt total the upstream did not report.
func ruleTokens(t *testing.T, counts map[catalog.Item]int64, physical int64) int64 {
	t.Helper()
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err == nil && physical >= 0 {
		u, err = u.WithPhysicalInput(physical)
	}
	if err != nil {
		t.Fatal(err)
	}
	return u.InputTotal() + u.OutputTotal()
}

// storedTokens is what observability.call_tokens counts of the usage as the call log stores it.
func storedTokens(t *testing.T, db *pg.DB, counts map[catalog.Item]int64, physical int64) int64 {
	t.Helper()
	named := make(map[string]int64, len(counts))
	for item, n := range counts {
		named[item.String()] = n
	}
	stored, err := jsonx.MarshalCanonical(named)
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT observability.call_tokens($1::jsonb, $2)::bigint`, stored, physical).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestCallTokensFollowTheUsageRule: the rollups' count of a call's tokens is catalog.Usage's InputTotal plus
// OutputTotal for every billing item, as the call log stores the usage, with a prompt total and without one.
func TestCallTokensFollowTheUsageRule(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	for it := range catalog.NumItems {
		for _, physical := range []int64{-1, 1000} {
			counts := map[catalog.Item]int64{catalog.ItemOutput: 1}
			counts[it] += 7
			if got, want := storedTokens(t, db, counts, physical), ruleTokens(t, counts, physical); got != want {
				t.Errorf("%s with prompt %d: %d tokens, want %d", it, physical, got, want)
			}
		}
	}
}

// switchCalls are three settled calls of site at at: a key retry on one endpoint with an overlapping cache, a switch
// between two endpoints with a disjoint cache, and a key retry with no prompt total. want is the usage rule's tokens.
func switchCalls(t *testing.T, site uuid.UUID, at time.Time) (records []CallRecord, want int64) {
	t.Helper()
	for i, c := range []struct {
		usage    map[catalog.Item]int64
		physical int64
		retry    bool // its second attempt went to the first attempt's endpoint
	}{
		{map[catalog.Item]int64{catalog.ItemCacheRead: 80, catalog.ItemCacheWrite: 80}, 100, true},
		{map[catalog.Item]int64{catalog.ItemInput: 50, catalog.ItemCacheRead: 20, catalog.ItemCacheWrite: 30, catalog.ItemOutput: 10}, 100, false},
		{map[catalog.Item]int64{catalog.ItemInput: 5, catalog.ItemCacheRead: 3, catalog.ItemOutput: 2}, -1, true},
	} {
		r := record(uuid.NewV7(), at.Add(time.Duration(i)*time.Second))
		r.Site, r.ErrorKind, r.Usage = site, "", c.usage
		if c.physical >= 0 {
			r.PhysicalInput = &c.physical
		}
		if c.retry {
			r.Attempts[1].Endpoint = r.Attempts[0].Endpoint
		}
		records = append(records, r)
		want += ruleTokens(t, c.usage, c.physical)
	}
	return records, want
}

// TestMonitorSwitchesAndTokens: a retry with another key on the same endpoint is not a switch, a call's tokens count
// its prompt once by the rule billing prices with, and a row journaled before observability 00051, which names no call
// and whose failure the old journal could not mark, stays out of the monitor while usage_hourly still counts it.
func TestMonitorSwitchesAndTokens(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	site := uuid.NewV7()
	at := time.Now().Add(-5 * time.Minute).UTC()
	records, want := switchCalls(t, site, at)
	if want != 220 {
		t.Fatalf("the usage rule counts %d tokens, want 220", want)
	}
	keep(t, db, records...)
	if err := pgtest.Run(t.Context(), database.Superuser, fmt.Sprintf(`INSERT INTO observability.usage_journal
		(site_id, account_id, workspace_id, key_id, model, at, currency, success, usage, physical_input, price_nano)
		VALUES ('%s', uuidv7(), uuidv7(), uuidv7(), 'claude-opus', '%s', 'USD', false, '{}', -1, 0)`,
		site, at.Format(time.RFC3339Nano))); err != nil {
		t.Fatal(err)
	}
	for usageBatch(t, db, 10) {
	}
	for _, width := range []time.Duration{MonitorMinute, MonitorHour} {
		m := readMonitor(t, db, width)
		got := fmt.Sprint(m.Calls, m.Failures, m.Switched, m.Tokens, m.Total.Count())
		if wanted := fmt.Sprint(3, 0, 1, want, 3); got != wanted {
			t.Errorf("%s window: calls, failures, switched, tokens and completions %s, want %s", width, got, wanted)
		}
	}
	var usage SiteUsage
	var requests int64
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		if usage, err = ReadSiteUsage(t.Context(), tx, site, "USD", at.Add(-time.Hour), at.Add(time.Hour), []string{"claude-opus"}); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT sum(requests)::bigint FROM observability.usage_hourly WHERE site_id = $1`, site).Scan(&requests)
	}); err != nil {
		t.Fatal(err)
	}
	if got := usage.Models["claude-opus"].Tokens; got != want || requests != 4 {
		t.Errorf("the model's tokens = %d over %d requests, want %d over 4", got, requests, want)
	}
}
