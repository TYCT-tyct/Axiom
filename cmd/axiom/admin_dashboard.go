package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// dashboardUsage is what the dashboard shows beside the overview's exact counts: the hourly rollups' tokens, active
// accounts, spenders and model use, the time to first token, and the users who signed up (docs/domains/operations.md
// 「决策与理由」).
type dashboardUsage struct {
	usage    observability.SiteUsage
	newUsers accounts.NewUsers
}

// readDashboardUsage reads the rollups for the window, with the token and charge of the models the overview ranked.
func readDashboardUsage(ctx context.Context, tx pg.Tx, site accounts.Site, overview observability.AdminOverview, since, until time.Time) (dashboardUsage, error) {
	models := make([]string, len(overview.Models))
	for i, m := range overview.Models {
		models[i] = m.Model
	}
	var d dashboardUsage
	var err error
	if d.usage, err = observability.ReadSiteUsage(ctx, tx, uuid.UUID(site.ID), site.Currency.String(), since, until, models); err != nil {
		return d, err
	}
	d.newUsers, err = accounts.ReadNewUsers(ctx, tx, site.ID, since, until)
	return d, err
}

// addTo puts the dashboard's optional members in body, and the ranked models' tokens and charge in models, which is
// body's top_models in the overview's order.
func (d dashboardUsage) addTo(body map[string]any, models []map[string]any, overview observability.AdminOverview, money billing.PlatformReport, currency string, since, until time.Time) {
	u := d.usage
	body["tokens"] = tokensBody(u.Tokens)
	body["active_accounts"] = u.ActiveAccounts
	body["new_users"] = d.newUsers.Total
	if p, ok := u.FirstToken.Percentile(50); ok {
		body["first_token_ms_p50"] = p
	}
	spenders := make([]map[string]any, len(u.Spenders))
	for i, s := range u.Spenders {
		spenders[i] = map[string]any{"account_id": accounts.AccountID(s.Account).TypeID(), "spend": adminapi.Money{Amount: s.Spend, Currency: currency}}
	}
	body["top_spenders"] = spenders
	for i, m := range overview.Models {
		if use, ok := u.Models[m.Model]; ok {
			models[i]["tokens"], models[i]["revenue"] = use.Tokens, adminapi.Money{Amount: use.Spend, Currency: currency}
		}
	}
	body["days"] = d.days(money, currency, since, until)
}

// days is each UTC day the window touches, oldest first, with zeros on a day that had nothing. Revenue and cost are
// the ledger's reading of the exact window; the rest are the hourly rollups' (docs/domains/operations.md「决策与理由」).
func (d dashboardUsage) days(money billing.PlatformReport, currency string, since, until time.Time) []map[string]any {
	charged := map[time.Time]billing.DayMoney{}
	for _, m := range money.Days {
		charged[m.Day] = m
	}
	var out []map[string]any
	for day := since.UTC().Truncate(24 * time.Hour); day.Before(until); day = day.Add(24 * time.Hour) {
		m := charged[day]
		revenue, cost := m.Revenue, m.Cost
		if revenue == "" {
			revenue, cost = "0", "0"
		}
		row := map[string]any{
			"date": day.Format(time.DateOnly), "calls": int64(0), "errors": int64(0),
			"revenue": adminapi.Money{Amount: revenue, Currency: currency}, "cost": adminapi.Money{Amount: cost, Currency: currency},
			"tokens": tokensBody(observability.Tokens{}), "active_accounts": int64(0), "new_users": d.newUsers.Days[day],
		}
		if u := d.usage.Days[day]; u != nil {
			row["tokens"], row["active_accounts"], row["calls"], row["errors"] = tokensBody(u.Tokens), u.ActiveAccounts, u.Calls, u.Errors
			if p, ok := u.FirstToken.Percentile(50); ok {
				row["first_token_ms_p50"] = p
			}
		}
		out = append(out, row)
	}
	return out
}

func tokensBody(t observability.Tokens) map[string]int64 {
	return map[string]int64{"input": t.Input, "cache_read": t.CacheRead, "cache_write": t.CacheWrite, "output": t.Output}
}
