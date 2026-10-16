package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The books a ledger entry moves (docs/domains/billing.md「账本」). A debit is positive, a credit negative; every
// transaction's entries sum to zero, and user_balance is a liability: the balance is minus its sum.
const (
	bookUserBalance     = "user_balance"
	bookRevenue         = "revenue"
	bookPlatformBorne   = "platform_borne"
	bookSupplierCost    = "supplier_cost"
	bookSupplierPrepaid = "supplier_prepaid"
	bookGiftExpense     = "gift_expense"
	bookCash            = "cash"
)

// event names one ledger transaction: its tenant and its idempotency key.
type event struct {
	site, account uuid.UUID
	kind          string
	request       uuid.UUID
	seq           int32
	revision      int64
}

type line struct {
	book   string
	amount int64
}

// lateRevision is the revision of the one settlement a late Settle posts after cleanup; corrections count from 1.
const lateRevision = -1

// post writes the transaction and its entries once, moving the user's balance with them. A repeated event writes
// nothing and reports false. Zero entries are left out, so an event that moves nothing is still recorded once.
func post(ctx context.Context, tx pg.Tx, e event, entries ...line) (bool, error) {
	id := uuid.NewV7()
	n, err := queries.InsertTransaction(ctx, tx, InsertTransactionParams{
		ID: id, SiteID: e.site, AccountID: e.account, Kind: e.kind, RequestID: e.request,
		AttemptSeq: e.seq, Revision: e.revision,
	})
	if err != nil || n == 0 {
		return false, wrapPost(e, err)
	}
	params := InsertEntriesParams{TransactionID: id, SiteID: e.site, AccountID: e.account}
	var sum int64
	for _, x := range entries {
		if x.amount == 0 {
			continue
		}
		params.Books, params.Amounts, sum = append(params.Books, x.book), append(params.Amounts, x.amount), sum+x.amount
		if x.book == bookUserBalance {
			params.UserDebit += x.amount
		}
	}
	if sum != 0 {
		panic(fmt.Sprintf("billing: the %s entries of %s sum to %d", e.kind, e.request, sum))
	}
	if len(params.Books) == 0 {
		return true, nil
	}
	err = queries.InsertEntries(ctx, tx, params)
	return err == nil, wrapPost(e, err)
}

func wrapPost(e event, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("billing: post %s of %s: %w", e.kind, e.request, err)
}

// charge is a request settlement: the user pays charged of price, the platform bears the rest, revenue takes price.
func charge(price, charged int64) []line {
	return []line{{bookUserBalance, charged}, {bookPlatformBorne, price - charged}, {bookRevenue, -price}}
}

// reversal undoes a charge, as a correction does before it posts the new one.
func reversal(price, charged int64) []line {
	return []line{{bookUserBalance, -charged}, {bookPlatformBorne, charged - price}, {bookRevenue, price}}
}

// Credit is a manual credit an administrator adds (docs/domains/billing.md「额度批次」): a gift, or an offline payment
// recorded at what one credit cost. ID makes it idempotent.
type Credit struct {
	ID                      uuid.UUID
	Site, Account           uuid.UUID
	Amount                  int64 // billionths of a credit, positive
	Gift                    bool
	PaidPrice, PaidCurrency string // per credit, for an offline payment
	ExpiresAt               *time.Time
	Actor                   audit.Actor
	Reason                  string
}

// AddCredit records c in tx, which must be in the platform scope, as the audit log requires: the batch, the ledger
// transaction that credits the balance, and the audit entry, all or none. A repeated ID changes nothing.
func AddCredit(ctx context.Context, tx pg.Tx, c Credit) error {
	if c.Amount <= 0 {
		return fmt.Errorf("billing: a credit of %d", c.Amount)
	}
	if _, err := lockAccount(ctx, tx, c.Account); err != nil {
		return err
	}
	source, debit := "offline", bookCash
	if c.Gift {
		source, debit, c.PaidPrice, c.PaidCurrency = "gift", bookGiftExpense, "", ""
	}
	posted, err := post(ctx, tx, event{site: c.Site, account: c.Account, kind: "credit", request: c.ID},
		line{debit, c.Amount}, line{bookUserBalance, -c.Amount})
	if err != nil || !posted {
		return err
	}
	if err := queries.InsertCreditBatch(ctx, tx, InsertCreditBatchParams{
		ID: c.ID, SiteID: c.Site, AccountID: c.Account, Source: source, Amount: c.Amount,
		PaidPrice: c.PaidPrice, PaidCurrency: c.PaidCurrency, ExpiresAt: c.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("billing: write the credit batch: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: c.Actor, Action: "credit.add", Target: audit.Target{Module: "billing", Kind: "account", ID: c.Account.String()},
		Reason: c.Reason, Detail: map[string]any{"credit": c.ID.String(), "amount": c.Amount, "source": source},
	})
}

// Reconcile lists what the daily reconciliation refuses: a transaction whose entries do not balance, a balance that
// differs from its book. It reads every tenant, so it runs in the platform scope.
func (b *Billing) Reconcile(ctx context.Context) (problems []string, err error) {
	err = b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		problems, err = queries.Unbalanced(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("billing: reconcile: %w", err)
	}
	return problems, nil
}
