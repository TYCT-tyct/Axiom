package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// ErrRefundSecondPerson is a subscription refund approval by the operator who requested it. The refund is a fund
// operation, so the same approval rule as a payment refund applies.
var ErrRefundSecondPerson = errors.New("billing: a different operator must approve the subscription refund")

// SubscriptionRefundProposalRequest asks for a subscription refund. The request changes no money or term; approval
// applies it.
type SubscriptionRefundProposalRequest struct {
	ID, Site, Account, Grant uuid.UUID
	SiteCurrency             platform.Currency
	Credits                  int64
	Reason                   string
	RequestedBy              uuid.UUID
	Actor                    audit.Actor
}

// SubscriptionRefundProposal is a stored request waiting for another operator.
type SubscriptionRefundProposal struct {
	ID           uuid.UUID
	Grant        uuid.UUID
	Subscription uuid.UUID
	Site         uuid.UUID
	Account      uuid.UUID
	Credits      int64
	Currency     platform.Currency
	RequestedBy  uuid.UUID
	ApprovedBy   *uuid.UUID
	State        string
	Reason       string
	CreatedAt    time.Time
	FinishedAt   *time.Time
}

// RequestSubscriptionRefund stores a pending refund after checking the grant and amount. It does not touch the
// subscription or the ledger; ApproveSubscriptionRefund does that after a different operator approves.
func RequestSubscriptionRefund(ctx context.Context, tx pg.Tx, req SubscriptionRefundProposalRequest) (SubscriptionRefundProposal, error) {
	if err := checkProposalRequest(req); err != nil {
		return SubscriptionRefundProposal{}, err
	}
	if old, ok, err := readRefundProposal(ctx, tx, req.ID); err != nil {
		return SubscriptionRefundProposal{}, err
	} else if ok {
		if old.Grant != req.Grant || old.Site != req.Site || old.Account != req.Account || old.Credits != req.Credits {
			return SubscriptionRefundProposal{}, fmt.Errorf("%w: refund request %s already names another grant or amount", ErrNotRefundable, req.ID)
		}
		return old, nil
	}
	g, _, _, _, remaining, err := readGrantValue(ctx, tx, SubscriptionRefundRequest{
		Grant: req.Grant, Site: req.Site, Account: req.Account, SiteCurrency: req.SiteCurrency, Credits: req.Credits,
	})
	if err != nil {
		return SubscriptionRefundProposal{}, err
	}
	if req.Credits > remaining {
		return SubscriptionRefundProposal{}, fmt.Errorf("%w: %d credits are more than the %d left", ErrNotRefundable, req.Credits, remaining)
	}
	if _, err := readRefundSubscription(ctx, tx, SubscriptionRefundRequest{
		Grant: req.Grant, Site: req.Site, Account: req.Account, SiteCurrency: req.SiteCurrency, Credits: req.Credits,
	}, g); err != nil {
		return SubscriptionRefundProposal{}, err
	}
	return storeRefundProposal(ctx, tx, req, g)
}

func checkProposalRequest(req SubscriptionRefundProposalRequest) error {
	if req.ID == (uuid.UUID{}) || req.Grant == (uuid.UUID{}) || req.Site == (uuid.UUID{}) || req.Account == (uuid.UUID{}) ||
		req.RequestedBy == (uuid.UUID{}) || req.SiteCurrency == (platform.Currency{}) || req.Credits <= 0 || req.Reason == "" {
		return &PlanError{"refund request names no ID, grant, site, account, requester, currency or credits"}
	}
	return nil
}

func storeRefundProposal(ctx context.Context, tx pg.Tx, req SubscriptionRefundProposalRequest, g ReadGrantRow) (SubscriptionRefundProposal, error) {
	inserted, err := queries.InsertSubscriptionRefundRequest(ctx, tx, InsertSubscriptionRefundRequestParams{
		ID: req.ID, GrantID: req.Grant, SiteID: req.Site, AccountID: req.Account,
		SubscriptionID: g.SubscriptionID, Credits: req.Credits, SiteCurrency: req.SiteCurrency.String(),
		RequestedBy: req.RequestedBy, Reason: req.Reason,
	})
	if err != nil {
		return SubscriptionRefundProposal{}, fmt.Errorf("billing: write the subscription refund request: %w", err)
	}
	out, ok, err := readRefundProposal(ctx, tx, req.ID)
	if err != nil {
		return SubscriptionRefundProposal{}, err
	}
	if !ok {
		return SubscriptionRefundProposal{}, fmt.Errorf("billing: read the subscription refund request: %w", pg.ErrNoRows)
	}
	if out.Grant != req.Grant || out.Site != req.Site || out.Account != req.Account || out.Credits != req.Credits {
		return SubscriptionRefundProposal{}, fmt.Errorf("%w: refund request %s already names another grant or amount", ErrNotRefundable, req.ID)
	}
	if inserted == 0 {
		return out, nil
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: req.Actor, Action: "subscription.refund_request", Reason: req.Reason,
		Target: audit.Target{Module: "billing", Kind: "subscription", ID: out.Subscription.String()},
		Detail: map[string]any{"refund": out.ID.String(), "grant": out.Grant.String(), "credits": out.Credits},
	}); err != nil {
		return SubscriptionRefundProposal{}, err
	}
	return out, nil
}

