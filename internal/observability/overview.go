package observability

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// AdminOverview is one site's calls over a window for the operations dashboard (README.md「运营看板」): the totals and
// the accounts and models that called most, as the call log recorded them.
type AdminOverview struct {
	Calls, Errors int64
	Accounts      []AdminAccountCalls
	Models        []AdminModelCalls
	Coverage      Coverage
}

// AdminAccountCalls is one account's calls and errors in the window.
type AdminAccountCalls struct {
	Account       uuid.UUID
	Calls, Errors int64
}

// AdminModelCalls is one model's calls and errors in the window.
type AdminModelCalls struct {
	Model         string
	Calls, Errors int64
}

// adminTop is how many accounts and models the dashboard lists; the page shows one screenful, not a ranking.
const adminTop = 10

// Coverage is how far back the call log still answers from PostgreSQL: Since is its oldest remaining monthly
// partition, and months records the months that are still there. A window that crosses a month missing from months
// reaches calls the archive removed (README.md「分区」); its call counts and rankings are incomplete, while the ledger
// report over the same window still holds every settled call.
type Coverage struct {
	Since  time.Time
	Known  bool
	months map[time.Time]struct{}
}

// Covers reports whether every month the window touches is still retained. A gap in the middle matters: retention
// records a failed month and continues with newer ones, so an older month can remain while a newer one is gone, and
// the oldest partition alone would present the window as complete. A log with no monthly partition left covers no
// window: everything it held is archived.
func (c Coverage) Covers(since, until time.Time) bool {
	if !c.Known {
		return false
	}
	if !until.After(since) {
		return true
	}
	for month := monthStart(since); month.Before(until); month = month.AddDate(0, 1, 0) {
		if _, ok := c.months[month]; !ok {
			return false
		}
	}
	return true
}

// monthStart is the first instant of t's UTC month, the key the partitions use.
func monthStart(t time.Time) time.Time {
	year, month, _ := t.UTC().Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
}

// CallCoverage reads the log's coverage in tx. Every monthly partition the log still has is a month it can answer
// from; the migration pre-created every month from 2026-10, so no call predates the oldest one; the default partition
// holds only dates outside those months, later than them.
func CallCoverage(ctx context.Context, tx pg.Tx) (Coverage, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname
 FROM pg_partition_tree('observability.call_log'::regclass) t
 JOIN pg_class c ON c.oid = t.relid
 WHERE t.isleaf`)
	if err != nil {
		return Coverage{}, fmt.Errorf("observability: read the call log's partitions: %w", err)
	}
	defer rows.Close()
	var coverage Coverage
	months := make(map[time.Time]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return Coverage{}, err
		}
		month, ok := monthOf("call_log", name)
		if !ok {
			continue
		}
		months[month] = struct{}{}
		if !coverage.Known || month.Before(coverage.Since) {
			coverage = Coverage{Since: month, Known: true}
		}
	}
	if err := rows.Err(); err != nil {
		return Coverage{}, err
	}
	coverage.months = months
	return coverage, nil
}

// The dashboard's queries read the site's window through the admin time index: the plain range prunes partitions, the
// UTC expression is the index's, and an error is any error_kind but the empty string.
const adminOverviewWhere = ` FROM observability.call_log
 WHERE site_id = $1 AND at >= $2 AND at < $3
 AND (at AT TIME ZONE 'UTC') >= $2 AND (at AT TIME ZONE 'UTC') < $3`

const adminOverviewCountsSQL = `SELECT count(*), count(*) FILTER (WHERE error_kind IS DISTINCT FROM '')` +
	adminOverviewWhere

const adminTopAccountsSQL = `SELECT account_id, count(*), count(*) FILTER (WHERE error_kind IS DISTINCT FROM '')` +
	adminOverviewWhere + ` GROUP BY account_id ORDER BY count(*) DESC, account_id LIMIT $4`

const adminTopModelsSQL = `SELECT model, count(*), count(*) FILTER (WHERE error_kind IS DISTINCT FROM '')` +
	adminOverviewWhere + ` GROUP BY model ORDER BY count(*) DESC, model LIMIT $4`

// ReadAdminOverview reads the site's calls over [since, until) in tx, which must hold the platform scope (or the
// site's). The log's own time axis is the call's start, the same axis the ledger report uses, so the call count and the
// revenue describe the same calls.
func ReadAdminOverview(ctx context.Context, tx pg.Tx, site uuid.UUID, since, until time.Time) (AdminOverview, error) {
	var overview AdminOverview
	var err error
	if overview.Coverage, err = CallCoverage(ctx, tx); err != nil {
		return AdminOverview{}, err
	}
	if err := tx.QueryRow(ctx, adminOverviewCountsSQL, site, since, until).Scan(&overview.Calls, &overview.Errors); err != nil {
		return AdminOverview{}, fmt.Errorf("observability: count the calls over the window: %w", err)
	}
	accounts, err := tx.Query(ctx, adminTopAccountsSQL, site, since, until, adminTop)
	if err != nil {
		return AdminOverview{}, fmt.Errorf("observability: read the top accounts: %w", err)
	}
	for accounts.Next() {
		var a AdminAccountCalls
		if err := accounts.Scan(&a.Account, &a.Calls, &a.Errors); err != nil {
			accounts.Close()
			return AdminOverview{}, err
		}
		overview.Accounts = append(overview.Accounts, a)
	}
	accounts.Close()
	if err := accounts.Err(); err != nil {
		return AdminOverview{}, err
	}
	models, err := tx.Query(ctx, adminTopModelsSQL, site, since, until, adminTop)
	if err != nil {
		return AdminOverview{}, fmt.Errorf("observability: read the top models: %w", err)
	}
	for models.Next() {
		var m AdminModelCalls
		if err := models.Scan(&m.Model, &m.Calls, &m.Errors); err != nil {
			models.Close()
			return AdminOverview{}, err
		}
		overview.Models = append(overview.Models, m)
	}
	models.Close()
	if err := models.Err(); err != nil {
		return AdminOverview{}, err
	}
	return overview, nil
}
