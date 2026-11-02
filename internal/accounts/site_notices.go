package accounts

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// RecordSitePaymentNotice enqueues the mandatory notice that a secondary site's payment configuration changed, one row
// per active user of the site's owner account, in tx. A main site has no owner account and enqueues nothing. The
// caller's scope must see the owner account (the admin's platform scope), so the notice and the change commit or roll
// back together. A fresh source makes each save one notice; the admin endpoint's idempotency key makes a retried request
// reuse that save rather than record a second change.
func RecordSitePaymentNotice(ctx context.Context, tx pg.Tx, site uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value,detail)
 SELECT a.site_id,a.id,u.id,$2,'site_payment_changed',0,s.name
 FROM accounts.sites s
 JOIN accounts.accounts a ON a.id=s.owner_account_id
 JOIN accounts.users u ON u.site_id=a.site_id AND u.account_id=a.id AND u.status='active'
 WHERE s.id=$1
 ON CONFLICT(user_id,kind,source) DO NOTHING`, site, uuid.NewV7().String())
	if err != nil {
		return fmt.Errorf("accounts: enqueue the site owner's payment notice: %w", err)
	}
	return nil
}
