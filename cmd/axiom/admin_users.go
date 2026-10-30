package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
			SignInDisabled: new(u.Status == "disabled"), Status: adminapi.UserSummaryStatus(u.Status), Tier: tierRef(u.Tier), MfaEnabled: u.MFA, CreatedAt: u.CreatedAt,
		}
	}
	if more {
		next := users[len(users)-1].ID.TypeID()
		list.NextCursor = &next
	}
	return adminapi.ListUsers200JSONResponse(list), nil
}

// balanceBody is an account's money as the contract gives it. Available is the balance less reservations and freezes, which
// can leave 64 bits when the figures are at their limits; text does not.
func balanceBody(v billing.BalanceView, currency platform.Currency) adminapi.Balance {
	available := v.Available()
	return adminapi.Balance{
		Balance: adminMoney(v.Balance, currency), Reserved: adminMoney(v.Reserved, currency), Floor: adminMoney(v.Floor, currency),
		Available:      adminapi.Money{Amount: available.String(), Currency: currency.String()},
		SpentThisMonth: adminMoney(v.SpentThisMonth, currency),
	}
}

// readBalance is the account's money, in tx, which holds a scope that sees the account.
func (a adminAPI) readBalance(ctx context.Context, tx pg.Tx, account accounts.AccountID) (billing.BalanceView, error) {
	ids, err := a.accounts.AccountWorkspaces(ctx, tx, account)
	if err != nil {
		return billing.BalanceView{}, err
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
	var limits adminapi.KeyLimitsView
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if account, err = a.accounts.Account(ctx, tx, id); err != nil {
			return err
		}
		own, effective, err := a.accounts.AccountKeyLimits(ctx, tx, id)
		if err != nil {
			return err
		}
		limits = keyLimitsBody(own, effective)
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
		CallsSuspended: new(account.Status == "disabled"), SignInDisabled: new(account.Owner.SignInDisabled),
		KeyLimits: limits, CreatedAt: account.CreatedAt,
	}
	body.Owner.Id, body.Owner.Email = account.Owner.ID.TypeID(), account.Owner.Email
	return adminapi.GetAccount200JSONResponse(body), nil
}

// maxGrant is the most one grant gives, in billionths of a credit: 10,000 units of the site's currency. The contract
// asks for the control plane's limit on money (docs/domains/operations.md); the operations module has none yet, so
// this default stands until it does, and the owner sets it (README.md「管理接口」).
const maxGrant = 10_000 * 1_000_000_000

// creditInput is a grant request read for an account: a gift of amount, or an offline payment that buys what it
// converts to.
type creditInput struct {
	amount  int64 // a gift's, in billionths of a credit
	payment *billing.Payment
	channel string
	expires *time.Time
}

// grantOf reads a grant request for an account whose site counts in currency. A gift's amount that is not in that
// currency, or a payment's that is not text of an integer, cannot be read, a 400; a source given what the other needs,
// an amount out of range, a payment that is not whole minor units of a currency an exchange rate is kept for, or an
// expiry that has passed, is a rule the grant breaks, a 422.
func grantOf(b *adminapi.CreditGrantRequest, currency platform.Currency, now time.Time) (g creditInput, err error) {
	if b.ExpiresAt != nil {
		if !b.ExpiresAt.After(now) {
			return creditInput{}, unprocessableFields("/expires_at", "expires_at must be in the future")
		}
		utc := b.ExpiresAt.UTC()
		g.expires = &utc
	}
	if b.Source != nil && *b.Source == adminapi.CreditSourceOfflinePayment {
		if b.Amount != nil || b.Payment == nil {
			return creditInput{}, unprocessableFields("/payment", "an offline payment gives payment, and no amount")
		}
		g.payment, g.channel, err = offlinePayment(b.Payment)
		return g, err
	}
	if b.Payment != nil || b.Amount == nil {
		return creditInput{}, unprocessableFields("/amount", "a gift gives amount, and no payment")
	}
	var p problems
	g.amount, _ = moneyIn(*b.Amount, currency, "/amount", &p)
	if err := p.err(); err != nil {
		return creditInput{}, err
	}
	if g.amount <= 0 || g.amount > maxGrant {
		return creditInput{}, unprocessable("a grant is more than 0 and at most 10,000 units of the site's currency")
	}
	return g, nil
}

// offlinePayment reads what an administrator says was paid: an amount of its own currency, in whole minor units of it.
func offlinePayment(m *adminapi.OfflinePayment) (*billing.Payment, string, error) {
	amount, err := strconv.ParseInt(m.Amount.Amount, 10, 64)
	if err != nil {
		var p problems
		p.add("/payment/amount/amount", "does not fit 64 bits")
		return nil, "", p.err()
	}
	currency, err := platform.ParseCurrency(m.Amount.Currency)
	if err != nil {
		return nil, "", unprocessableFields("/payment/amount/currency", err.Error())
	}
	minor, err := billing.MinorUnits(currency, amount)
	if err != nil {
		return nil, "", unprocessableFields("/payment/amount", err.Error())
	}
	if minor <= 0 {
		return nil, "", unprocessableFields("/payment/amount/amount", "a payment is more than 0")
	}
	channel := strings.TrimSpace(m.Channel)
	if channel == "" || utf8.RuneCountInString(channel) > 200 {
		return nil, "", unprocessableFields("/payment/channel", "the channel is 1 to 200 characters")
	}
	return &billing.Payment{Currency: currency, Paid: minor}, channel, nil
}

