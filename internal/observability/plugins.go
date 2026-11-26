package observability

import (
	"context"
	"fmt"
	"time"

	"axiom/internal/platform/pg"
)

// PluginStat is one plugin's runs over the hours of a window, as plugin_hourly holds them (docs/domains/observability.md「监控汇总」).
type PluginStat struct {
	Plugin          string
	Calls, Failures int64
	Uses            int64
	MeanMS          int64 // over the timed runs; meaningless when Timed is 0
	Timed           int64
	Price           string       // what the runs were charged in the currency asked for, decimal billionths
	Hours           []PluginHour // every hour of the window, oldest first
}

// PluginHour is the calls that ran the plugin in one hour.
type PluginHour struct {
	At    time.Time
	Calls int64
}

// Each hour's calls, and each plugin's totals over the window: grouping(hour) is 1 on a total.
const pluginStatsSQL = `SELECT grouping(hour)::int, coalesce(hour, '0001-01-01T00:00:00Z'::timestamptz), plugin,
 sum(calls)::bigint, sum(failures)::bigint, sum(uses)::bigint, sum(ms)::bigint, sum(timed)::bigint,
 coalesce(sum(price) FILTER (WHERE currency = $3), 0)::text
 FROM observability.plugin_hourly WHERE hour >= $1 AND hour < $2
 GROUP BY GROUPING SETS ((hour, plugin), (plugin))`

// ReadPluginStats reads every site's plugin runs over the hours that start in [since, until), in tx, which must hold
// the platform scope; money counts only the runs in currency. Every plugin has a stat, with every hour, even when it
// never ran.
func ReadPluginStats(ctx context.Context, tx pg.Tx, since, until time.Time, currency string) ([]PluginStat, error) {
	first := FirstHour(since)
	stats := []PluginStat{{Plugin: PluginWeb, Price: "0"}, {Plugin: PluginFileParser, Price: "0"}}
	index := map[string]int{PluginWeb: 0, PluginFileParser: 1}
	for i := range stats {
		for at := first; at.Before(until); at = at.Add(time.Hour) {
			stats[i].Hours = append(stats[i].Hours, PluginHour{At: at})
		}
	}
	rows, err := tx.Query(ctx, pluginStatsSQL, first, until, currency)
	if err != nil {
		return nil, fmt.Errorf("observability: read the plugins' use: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var total int
		var at time.Time
		var plugin, price string
		var calls, failures, uses, ms, timed int64
		if err := rows.Scan(&total, &at, &plugin, &calls, &failures, &uses, &ms, &timed, &price); err != nil {
			return nil, err
		}
		i, ok := index[plugin]
		if !ok {
			continue // a plugin this build does not know of, from a newer node
		}
		s := &stats[i]
		if total == 0 {
			s.hour(first, at, calls)
			continue
		}
		s.Calls, s.Failures, s.Uses, s.Timed, s.Price = calls, failures, uses, timed, price
		if timed > 0 {
			s.MeanMS = ms / timed
		}
	}
	return stats, rows.Err()
}

// hour sets the calls of the hour at, of the hours from first.
func (s *PluginStat) hour(first, at time.Time, calls int64) {
	if i := int(at.Sub(first) / time.Hour); !at.Before(first) && i < len(s.Hours) {
		s.Hours[i].Calls = calls
	}
}
