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