// GrantCredit gives an account credit, a gift or an offline payment: a credit batch, its ledger entries and its audit
// entry, in one transaction.
func (a adminAPI) GrantCredit(ctx context.Context, request adminapi.GrantCreditRequestObject) (adminapi.GrantCreditResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "grantCredit", request.Params.IdempotencyKey, []any{request.AccountId, request.Body},
		func(tx pg.Tx) (adminapi.CreditGrant, error) {
			account, err := a.accounts.Account(ctx, tx, id)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			currency, err := a.currencyOf(account.Site)
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			g, err := grantOf(request.Body, currency, time.Now())
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			batch := uuid.NewV7()
			made := adminapi.CreditGrant{Id: batch.String(), AccountId: id.TypeID(), Source: adminapi.CreditSourceGift}
			if g.payment == nil {
				err = billing.AddCredit(ctx, tx, billing.Credit{
					ID: batch, Site: uuid.UUID(account.Site), Account: uuid.UUID(id), Amount: g.amount, ExpiresAt: g.expires,
					Actor: actor(ctx), Reason: request.Body.Reason,
				})
			} else {
				made.Source, made.Payment = adminapi.CreditSourceOfflinePayment, &adminapi.OfflinePayment{Amount: request.Body.Payment.Amount, Channel: g.channel}
				made.ExchangeRate, err = creditOffline(ctx, tx, billing.PaidCredit{
					ID: batch, Site: uuid.UUID(account.Site), Account: uuid.UUID(id), SiteCurrency: currency, Payment: *g.payment,
					Channel: billing.Channel{Offline: g.channel}, ExpiresAt: g.expires, Actor: actor(ctx), Reason: request.Body.Reason,
				})
			}
			if err != nil {
				return adminapi.CreditGrant{}, err
			}
			return a.granted(ctx, tx, id, batch, made, currency)
		})
	if err != nil {
		return nil, err
	}
	if body.Source == "" { // a response stored before grants had a source, when every grant was a gift
		body.Source = adminapi.CreditSourceGift
	}
	return adminapi.GrantCredit201JSONResponse{Body: body, Headers: adminapi.GrantCredit201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// creditOffline credits p, a payment an administrator received offline, with what it buys at the reference exchange
// rate in force, none in the site's own currency, and returns that rate. The current rate is used as it stands: the
// buffer a top-up adds comes with payments' quotes, which an offline payment does not have.
func creditOffline(ctx context.Context, tx pg.Tx, p billing.PaidCredit) (*adminapi.ExchangeRate, error) {
	var rate *billing.BillingExchangeRate
	if p.Payment.Currency != p.SiteCurrency {
		rates, err := billing.ExchangeRateHistory(ctx, tx, p.Payment.Currency.String(), p.SiteCurrency.String(), 0)
		if err != nil {
			return nil, err
		}
		if len(rates) == 0 {
			return nil, unprocessableFields("/payment/amount/currency", "no exchange rate to "+p.SiteCurrency.String()+" is kept for it")
		}
		rate, p.RateID = &rates[0], rates[0].ID
	}
	credited, err := billing.CreditsFor(p.Payment, p.SiteCurrency, rate)
	if invalid := new(billing.PaidError); errors.As(err, &invalid) {
		return nil, unprocessableFields("/payment/amount", err.Error())
	} else if err != nil {
		return nil, err
	}
	if credited <= 0 || credited > maxGrant {
		return nil, unprocessable("a grant is more than 0 and at most 10,000 units of the site's currency")
	}
	p.Credited = credited
	if err := billing.CreditPaid(ctx, tx, p); err != nil {
		return nil, err
	}
	if rate == nil {
		return nil, nil //nolint:nilnil // a payment in the site's currency is converted at no rate
	}
	body := exchangeBody(*rate)
	return &body, nil
}

// granted is what a grant answers: made, with the batch as it was written and the balance after it.
func (a adminAPI) granted(ctx context.Context, tx pg.Tx, account accounts.AccountID, id uuid.UUID, made adminapi.CreditGrant,
	currency platform.Currency,
) (adminapi.CreditGrant, error) {
	batch, _, err := billing.ReadCredit(ctx, tx, id)
	if err != nil {
		return adminapi.CreditGrant{}, err
	}
	balance, err := a.readBalance(ctx, tx, account)
	if err != nil {
		return adminapi.CreditGrant{}, err
	}
	made.Amount, made.ExpiresAt, made.CreatedAt = adminMoney(batch.Amount, currency), batch.ExpiresAt, batch.CreatedAt
	made.BalanceAfter = balanceBody(balance, currency)
	return made, nil
}
