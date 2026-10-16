package billing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Funds is an account's money as it stands, in billionths of a credit.
type Funds struct {
	Balance int64 // the ledger's total: what the account has
	Spent   int64 // everything the account was ever charged, from its workspaces' spend counters
}

// ReadFunds reads the account's balance row and its lifetime spend in tx, without locking, for an endpoint that only
// shows them. workspaces are all of the account's workspaces, deleted ones included: the spend counters of a workspace
// and of the keys in it each hold every charge, so the account's total is the workspaces' alone. An account with no
// balance row has no money (Reserve treats it alike).
func ReadFunds(ctx context.Context, tx pg.Tx, account uuid.UUID, workspaces []uuid.UUID) (Funds, error) {
	row, err := queries.ReadFunds(ctx, tx, ReadFundsParams{AccountID: account, Workspaces: workspaces})
	if errors.Is(err, pg.ErrNoRows) {
		return Funds{}, nil
	}
	if err != nil {
		return Funds{}, fmt.Errorf("billing: read the funds of an account: %w", err)
	}
	return Funds(row), nil
}

// KeyUsage is what a key has spent, in billionths of a credit. The day, week and month are the current UTC ones by the
// database's clock.
type KeyUsage struct {
	Total, Day, Week, Month int64
	// Counted is what the key's limit counts now: spent and reserved over the window of its reset, as admission checks
	// it. The limit's remaining is the limit less this.
	Counted int64
}

// ReadKeyUsage reads the key's counters in tx without locking. reset is the key's limit reset, empty for none or for a
// total.
func ReadKeyUsage(ctx context.Context, tx pg.Tx, key uuid.UUID, reset Reset) (KeyUsage, error) {
	row, err := queries.ReadKeyUsage(ctx, tx, ReadKeyUsageParams{KeyID: key, Reset: string(reset)})
	if err != nil {
		return KeyUsage{}, fmt.Errorf("billing: read the usage of a key: %w", err)
	}
	return KeyUsage{Total: row.Total, Day: row.Daily, Week: row.Weekly, Month: row.Monthly, Counted: row.Limited}, nil
}
