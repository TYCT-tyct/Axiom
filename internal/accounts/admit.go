package accounts

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"uuid"

	"axiom/internal/platform/pg"
)

// AdmitRequest is what the gateway knows of the request: the Host it came to, as SiteForHost takes it, and the
// client's address, as resolved from trusted proxies.
type AdmitRequest struct {
	Domain   string
	ClientIP netip.Addr
}

// Admission is what a call with the key may do, read from the database in the admission transaction: routing and
// billing take their rules from it, never from a cache. Only Admit makes one.
type Admission struct {
	accountRate, keyRate int
	owner                UserID
	caller               Caller
	tier                 TierID
	preset               *uuid.UUID
	models               []string
	spendLimit           *int64
	limitReset           *string
	budget               *int64
	budgetReset          *string
}

// OwnerID is the account owner, used to deduplicate independent health evidence.
func (a Admission) OwnerID() UserID { return a.owner }

// Caller is the key admitted.
func (a Admission) Caller() Caller { return a.caller }

// Scope is the key's account: the scope of the call's later transactions (extending, settling). It comes only from an
// Admission, so only an admitted call has it.
func (a Admission) Scope() pg.Scope { return pg.AccountScope(uuid.UUID(a.caller.account)) }

// Tier is the account's tier now.
func (a Admission) Tier() TierID { return a.tier }

// Preset is the key's routing preset, if it has one.
func (a Admission) Preset() (uuid.UUID, bool) {
	if a.preset == nil {
		return uuid.UUID{}, false
	}
	return *a.preset, true
}

// Models is the models the key may call; nil is all of them.
func (a Admission) Models() []string { return a.models }

// SpendLimit is the key's spend limit in billionths of a credit and its reset period (daily, weekly, monthly, or
// empty for a total), if it has one.
func (a Admission) SpendLimit() (int64, string, bool) { return limit(a.spendLimit, a.limitReset) }

// RateLimits are the account and key business allowances; zero is unlimited.
func (a Admission) RateLimits() (account, key int) { return a.accountRate, a.keyRate }

// Budget is the workspace's budget, as SpendLimit is the key's.
func (a Admission) Budget() (int64, string, bool) { return limit(a.budget, a.budgetReset) }

func limit(amount *int64, reset *string) (int64, string, bool) {
	if amount == nil {
		return 0, "", false
	}
	if reset == nil {
		return *amount, "", true
	}
	return *amount, *reset, true
}

// Admit opens the call's admission transaction in the key's account scope, checks in one query that the key may be
// used now (on an enabled API domain of its site; key, workspace and account enabled; key and workspace not deleted;
// key not expired, a call key, and the client's address allowed), and hands what it read to fn in the same
// transaction. An otherwise usable key on a suspended account is ErrAccountSuspended; other refusals are ErrUnknownKey. Every node reads the database here, so a
// change committed before the call starts refuses it whatever the nodes have cached. fn's error rolls back.
func (s *Service) Admit(ctx context.Context, c Caller, r AdmitRequest, fn func(tx pg.Tx, a Admission) error) error {
	if c == (Caller{}) {
		return ErrUnknownKey
	}
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(c.account)), func(tx pg.Tx) error {
		rows, err := s.queries.AdmitKey(ctx, tx, AdmitKeyParams{
			KeyID: uuid.UUID(c.key), Domain: strings.ToLower(r.Domain), ClientIp: r.ClientIP,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrUnknownKey
		}
		a, err := admitRow(c, rows[0])
		if err != nil {
			return err
		}
		return fn(tx, a)
	})
}

// AdmitCall is one call of an admission batch: the key that made it and what the gateway knows of the request.
type AdmitCall struct {
	Caller  Caller
	Request AdmitRequest
}

// AdmitBatch checks every call in one transaction of their account's scope and hands the transaction and the results
// to fn (internal/gateway/README.md「账户闸」). Statements are pipelined, so the batch pays one round trip for the
// key checks no matter how many calls it holds. A call that may not be used now gets its error in refusals, and its
// admission is zero; the other calls still go on, because no write has happened yet. An error from fn or from the
// database rolls the whole transaction back. All calls must be for one account; the gateway groups them by account.
func (s *Service) AdmitBatch(ctx context.Context, calls []AdmitCall, fn func(tx pg.Tx, admissions []Admission, refusals []error) error) error {
	if len(calls) == 0 {
		return nil
	}
	account := calls[0].Caller.account
	for _, c := range calls[1:] {
		if c.Caller.account != account {
			return fmt.Errorf("accounts: an admission batch mixes accounts %s and %s", uuid.UUID(account), uuid.UUID(c.Caller.account))
		}
	}
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(account)), func(tx pg.Tx) error {
		admissions, refusals, err := checkKeys(ctx, tx, calls)
		if err != nil {
			return err
		}
		return fn(tx, admissions, refusals)
	})
}

// checkKeys checks the key of every call that has one, in one pipelined round trip.
func checkKeys(ctx context.Context, tx pg.Tx, calls []AdmitCall) ([]Admission, []error, error) {
	admissions, refusals := make([]Admission, len(calls)), make([]error, len(calls))
	statements := make([]pg.Statement, 0, len(calls))
	queued := make([]int, 0, len(calls))
	for i, c := range calls {
		if c.Caller == (Caller{}) {
			refusals[i] = ErrUnknownKey
			continue
		}
		statements = append(statements, pg.Statement{SQL: admitKey, Args: []any{
			uuid.UUID(c.Caller.key), strings.ToLower(c.Request.Domain), c.Request.ClientIP,
		}})
		queued = append(queued, i)
	}
	if len(statements) == 0 {
		return admissions, refusals, nil
	}
	err := tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
		for _, i := range queued {
			var row AdmitKeyRow
			err := batch.QueryRow().Scan(&row.PresetID, &row.Models, &row.SpendLimit, &row.LimitReset,
				&row.Budget, &row.BudgetReset, &row.AdmissionIds, &row.AccountActive)
			switch {
			case errors.Is(err, pg.ErrNoRows):
				refusals[i] = ErrUnknownKey
			case err != nil:
				return fmt.Errorf("accounts: check the key: %w", err)
			default:
				admissions[i], refusals[i] = admitRow(calls[i].Caller, row)
			}
		}
		return nil
	})
	return admissions, refusals, err
}

// admitRow turns the row an admission check read into what a call may do.
func admitRow(c Caller, row AdmitKeyRow) (Admission, error) {
	if !row.AccountActive {
		return Admission{}, ErrAccountSuspended
	}
	// One fixed-width binary value avoids the UUID scanner's two allocations per additional column.
	var tier TierID
	var owner UserID
	copy(tier[:], row.AdmissionIds[:16])
	copy(owner[:], row.AdmissionIds[16:])
	return Admission{
		accountRate: admissionRate(row.AdmissionIds[32:36]),
		keyRate:     admissionRate(row.AdmissionIds[36:40]),
		owner:       owner, caller: c, tier: tier, preset: row.PresetID, models: row.Models,
		spendLimit: row.SpendLimit, limitReset: row.LimitReset, budget: row.Budget, budgetReset: row.BudgetReset,
	}, nil
}

func admissionRate(data []byte) int {
	n := binary.BigEndian.Uint32(data)
	if n == math.MaxUint32 {
		return DefaultRateRequests
	}
	return int(n)
}