// ApproveSubscriptionRefund applies a pending request as a different operator. The refund row keeps the request ID, so
// approval replay returns the same stored refund and posts nothing again.
func ApproveSubscriptionRefund(ctx context.Context, tx pg.Tx, id, approver uuid.UUID, actor audit.Actor, reason string) (SubscriptionRefund, error) {
	if id == (uuid.UUID{}) || approver == (uuid.UUID{}) || reason == "" {
		return SubscriptionRefund{}, &PlanError{"refund approval names no refund, approver or reason"}
	}
	p, replay, err := readApprovable(ctx, tx, id, approver)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	if replay {
		return refundProposal(ctx, tx, p, actor, p.Reason)
	}
	if _, err := lockAccount(ctx, tx, p.Account); err != nil {
		return SubscriptionRefund{}, err
	}
	p, replay, err = readApprovable(ctx, tx, id, approver)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	if replay {
		return refundProposal(ctx, tx, p, actor, p.Reason)
	}
	return approveProposal(ctx, tx, p, approver, actor, reason)
}

// readApprovable reads a request and says whether it is already finished (replay) or still needs this operator's
// approval. It refuses a request by the same operator.
func readApprovable(ctx context.Context, tx pg.Tx, id, approver uuid.UUID) (SubscriptionRefundProposal, bool, error) {
	p, ok, err := readRefundProposal(ctx, tx, id)
	if err != nil {
		return SubscriptionRefundProposal{}, false, err
	}
	if !ok {
		return SubscriptionRefundProposal{}, false, fmt.Errorf("%w: refund request %s does not exist", ErrNotRefundable, id)
	}
	if p.ApprovedBy != nil || p.State != "pending" {
		return p, true, nil
	}
	if p.RequestedBy == approver {
		return SubscriptionRefundProposal{}, false, ErrRefundSecondPerson
	}
	return p, false, nil
}

func refundProposal(ctx context.Context, tx pg.Tx, p SubscriptionRefundProposal, actor audit.Actor, reason string) (SubscriptionRefund, error) {
	return applySubscriptionRefund(ctx, tx, SubscriptionRefundRequest{
		ID: p.ID, Site: p.Site, Account: p.Account, Grant: p.Grant, SiteCurrency: p.Currency,
		Credits: p.Credits, Reason: reason, Actor: actor,
	})
}

func approveProposal(ctx context.Context, tx pg.Tx, p SubscriptionRefundProposal, approver uuid.UUID, actor audit.Actor, reason string) (SubscriptionRefund, error) {
	out, err := refundProposal(ctx, tx, p, actor, reason)
	if err != nil {
		return SubscriptionRefund{}, err
	}
	if n, err := queries.ApproveSubscriptionRefundRequest(ctx, tx, ApproveSubscriptionRefundRequestParams{
		ID: p.ID, ApprovedBy: &approver,
	}); err != nil {
		return SubscriptionRefund{}, fmt.Errorf("billing: approve the subscription refund: %w", err)
	} else if n != 1 {
		return SubscriptionRefund{}, fmt.Errorf("billing: refund request %s changed under the lock", p.ID)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "subscription.refund_approve", Reason: reason,
		Target: audit.Target{Module: "billing", Kind: "subscription", ID: p.Subscription.String()},
		Detail: map[string]any{"refund": p.ID.String(), "grant": p.Grant.String(), "credits": p.Credits, "requested_by": p.RequestedBy.String()},
	}); err != nil {
		return SubscriptionRefund{}, err
	}
	return out, nil
}

func readRefundProposal(ctx context.Context, tx pg.Tx, id uuid.UUID) (SubscriptionRefundProposal, bool, error) {
	r, err := queries.ReadSubscriptionRefundRequest(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		return SubscriptionRefundProposal{}, false, nil
	}
	if err != nil {
		return SubscriptionRefundProposal{}, false, fmt.Errorf("billing: read the subscription refund request: %w", err)
	}
	currency, err := platform.ParseCurrency(r.SiteCurrency)
	if err != nil {
		return SubscriptionRefundProposal{}, false, err
	}
	return SubscriptionRefundProposal{
		ID: r.ID, Grant: r.GrantID, Subscription: r.SubscriptionID, Site: r.SiteID, Account: r.AccountID,
		Credits: r.Credits, Currency: currency, RequestedBy: r.RequestedBy, ApprovedBy: r.ApprovedBy,
		State: r.State, Reason: r.Reason, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}, true, nil
}

// ReadSubscriptionRefundProposal reads a pending or finished request for an operator's page.
func ReadSubscriptionRefundProposal(ctx context.Context, tx pg.Tx, id uuid.UUID) (SubscriptionRefundProposal, bool, error) {
	return readRefundProposal(ctx, tx, id)
}
