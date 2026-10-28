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
// transaction's entries sum to zero in each currency, and user_balance is a liability: the balance is minus its sum.
const (
	bookUserBalance     = "user_balance"
	bookRevenue         = "revenue"
	bookPlatformBorne   = "platform_borne"
	bookSupplierCost    = "supplier_cost"
	bookSupplierPrepaid = "supplier_prepaid"
	bookGiftExpense     = "gift_expense"
	bookCreditExpired   = "credit_expired"
	bookCash            = "cash"
	bookFeeIncome       = "fee_income"
	bookExchange        = "exchange_gain_loss"
)

// event names one ledger transaction: its tenant and its idempotency key.
type event struct {
	site, account uuid.UUID
	kind          string
	request       uuid.UUID
	seq           int32
	revision      int64
}

// line is one entry: amount is in billionths of one unit of currency, which is siteCredits for the site's own credits
// and otherwise the currency of a payment that is not the site's. The user's balance is only ever in site credits.
type line struct {
	book     string
	amount   int64
	currency string
}

const siteCredits = ""

// lateRevision is the revision of the one settlement a late Settle posts after cleanup; corrections count from 1.
const lateRevision = -1

// post writes the transaction and its entries once, moving the user's balance with them. A repeated event writes
// nothing and reports false. Zero entries are left out, so an event that moves nothing is still recorded once. The
// entries must balance in each currency on its own: a sum over all of them would let a top-up in another currency
// balance against the wrong book.
//
// Every transaction of an account is numbered while its balance row lock is held, so the seqs of one account follow the
// order they commit, which the allocation replays by and the unlocked replay bounds itself with (#361). Every event
// takes the balance row lock before numbering, then writes its entries and balance change in the same statement.
// Both writes depend on the newly inserted event, so an idempotency conflict changes neither.
func post(ctx context.Context, tx pg.Tx, e event, entries ...line) (bool, error) {
	id := uuid.NewV7()
	params := PostLedgerParams{ID: id, SiteID: e.site, AccountID: e.account, Kind: e.kind, RequestID: e.request, AttemptSeq: e.seq, Revision: e.revision}
	for _, x := range entries {
		if x.amount == 0 {
			continue
		}
		params.Books, params.Amounts = append(params.Books, x.book), append(params.Amounts, x.amount)
		params.Currencies = append(params.Currencies, x.currency)
		if x.book == bookUserBalance {
			if x.currency != siteCredits {
				panic(fmt.Sprintf("billing: the %s of %s moves the user's balance in %s", e.kind, e.request, x.currency))
			}
			params.UserDebit += x.amount
		}
	}
	for _, x := range entries { // a handful of entries: summing each one's currency again allocates nothing
		if sum := sumIn(entries, x.currency); sum != 0 {
			panic(fmt.Sprintf("billing: the %s entries of %s sum to %d in %q", e.kind, e.request, sum, x.currency))
		}
	}
	n, err := queries.PostLedger(ctx, tx, params)
	if err == nil && n == 0 {
		// No event means either a duplicate or an account without a balance row to lock.
		_, err = lockAccount(ctx, tx, e.account)
	}
	return n > 0, wrapPost(e, err)
}

// sumIn is the sum of the entries in currency.
func sumIn(entries []line, currency string) int64 {
	var sum int64
	for _, x := range entries {
		if x.currency == currency {
			sum += x.amount
		}
	}
	return sum
}

func wrapPost(e event, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("billing: post %s of %s: %w", e.kind, e.request, err)
}

// charge is a request settlement: the user pays charged of price, the platform bears the rest, revenue takes price.
func charge(price, charged int64) []line {
	return []line{
		{bookUserBalance, charged, siteCredits}, {bookPlatformBorne, price - charged, siteCredits}, {bookRevenue, -price, siteCredits},
	}
}

// reversal undoes a charge, as a correction does before it posts the new one.
func reversal(price, charged int64) []line {
	return []line{
		{bookUserBalance, -charged, siteCredits}, {bookPlatformBorne, charged - price, siteCredits}, {bookRevenue, price, siteCredits},
	}
}

// Credit is a gift an administrator adds (docs/domains/billing.md「额度批次」). ID makes it idempotent. ExpiresAt, when
// set, is when what is left of the batch lapses (expire.go); the caller checks it lies ahead. Paid credit is
// CreditPaid's.
type Credit struct {
	ID            uuid.UUID
	Site, Account uuid.UUID
	Amount        int64 // billionths of a credit, positive
	ExpiresAt     *time.Time
	Actor         audit.Actor
	Reason        string
}

// AddCredit records c in tx, which must be in the platform scope, as the audit log requires: the batch, the ledger
// transaction that credits the balance, and the audit entry, all or none. A repeated ID changes nothing.
func AddCredit(ctx context.Context, tx pg.Tx, c Credit) error {
	if c.Amount <= 0 {
		return fmt.Errorf("billing: a credit of %d", c.Amount)
	}
	_, err := addBatch(ctx, tx, InsertCreditBatchParams{
		ID: c.ID, SiteID: c.Site, AccountID: c.Account, Source: "gift", Amount: c.Amount, ExpiresAt: c.ExpiresAt,
	}, []line{{bookGiftExpense, c.Amount, siteCredits}, {bookUserBalance, -c.Amount, siteCredits}}, c.Actor, c.Reason, map[string]any{})
	return err
}

// addBatch is what every credit does under the account's balance row lock: the credit transaction, which the
// allocation opens the batch by, then the batch and the audit entry with detail. A batch ID posted before writes
// nothing and reports false.
func addBatch(ctx context.Context, tx pg.Tx, b InsertCreditBatchParams, lines []line, actor audit.Actor, reason string,
	detail map[string]any,
) (bool, error) {
	if _, err := lockAccount(ctx, tx, b.AccountID); err != nil {
		return false, err
	}
	posted, err := post(ctx, tx, event{site: b.SiteID, account: b.AccountID, kind: "credit", request: b.ID}, lines...)
	if err != nil || !posted {
		return false, err
	}
	if err := queries.InsertCreditBatch(ctx, tx, b); err != nil {
		return false, fmt.Errorf("billing: write the credit batch: %w", err)
	}
	detail["credit"], detail["amount"], detail["source"] = b.ID.String(), b.Amount, b.Source
	if b.ExpiresAt != nil {
		detail["expires_at"] = b.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return true, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "credit.add", Target: audit.Target{Module: "billing", Kind: "account", ID: b.AccountID.String()},
		Reason: reason, Detail: detail,
	})
}
