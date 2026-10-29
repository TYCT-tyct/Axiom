package accounts

import (
	"context"
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
	caller      Caller
	tier        TierID
	preset      *uuid.UUID
	models      []string
	spendLimit  *int64
	limitReset  *string
	budget      *int64
	budgetReset *string
}

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
		row := rows[0]
		if !row.AccountActive {
			return ErrAccountSuspended
		}
		return fn(tx, Admission{
			caller: c, tier: TierID(row.TierID), preset: row.PresetID, models: row.Models,
			spendLimit: row.SpendLimit, limitReset: row.LimitReset, budget: row.Budget, budgetReset: row.BudgetReset,
		})
	})
}
