package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// LedgerTransaction is one event of a request's ledger: what was recorded and its entries. A transaction that moved
// nothing still exists, with no entries.
type LedgerTransaction struct {
	ID         uuid.UUID
	Kind       string // credit, settle, reverse, attempt_cost or cost_adjust
	AttemptSeq int32  // 0 for the request's own settlement; the attempt's number for its cost
	Revision   int64
	CreatedAt  time.Time
	Entries    []LedgerEntry
}

// LedgerEntry is one line of a ledger transaction: a book and an amount in billionths of a credit. A debit is positive,
// a credit negative; a transaction's entries sum to zero.
type LedgerEntry struct {
	Book   string
	Amount int64
}

// ledgerEntryJSON is an entry as RequestLedger returns it inside the transaction's array.
type ledgerEntryJSON struct {
	Book   string `json:"book"`
	Amount int64  `json:"amount"`
}

// LedgerOf returns the ledger transactions of a request, oldest first, each with its entries. Every transaction of a
// call carries the call's request ID, so this is the ledger half of the chain that starts at a call record
// (README.md「按请求读账本」): no transaction or entry ID is copied onto the call. The entries carry our supplier cost
// as well as the user's charge, so this is an internal read: only an operator's or an administrator's handler may call
// it. The transaction's scope is the whole of the access control: an account's scope sees only its own transactions, the
// platform scope all of them. A request with no transactions is an empty result, not an error.
func LedgerOf(ctx context.Context, tx pg.Tx, requestID uuid.UUID) ([]LedgerTransaction, error) {
	rows, err := queries.RequestLedger(ctx, tx, requestID)
	if err != nil {
		return nil, fmt.Errorf("billing: read the ledger of %s: %w", requestID, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	transactions := make([]LedgerTransaction, len(rows))
	for i, r := range rows {
		id, err := uuid.Parse(r.ID)
		if err != nil {
			return nil, fmt.Errorf("billing: read the ledger of %s: %w", requestID, err)
		}
		var entries []ledgerEntryJSON
		if err := jsonx.Unmarshal([]byte(r.Entries), &entries); err != nil {
			return nil, fmt.Errorf("billing: read the ledger of %s: %w", requestID, err)
		}
		lines := make([]LedgerEntry, len(entries))
		for j, e := range entries {
			lines[j] = LedgerEntry{Book: e.Book, Amount: e.Amount}
		}
		transactions[i] = LedgerTransaction{
			ID: id, Kind: r.Kind, AttemptSeq: r.AttemptSeq, Revision: r.Revision, CreatedAt: r.CreatedAt,
			Entries: lines,
		}
	}
	return transactions, nil
}
