package main

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// monitorDurations are the completion-time distribution's upper edges in milliseconds, each an edge of the latency
// bins so a bin of the distribution is a whole number of them.
var monitorDurations = []int32{1000, 2000, 5000, 10000, 20000, 30000, 60000, 120000}

// upstreamKinds are the call error kinds that are the supplier's side: its endpoint failed or timed out, or none of
// the endpoints could serve. The rest are the request's, the account's or this node's.
var upstreamKinds = map[string]bool{
	protocol.UpstreamFailed.String(): true, protocol.Timeout.String(): true, protocol.NoEndpoint.String(): true,
}

// GetOperationsMonitor reads the monitor rollups of the last hour by minute or the last day by hour, and names each
// endpoint's line (docs/domains/observability.md「决策与理由」).
func (a adminAPI) GetOperationsMonitor(ctx context.Context, r adminapi.GetOperationsMonitorRequestObject) (adminapi.GetOperationsMonitorResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	window, width := adminapi.N1h, observability.MonitorMinute
	if r.Params.Window != nil {
		switch *r.Params.Window {
		case adminapi.N1h:
		case adminapi.N24h:
			window, width = adminapi.N24h, observability.MonitorHour
		default:
			return nil, invalidField("window", "choose 1h or 24h")
		}
	}
	var m observability.Monitor
	var lines map[uuid.UUID]suppliers.Line
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if m, err = observability.ReadMonitor(ctx, tx, width, time.Now()); err != nil {
			return err
		}
		endpoints := make([]uuid.UUID, len(m.Endpoints))
		for i, e := range m.Endpoints {
			endpoints[i] = e.Endpoint
		}
		lines, err = suppliers.EndpointLines(ctx, tx, endpoints)
		return err
	})
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"window": window, "since": m.Since, "until": m.Until, "bucket_seconds": int(width / time.Second),
		"buckets": monitorBuckets(m.Buckets), "calls": m.Calls, "failures": m.Failures, "tokens": m.Tokens,
		"switched_calls": m.Switched, "first_token_ms": percentiles(&m.FirstToken), "total_ms": percentiles(&m.Total),
		"durations": durations(&m.Total), "failure_causes": failureCauses(m.Causes), "lines": monitorLines(m.Endpoints, lines),
	}
	return adminJSON[adminapi.GetOperationsMonitor200JSONResponse](body)
}

func monitorBuckets(buckets []observability.MonitorBucket) []map[string]any {
	out := make([]map[string]any, len(buckets))
	for i, b := range buckets {
		out[i] = map[string]any{"at": b.At, "calls": b.Calls, "failures": b.Failures, "tokens": b.Tokens}
		if p, ok := b.FirstToken.Percentile(50); ok {
			out[i]["first_token_ms_p50"] = p
		}
		if p, ok := b.FirstToken.Percentile(95); ok {
			out[i]["first_token_ms_p95"] = p
		}
	}
	return out
}

// percentiles is the contract's p50 to p99 of h; empty when no call had the measure.
func percentiles(h *observability.Histogram) map[string]int64 {
	out := map[string]int64{}
	for _, p := range []struct {
		name    string
		percent int64
	}{{"p50", 50}, {"p90", 90}, {"p95", 95}, {"p99", 99}} {
		if v, ok := h.Percentile(p.percent); ok {
			out[p.name] = v
		}
	}
	return out
}

// durations spreads the succeeded calls' total times over monitorDurations and the open bin after them.
func durations(h *observability.Histogram) []map[string]any {
	out := make([]map[string]any, 0, len(monitorDurations)+1)
	var below int64
	for _, edge := range monitorDurations {
		n := h.Below(edge)
		out = append(out, map[string]any{"below_ms": edge, "calls": n - below})
		below = n
	}
	return append(out, map[string]any{"calls": h.Count() - below})
}

func failureCauses(causes map[string]int64) []map[string]any {
	kinds := slices.Sorted(maps.Keys(causes))
	slices.SortStableFunc(kinds, func(a, b string) int { return cmp.Compare(causes[b], causes[a]) })
	out := make([]map[string]any, len(kinds))
	for i, k := range kinds {
		out[i] = map[string]any{"error_kind": k, "upstream": upstreamKinds[k], "calls": causes[k]}
	}
	return out
}

// monitorLines adds the endpoints up by the line each belongs to, most calls first; an endpoint the suppliers no
// longer have is left out.
func monitorLines(endpoints []observability.MonitorEndpoint, lines map[uuid.UUID]suppliers.Line) []map[string]any {
	type line struct {
		suppliers.Line
		calls, failures int64
		firstToken      observability.Histogram
	}
	var order []suppliers.Line
	byLine := map[suppliers.Line]*line{}
	for _, e := range endpoints {
		key, ok := lines[e.Endpoint]
		if !ok {
			continue
		}
		l := byLine[key]
		if l == nil {
			l = &line{Line: key}
			byLine[key] = l
			order = append(order, key)
		}
		l.calls, l.failures = l.calls+e.Calls, l.failures+e.Failures
		l.firstToken.Add(&e.FirstToken)
	}
	slices.SortStableFunc(order, func(a, b suppliers.Line) int { return cmp.Compare(byLine[b].calls, byLine[a].calls) })
	out := make([]map[string]any, len(order))
	for i, key := range order {
		l := byLine[key]
		out[i] = map[string]any{"supplier_id": l.Supplier.String(), "group_id": l.Group.String(), "calls": l.calls, "failures": l.failures}
		if p, ok := l.firstToken.Percentile(50); ok {
			out[i]["first_token_ms_p50"] = p
		}
	}
	return out
}

// GetPluginStats reads the plugins' hourly rollup over the hours that start in the window, with money in the
// request's site's currency (docs/domains/observability.md「决策与理由」).
func (a adminAPI) GetPluginStats(ctx context.Context, r adminapi.GetPluginStatsRequestObject) (adminapi.GetPluginStatsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	w, err := boundedCallWindow(observability.OwnQuery{}, r.Params.Since, r.Params.Until, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	call := callOf(ctx)
	currency := call.site.Currency.String()
	var stats []observability.PluginStat
	err = a.db.ReadTx(ctx, call.scope, func(tx pg.Tx) (err error) {
		stats, err = observability.ReadPluginStats(ctx, tx, w.Since, w.Until, currency)
		return err
	})
	if err != nil {
		return nil, err
	}
	plugins := make([]map[string]any, len(stats))
	for i, s := range stats {
		hours := make([]map[string]any, len(s.Hours))
		for j, h := range s.Hours {
			hours[j] = map[string]any{"at": h.At, "calls": h.Calls}
		}
		plugins[i] = map[string]any{"plugin": s.Plugin, "calls": s.Calls, "failures": s.Failures, "uses": s.Uses, "hours": hours}
		if s.Price != "" {
			plugins[i]["revenue"] = adminapi.Money{Amount: s.Price, Currency: currency}
		}
		if s.Timed > 0 {
			plugins[i]["mean_ms"] = s.MeanMS
		}
	}
	return adminJSON[adminapi.GetPluginStats200JSONResponse](map[string]any{"since": w.Since, "until": w.Until, "plugins": plugins})
}
