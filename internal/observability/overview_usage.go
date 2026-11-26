package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// SiteUsage is what the hourly rollups say of one site over a window, for the operations dashboard: the settled calls'
// tokens, the accounts that called, what the most charged accounts and the given models were charged, and the time to
// first token. The rollups are by hour, so the window is the hours that start in it (FirstHour); everything else on
// the dashboard reads the exact window (docs/domains/observability.md「监控汇总」).
type SiteUsage struct {
	Tokens         Tokens
	ActiveAccounts int64
	Spenders       []AccountSpend      // most charged first, at most adminTop
	Models         map[string]ModelUse // the models asked for that had calls
	FirstToken     Histogram
	Days           map[time.Time]*UsageDay // by the UTC day's start
}

// Tokens are the settled calls' tokens by kind; Input leaves the cached ones out.
type Tokens struct{ Input, CacheRead, CacheWrite, Output int64 }

// AccountSpend is what one account's settled calls were charged, as decimal billionths of the currency asked for.
type AccountSpend struct {
	Account uuid.UUID
	Spend   string
}

// ModelUse is one model's tokens of every kind, and what its calls were charged in the currency asked for.
type ModelUse struct {
	Tokens int64
	Spend  string
}

// UsageDay is the same for one UTC day of the window, with its settled calls and the failed ones among them.
type UsageDay struct {
	Calls, Errors  int64
	Tokens         Tokens
	ActiveAccounts int64
	FirstToken     Histogram
}

// FirstHour is the first hour the hourly rollups count for a window starting at since: the first that starts in it.
func FirstHour(since time.Time) time.Time {
	first := since.UTC().Truncate(time.Hour)
	if first.Before(since) {
		first = first.Add(time.Hour)
	}
	return first
}

const siteUsageWhere = ` FROM observability.usage_hourly WHERE site_id = $1 AND hour >= $2 AND hour < $3`

// The day sets and the window's total come from one read; grouping() is 1 on the total.
const siteUsageTokensSQL = `SELECT grouping(day)::int, coalesce(day, '0001-01-01T00:00:00Z'::timestamptz),
 coalesce(sum(input), 0)::bigint, coalesce(sum(cache_read), 0)::bigint, coalesce(sum(cache_write), 0)::bigint,
 coalesce(sum(output), 0)::bigint, count(DISTINCT account_id), coalesce(sum(requests), 0)::bigint,
 coalesce(sum(requests - successes), 0)::bigint
 FROM (SELECT account_id, input, cache_read, cache_write, output, requests, successes,
     date_trunc('day', hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS day` + siteUsageWhere + `) u
 GROUP BY GROUPING SETS ((day), ())`

const siteSpendersSQL = `SELECT account_id, sum(spend)::text` + siteUsageWhere + ` AND currency = $4
 GROUP BY account_id ORDER BY sum(spend) DESC, account_id LIMIT $5`

const siteModelsSQL = `SELECT model,
 (coalesce(sum(input), 0) + coalesce(sum(cache_read), 0) + coalesce(sum(cache_write), 0) + coalesce(sum(output), 0))::bigint,
 coalesce(sum(spend) FILTER (WHERE currency = $4), 0)::text` + siteUsageWhere + ` AND model = ANY($5::text[])
 GROUP BY model`

const siteFirstOutputSQL = `SELECT date_trunc('day', hour AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', bin, sum(calls)::bigint
 FROM observability.first_token_hourly WHERE site_id = $1 AND hour >= $2 AND hour < $3
 GROUP BY 1, 2`

// ReadSiteUsage reads site's hours from FirstHour(since) until until in tx, which must hold the platform scope, with
// money in currency and models the ones to say the tokens and charges of. It reads the rollups through their site and
// hour index, whose rows grow with the accounts, keys and models in use, not with the calls.
func ReadSiteUsage(ctx context.Context, tx pg.Tx, site uuid.UUID, currency string, since, until time.Time, models []string) (SiteUsage, error) {
	first := FirstHour(since)
	u := SiteUsage{Models: map[string]ModelUse{}, Days: map[time.Time]*UsageDay{}}
	if err := u.readTokens(ctx, tx, site, first, until); err != nil {
		return SiteUsage{}, err
	}
	spenders, err := tx.Query(ctx, siteSpendersSQL, site, first, until, currency, adminTop)
	if err != nil {
		return SiteUsage{}, fmt.Errorf("observability: read the top spenders: %w", err)
	}
	for spenders.Next() {
		var s AccountSpend
		if err := spenders.Scan(&s.Account, &s.Spend); err != nil {
			spenders.Close()
			return SiteUsage{}, err
		}
		u.Spenders = append(u.Spenders, s)
	}
	spenders.Close()
	if err := spenders.Err(); err != nil {
		return SiteUsage{}, err
	}
	rows, err := tx.Query(ctx, siteModelsSQL, site, first, until, currency, models)
	if err != nil {
		return SiteUsage{}, fmt.Errorf("observability: read the models' tokens: %w", err)
	}
	for rows.Next() {
		var model string
		var m ModelUse
		if err := rows.Scan(&model, &m.Tokens, &m.Spend); err != nil {
			rows.Close()
			return SiteUsage{}, err
		}
		u.Models[model] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SiteUsage{}, err
	}
	return u, u.readFirstTokens(ctx, tx, site, first, until)
}

func (u *SiteUsage) day(at time.Time) *UsageDay {
	at = at.UTC()
	d := u.Days[at]
	if d == nil {
		d = &UsageDay{}
		u.Days[at] = d
	}
	return d
}

func (u *SiteUsage) readTokens(ctx context.Context, tx pg.Tx, site uuid.UUID, first, until time.Time) error {
	rows, err := tx.Query(ctx, siteUsageTokensSQL, site, first, until)
	if err != nil {
		return fmt.Errorf("observability: read the site's tokens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var total int
		var at time.Time
		var t Tokens
		var active, calls, errors int64
		if err := rows.Scan(&total, &at, &t.Input, &t.CacheRead, &t.CacheWrite, &t.Output, &active, &calls, &errors); err != nil {
			return err
		}
		if total == 1 {
			u.Tokens, u.ActiveAccounts = t, active
			continue
		}
		d := u.day(at)
		d.Tokens, d.ActiveAccounts, d.Calls, d.Errors = t, active, calls, errors
	}
	return rows.Err()
}

func (u *SiteUsage) readFirstTokens(ctx context.Context, tx pg.Tx, site uuid.UUID, first, until time.Time) error {
	rows, err := tx.Query(ctx, siteFirstOutputSQL, site, first, until)
	if err != nil {
		return fmt.Errorf("observability: read the site's first tokens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var bin int16
		var calls int64
		if err := rows.Scan(&at, &bin, &calls); err != nil {
			return err
		}
		u.FirstToken[bin] += calls
		u.day(at).FirstToken[bin] += calls
	}
	return rows.Err()
}
