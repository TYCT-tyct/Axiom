package main

import (
	"context"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// ListUsers lists users, newest first, of one site or all. A page starts after the last user of the one before; the
// cursor is that user's ID, which says nothing a client may not read.
func (a adminAPI) ListUsers(ctx context.Context, request adminapi.ListUsersRequestObject) (adminapi.ListUsersResponseObject, error) {
	p := request.Params
	q := accounts.UserQuery{PageSize: pageSize(p.Limit)}
	if p.SiteId != nil {
		site, err := accounts.ParseSiteID(*p.SiteId)
		if err != nil {
			return nil, invalidField("site_id", "not a site ID")
		}
		q.Site = &site
	}
	if p.EmailPrefix != nil {
		q.EmailPrefix = *p.EmailPrefix
	}
	if p.Cursor != nil {
		after, err := accounts.ParseUserID(*p.Cursor)
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
		q.After = after
	}
	var users []accounts.UserSummary
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		users, more, err = a.accounts.Users(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.UserList{Data: make([]adminapi.UserSummary, len(users))}
	for i, u := range users {
		list.Data[i] = adminapi.UserSummary{
			Id: u.ID.TypeID(), AccountId: u.Account.TypeID(), SiteId: u.Site.TypeID(), Email: u.Email,
			Status: adminapi.UserSummaryStatus(u.Status), Tier: tierRef(u.Tier), MfaEnabled: u.MFA, CreatedAt: u.CreatedAt,
		}
	}
	if more {
		next := users[len(users)-1].ID.TypeID()
		list.NextCursor = &next
	}
	return adminapi.ListUsers200JSONResponse(list), nil
}

// balanceBody is an account's money as the contract gives it. Available is the balance less what is reserved, which
// can leave 64 bits when the figures are at their limits; text does not.
func balanceBody(v billing.BalanceView, currency platform.Currency) adminapi.Balance {
	available := new(big.Int).Sub(big.NewInt(v.Balance), big.NewInt(v.Reserved))
	return adminapi.Balance{
		Balance: adminMoney(v.Balance, currency), Reserved: adminMoney(v.Reserved, currency), Floor: adminMoney(v.Floor, currency),
		Available:      adminapi.Money{Amount: available.String(), Currency: currency.String()},
		SpentThisMonth: adminMoney(v.SpentThisMonth, currency),
	}
}

// readBalance is the account's money, in tx, which holds a scope that sees the account.
func (a adminAPI) readBalance(ctx context.Context, tx pg.Tx, account accounts.AccountID) (billing.BalanceView, error) {
	workspaces, err := a.accounts.WorkspaceIDs(ctx, tx, account)
	if err != nil {
		return billing.BalanceView{}, err
	}
	ids := make([]uuid.UUID, len(workspaces))
	for i, w := range workspaces {
		ids[i] = uuid.UUID(w)
	}
	return billing.ReadBalance(ctx, tx, uuid.UUID(account), ids)
}

// accountID reads an account's ID from a path. A malformed ID and an unknown one are the same 404.
func accountID(s string) (accounts.AccountID, error) {
	id, err := accounts.ParseAccountID(s)
	if err != nil {
		return accounts.AccountID{}, accounts.ErrNotFound
	}
	return id, nil
}

// currencyOf is the accounting currency of the site an account is in.
func (a adminAPI) currencyOf(site accounts.SiteID) (platform.Currency, error) {
	s, ok := a.accounts.Site(site)
	if !ok { // the account exists, so its site does: this node has not loaded a site made a moment ago
		return platform.Currency{}, accounts.ErrStale
	}
	return s.Currency, nil
}

// GetAccount reads one account with its balance.
func (a adminAPI) GetAccount(ctx context.Context, request adminapi.GetAccountRequestObject) (adminapi.GetAccountResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	var account accounts.AccountDetail
	var balance billing.BalanceView
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if account, err = a.accounts.Account(ctx, tx, id); err != nil {
			return err
		}
		balance, err = a.readBalance(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	currency, err := a.currencyOf(account.Site)
	if err != nil {
		return nil, err
	}
	body := adminapi.AccountDetail{
		Id: account.ID.TypeID(), SiteId: account.Site.TypeID(), Name: account.Name, Kind: adminapi.AccountDetailKind(account.Kind),
		Status: adminapi.AccountDetailStatus(account.Status), Tier: tierRef(account.Tier), Balance: balanceBody(balance, currency),
		CreatedAt: account.CreatedAt,
	}
	body.Owner.Id, body.Owner.Email = account.Owner.ID.TypeID(), account.Owner.Email
	return adminapi.GetAccount200JSONResponse(body), nil
}

// maxGrant is the most one grant gives, in billionths of a credit: 10,000 units of the site's currency. The contract
// asks for the control plane's limit on money (docs/domains/operations.md); the operations module has none yet, so
// this default stands until it does, and the owner sets it (README.md「管理接口」).
const maxGrant = 10_000 * 1_000_000_000

// grantOf reads a grant request for an account whose site counts in currency. An amount that is not in that currency
// cannot be read, a 400; one that is too small or too large, or an expiry that has passed, is a rule the grant breaks,
// a 422.
func grantOf(b *adminapi.CreditGrantRequest, currency platform.Currency, now time.Time) (amount int64, expires *time.Time, err error) {
	var p problems
	amount, _ = moneyIn(b.Amount, currency, "/amount", &p)
	if err := p.err(); err != nil {
		return 0, nil, err
	}
	if amount <= 0 || amount > maxGrant {
		return 0, nil, unprocessable("a grant is more than 0 and at most 10,000 units of the site's currency")
	}
	if b.ExpiresAt != nil {
		if !b.ExpiresAt.After(now) {
			return 0, nil, unprocessable("expires_at must be in the future")
		}
		utc := b.ExpiresAt.UTC()
		expires = &utc
	}
	return amount, expires, nil
}

// GrantCredit gives an account a gift: a credit batch, its ledger entry and its audit entry, in one transaction.
func (a adminAPI) GrantCredit(ctx context.Context, request adminapi.GrantCreditRequestObject) (adminapi.GrantCreditResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	grant, replayed, err := idempotently(ctx, a.db, "grantCredit", request.Params.IdempotencyKey, []any{request.AccountId, request.Body},
		func(tx pg.Tx) (adminapi.CreditGrant, error) {
			account, err := a.accounts.Account(ctx, tx, id)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			currency, err := a.currencyOf(account.Site)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			amount, expires, err := grantOf(request.Body, currency, time.Now())
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			batch := uuid.NewV7()
			if err := billing.AddCredit(ctx, tx, billing.Credit{
				ID: batch, Site: uuid.UUID(account.Site), Account: uuid.UUID(id), Amount: amount, ExpiresAt: expires,
				Actor: actor(ctx), Reason: request.Body.Reason,
			}); err != nil {
				return adminapi.CreditGrant{}, err
			}
			made, _, err := billing.ReadCredit(ctx, tx, batch)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			balance, err := a.readBalance(ctx, tx, id)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			return adminapi.CreditGrant{
				Id: batch.String(), AccountId: id.TypeID(), Amount: adminMoney(made.Amount, currency), ExpiresAt: made.ExpiresAt,
				CreatedAt: made.CreatedAt, BalanceAfter: balanceBody(balance, currency),
			}, nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.GrantCredit201JSONResponse{Body: grant, Headers: adminapi.GrantCredit201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
