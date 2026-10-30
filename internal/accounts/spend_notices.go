package accounts

import (
	"context"
	"uuid"

	"axiom/internal/platform/pg"
)

// SpendAnomalyNotice is one account-level incident found by observability. Source names the incident so a persistent
// anomaly notifies once and a later burst notifies again; Value is the recent spend in billionths of a credit.
type SpendAnomalyNotice struct {
	Account uuid.UUID
	Source  string
	Value   int64
}

// RecordSpendAnomalyNotices inserts one notice per active user of each account, honouring the user's preference.
// The detector and these rows commit together, so a notified incident always has its detector state.
func RecordSpendAnomalyNotices(ctx context.Context, tx pg.Tx, notices []SpendAnomalyNotice) error {
	if len(notices) == 0 {
		return nil
	}
	accounts := make([]uuid.UUID, len(notices))
	sources := make([]string, len(notices))
	values := make([]int64, len(notices))
	for i, n := range notices {
		accounts[i], sources[i], values[i] = n.Account, n.Source, n.Value
	}
	defaults := notificationDefaults()
	_, err := tx.Exec(ctx, `INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value)
 SELECT a.site_id,a.id,u.id,n.source,'spend_anomaly',n.value
 FROM unnest($1::uuid[],$2::text[],$3::bigint[]) n(account_id,source,value)
 JOIN accounts.accounts a ON a.id=n.account_id
 JOIN accounts.users u ON u.site_id=a.site_id AND u.account_id=a.id AND u.status='active'
 LEFT JOIN accounts.notification_preferences p ON p.user_id=u.id
 WHERE coalesce(p.spend_anomaly,$4)
 ON CONFLICT(user_id,kind,source) DO NOTHING`, accounts, sources, values, defaults.SpendAnomaly)
	return err
}
