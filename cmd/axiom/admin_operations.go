package main

import (
	"context"
	"errors"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The operations agent's control plane (docs/domains/operations.md「控制面」): only the owner's admin session, with
// operations.manage and two-step verification, makes, rotates and revokes the agent's credentials and stops it. A token
// is in the first answer only: the idempotency record keeps the answer without it, so a replay never shows it again.

// ListOperationsCredentials lists every credential, newest first.
func (a adminAPI) ListOperationsCredentials(ctx context.Context, r adminapi.ListOperationsCredentialsRequestObject) (adminapi.ListOperationsCredentialsResponseObject, error) {
	var after accounts.AgentID
	if r.Params.Cursor != nil {
		id, err := accounts.ParseAgentID(*r.Params.Cursor)
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
		after = id
	}
	var page []accounts.AgentCredential
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		page, more, err = a.accounts.AgentCredentials(ctx, tx, after, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.OperationsCredentialList{Data: make([]adminapi.OperationsCredential, len(page))}
	for i, c := range page {
		list.Data[i] = credentialOf(c)
	}
	if more {
		list.NextCursor = new(page[len(page)-1].ID.TypeID())
	}
	return adminapi.ListOperationsCredentials200JSONResponse(list), nil
}

func credentialOf(c accounts.AgentCredential) adminapi.OperationsCredential {
	body := adminapi.OperationsCredential{
		Id: c.ID.TypeID(), Name: c.Name, Permissions: c.Permissions, Platform: c.Platform, ProposerVendors: c.Vendors,
		CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, LastUsedAt: c.LastUsedAt,
		TokenLast4: c.Last4, PreviousTokenUntil: c.PreviousUntil,
	}
	if c.Sites != nil {
		body.Sites = new(typeIDs(c.Sites))
	}
	return body
}

// typeIDs writes sites as the API names them.
func typeIDs(sites []accounts.SiteID) []string {
	ids := make([]string, len(sites))
	for i, s := range sites {
		ids[i] = s.TypeID()
	}
	return ids
}

// CreateOperationsCredential makes a credential and shows its token once.
func (a adminAPI) CreateOperationsCredential(ctx context.Context, r adminapi.CreateOperationsCredentialRequestObject) (adminapi.CreateOperationsCredentialResponseObject, error) {
	spec := accounts.AgentSpec{
		Name: r.Body.Name, Permissions: r.Body.Permissions, Platform: r.Body.Platform, Vendors: r.Body.ProposerVendors,
		ExpiresAt: r.Body.ExpiresAt, CreatedBy: callOf(ctx).principal.UserID(), Reason: r.Body.Reason,
	}
	if r.Body.Sites != nil {
		for i, s := range *r.Body.Sites {
			site, err := accounts.ParseSiteID(s)
			if err != nil {
				return nil, invalidField("/sites/"+strconv.Itoa(i), "not a site ID")
			}
			spec.Sites = append(spec.Sites, site)
		}
	}
	var token string
	body, replayed, err := idempotently(ctx, a.db, "createOperationsCredential", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.OperationsCredentialToken, error) {
		made, err := a.accounts.CreateAgent(ctx, tx, actor(ctx), spec)
		if err != nil {
			return adminapi.OperationsCredentialToken{}, agentSpecFailure(err)
		}
		token = made.Token.Reveal()
		return adminapi.OperationsCredentialToken{CredentialId: made.ID.TypeID()}, nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		body.Token = &token
	}
	return adminapi.CreateOperationsCredential201JSONResponse{Body: body, Headers: adminapi.CreateOperationsCredential201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RotateOperationsCredential gives a live credential a new token, keeping the old one for the overlap.
func (a adminAPI) RotateOperationsCredential(ctx context.Context, r adminapi.RotateOperationsCredentialRequestObject) (adminapi.RotateOperationsCredentialResponseObject, error) {
	id, err := agentIDOf(r.CredentialId)
	if err != nil {
		return nil, err
	}
	overlap := accounts.AgentOverlap
	if r.Body.OverlapHours != nil {
		overlap = time.Duration(*r.Body.OverlapHours) * time.Hour
	}
	var token string
	body, replayed, err := idempotently(ctx, a.db, "rotateOperationsCredential", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsCredentialToken, error) {
		made, err := a.accounts.RotateAgent(ctx, tx, actor(ctx), id, overlap, r.Body.Reason)
		if err != nil {
			return adminapi.OperationsCredentialToken{}, agentSpecFailure(err)
		}
		token = made.Token.Reveal()
		return adminapi.OperationsCredentialToken{CredentialId: made.ID.TypeID()}, nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		body.Token = &token
	}
	return adminapi.RotateOperationsCredential200JSONResponse{Body: body, Headers: adminapi.RotateOperationsCredential200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RevokeOperationsCredential ends a credential for good, and cancels its proposals still waiting for the owner.
func (a adminAPI) RevokeOperationsCredential(ctx context.Context, r adminapi.RevokeOperationsCredentialRequestObject) (adminapi.RevokeOperationsCredentialResponseObject, error) {
	id, err := agentIDOf(r.CredentialId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "revokeOperationsCredential", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsCredentialRevoked, error) {
		revoked := adminapi.OperationsCredentialRevoked{CredentialId: id.TypeID()}
		if err := a.accounts.RevokeAgent(ctx, tx, actor(ctx), id, r.Body.Reason); err != nil {
			return revoked, err
		}
		_, err := operations.CancelLive(ctx, tx, actor(ctx), new(uuid.UUID(id)), r.Body.Reason)
		return revoked, err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.RevokeOperationsCredential200JSONResponse{Body: body, Headers: adminapi.RevokeOperationsCredential200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// StopOperationsAgent is the emergency stop: every live credential revoked at once, and every proposal still waiting for
// the owner canceled with them.
func (a adminAPI) StopOperationsAgent(ctx context.Context, r adminapi.StopOperationsAgentRequestObject) (adminapi.StopOperationsAgentResponseObject, error) {
	body, replayed, err := idempotently(ctx, a.db, "stopOperationsAgent", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.OperationsAgentStopped, error) {
		revoked, err := a.accounts.StopAgents(ctx, tx, actor(ctx), r.Body.Reason)
		if err != nil {
			return adminapi.OperationsAgentStopped{}, err
		}
		canceled, err := operations.CancelLive(ctx, tx, actor(ctx), nil, r.Body.Reason)
		return adminapi.OperationsAgentStopped{Revoked: revoked, Canceled: &canceled}, err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.StopOperationsAgent200JSONResponse{Body: body, Headers: adminapi.StopOperationsAgent200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// agentIDOf reads a credential's ID from a path. A malformed ID and an unknown one are the same 404.
func agentIDOf(s string) (accounts.AgentID, error) {
	id, err := accounts.ParseAgentID(s)
	if err != nil {
		return accounts.AgentID{}, accounts.ErrAgentNotFound
	}
	return id, nil
}

// agentSpecFailure is a wrongly described credential as the 400 that names the part of the request at fault.
func agentSpecFailure(err error) error {
	if spec, ok := errors.AsType[*accounts.AgentSpecError](err); ok {
		return invalidField(spec.Field, spec.Message)
	}
	return err
}

// ListOperationsApprovals is the owner's queue of what the agent asked to be approved, newest first.
func (a adminAPI) ListOperationsApprovals(ctx context.Context, r adminapi.ListOperationsApprovalsRequestObject) (adminapi.ListOperationsApprovalsResponseObject, error) {
	after, err := cursorID(r.Params.Cursor)
	if err != nil {
		return nil, err
	}
	var state operations.ApprovalState
	if r.Params.State != nil {
		state = operations.ApprovalState(*r.Params.State)
	}
	var page []operations.Operation
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		page, more, err = operations.Approvals(ctx, tx, uuid.UUID{}, state, after, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.OperationsApprovalList{Data: make([]adminapi.OperationsApproval, len(page))}
	for i, op := range page {
		list.Data[i] = approvalBody(op)
	}
	if more {
		list.NextCursor = new(page[len(page)-1].Approval.ID.String())
	}
	return adminapi.ListOperationsApprovals200JSONResponse(list), nil
}

// GetOperationsApproval reads one approval with its operation.
func (a adminAPI) GetOperationsApproval(ctx context.Context, r adminapi.GetOperationsApprovalRequestObject) (adminapi.GetOperationsApprovalResponseObject, error) {
	id, err := approvalIDOf(r.ApprovalId)
	if err != nil {
		return nil, err
	}
	var op operations.Operation
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		op, err = operations.ApprovalOperation(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetOperationsApproval200JSONResponse(approvalBody(op)), nil
}

// ApproveOperationsApproval approves what the agent asked for and runs it, in one transaction.
func (a adminAPI) ApproveOperationsApproval(ctx context.Context, r adminapi.ApproveOperationsApprovalRequestObject) (adminapi.ApproveOperationsApprovalResponseObject, error) {
	if id, err := approvalIDOf(r.ApprovalId); err == nil { // a malformed ID is answered by decide
		if err := a.prepareRefundRun(ctx, id); err != nil {
			return nil, err
		}
	}
	body, replayed, err := a.decide(ctx, "approveOperationsApproval", r.ApprovalId, r.Params.IdempotencyKey, r.Body.Reason, true)
	if err != nil {
		return nil, err
	}
	return adminapi.ApproveOperationsApproval200JSONResponse{Body: body, Headers: adminapi.ApproveOperationsApproval200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RejectOperationsApproval rejects what the agent asked for; nothing is changed.
func (a adminAPI) RejectOperationsApproval(ctx context.Context, r adminapi.RejectOperationsApprovalRequestObject) (adminapi.RejectOperationsApprovalResponseObject, error) {
	body, replayed, err := a.decide(ctx, "rejectOperationsApproval", r.ApprovalId, r.Params.IdempotencyKey, r.Body.Reason, false)
	if err != nil {
		return nil, err
	}
	return adminapi.RejectOperationsApproval200JSONResponse{Body: body, Headers: adminapi.RejectOperationsApproval200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// decide records the owner's decision on an approval under an Idempotency-Key; an approval runs its operation in the
// same transaction, and a change of the catalog is reloaded once it has committed.
func (a adminAPI) decide(ctx context.Context, operation, approval, key, reason string, approve bool) (adminapi.OperationsApproval, bool, error) {
	id, err := approvalIDOf(approval)
	if err != nil {
		return adminapi.OperationsApproval{}, false, err
	}
	succeeded := false
	body, replayed, err := idempotently(ctx, a.db, operation, key, []any{approval, reason}, func(tx pg.Tx) (adminapi.OperationsApproval, error) {
		if approve {
			if live, err := a.holdProposer(ctx, tx, id); err != nil || !live {
				return cancelOrphan(ctx, tx, id, err)
			}
		}
		op, err := operations.Decide(ctx, tx, actor(ctx), id, approve, reason)
		if err == nil && approve {
			op, err = a.runApproved(ctx, tx, op)
		}
		if err != nil {
			return adminapi.OperationsApproval{}, err
		}
		succeeded = op.State == operations.StateSucceeded
		return approvalBody(op), nil
	})
	a.refreshAfterCommit(ctx, err != nil || !succeeded)
	return body, replayed, err
}

// holdProposer holds the credential that proposed approval id until tx ends, before the operation is locked, in the
// order a proposal takes them; false is a credential no longer live. Revocation and the stop cancel a credential's
// proposals, but a node from before them only revokes, and an approved change must not run for a credential that is
// gone.
func (a adminAPI) holdProposer(ctx context.Context, tx pg.Tx, id uuid.UUID) (bool, error) {
	op, err := operations.ApprovalOperation(ctx, tx, id)
	if err != nil {
		return false, err
	}
	err = a.accounts.HoldAgent(ctx, tx, accounts.AgentID(op.Credential))
	if errors.Is(err, accounts.ErrUnknownAgent) {
		return false, nil
	}
	return err == nil, err
}

// cancelOrphan cancels approval id, whose proposer is no longer live, unless err says why it could not be read.
func cancelOrphan(ctx context.Context, tx pg.Tx, id uuid.UUID, err error) (adminapi.OperationsApproval, error) {
	if err != nil {
		return adminapi.OperationsApproval{}, err
	}
	op, err := operations.Cancel(ctx, tx, actor(ctx), id, "the credential that proposed it is no longer live")
	if err != nil {
		return adminapi.OperationsApproval{}, err
	}
	return approvalBody(op), nil
}

// approvalIDOf reads an approval's ID from a path. A malformed ID and an unknown one are the same 404.
func approvalIDOf(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, operations.ErrNoApproval
	}
	return id, nil
}

// approvalBody is an approval with its operation as the admin contract answers it.
func approvalBody(op operations.Operation) adminapi.OperationsApproval {
	ap := op.Approval
	body := adminapi.OperationsApproval{
		Id: ap.ID.String(), State: adminapi.OperationsApprovalState(ap.State), Tier: adminapi.OperationsApprovalTier("human"),
		RequestedAt: ap.Requested, ExpiresAt: ap.Expires, DecidedAt: ap.Decided,
		OperationId: op.ID.String(), CredentialId: accounts.AgentID(op.Credential).TypeID(), Tool: op.Tool,
		Target: adminapi.OperationTarget{Kind: op.Target.Kind, Id: op.Target.ID}, Risk: adminapi.OperationRisk(op.Risk),
		Params: jsonx.Raw(op.Params), Summary: jsonx.Raw(ap.Summary), AgentReason: op.Reason,
		OperationState: adminapi.OperationState(op.State), DecidedBy: optionalText(ap.DecidedBy),
		DecisionReason: optionalText(ap.DecisionReason), RunId: optionalText(op.Run), ErrorCode: optionalText(op.Error),
		Decidable: &ap.Decidable,
	}
	if ap.Reasons != nil { // nil: an older node asked for it without them
		reasons := make([]adminapi.OperationsApprovalReason, len(ap.Reasons))
		for i, r := range ap.Reasons {
			reasons[i].Code = adminapi.OperationsApprovalReasonCode(r)
		}
		body.Reasons = &reasons
	}
	return body
}

// ListOperationsCapPolicies lists every cap on the money the agent moves.
func (a adminAPI) ListOperationsCapPolicies(ctx context.Context, _ adminapi.ListOperationsCapPoliciesRequestObject) (adminapi.ListOperationsCapPoliciesResponseObject, error) {
	var list adminapi.OperationsCapPolicyList
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		policies, err := operations.CapPolicies(ctx, tx)
		list.Data = make([]adminapi.OperationsCapPolicy, len(policies))
		for i, p := range policies {
			list.Data[i] = capPolicyBody(p)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.ListOperationsCapPolicies200JSONResponse(list), nil
}

// SetOperationsCapPolicy makes or changes the caps of a scope and category from the revision the owner read.
func (a adminAPI) SetOperationsCapPolicy(ctx context.Context, r adminapi.SetOperationsCapPolicyRequestObject) (adminapi.SetOperationsCapPolicyResponseObject, error) {
	b := r.Body
	l := b.Limits
	site, err := capScopeOf(r.Scope)
	if err != nil {
		return nil, err
	}
	p := operations.CapPolicy{Site: site, Category: operations.Category(r.Category), DailyCount: l.DailyCountMax}
	currency, err := platform.ParseCurrency(b.Currency)
	if err != nil {
		return nil, invalidField("/currency", "not a currency code")
	}
	p.Currency = currency
	for _, f := range []struct {
		field string
		text  adminapi.OperationsCapAmount
		into  *int64
	}{
		{"single_max", l.SingleMax, &p.Single},
		{"daily_max", l.DailyMax, &p.Daily},
		{"weekly_max", l.WeeklyMax, &p.Weekly},
		{"object_daily_max", l.ObjectDailyMax, &p.ObjectDaily},
		{"hard_single_max", l.HardSingleMax, &p.HardSingle},
		{"hard_daily_max", l.HardDailyMax, &p.HardDaily},
		{"hard_weekly_max", l.HardWeeklyMax, &p.HardWeekly},
	} {
		if *f.into, err = strconv.ParseInt(f.text, 10, 64); err != nil {
			return nil, invalidField("/limits/"+f.field, "not an amount a cap can count")
		}
	}
	body, replayed, err := idempotently(ctx, a.db, "setOperationsCapPolicy", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsCapPolicy, error) {
		set, err := operations.SetCapPolicy(ctx, tx, actor(ctx), p, b.Rev, b.Reason)
		return capPolicyBody(set), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetOperationsCapPolicy200JSONResponse{Body: body, Headers: adminapi.SetOperationsCapPolicy200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RepairOperationsCapCounters recomputes the counters of a scope and category from its money actions and resumes it.
func (a adminAPI) RepairOperationsCapCounters(ctx context.Context, r adminapi.RepairOperationsCapCountersRequestObject) (adminapi.RepairOperationsCapCountersResponseObject, error) {
	site, err := capScopeOf(r.Scope)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "repairOperationsCapCounters", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsCapPolicy, error) {
		p, err := operations.RepairCaps(ctx, tx, actor(ctx), operations.Category(r.Category), site, r.Body.Reason)
		if errors.Is(err, operations.ErrNoCapPolicy) {
			return adminapi.OperationsCapPolicy{}, accounts.ErrNotFound
		}
		return capPolicyBody(p), err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.RepairOperationsCapCounters200JSONResponse{Body: body, Headers: adminapi.RepairOperationsCapCounters200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// capScopeOf is the site a cap policy's scope names, nil for the platform.
func capScopeOf(scope string) (site *uuid.UUID, err error) {
	if scope != "platform" {
		id, err := accounts.ParseSiteID(scope)
		if err != nil {
			return nil, invalidField("scope", "not platform or a site ID")
		}
		site = new(uuid.UUID(id))
	}
	return site, nil
}

func capPolicyBody(p operations.CapPolicy) adminapi.OperationsCapPolicy {
	scope := "platform"
	if p.Site != nil {
		scope = accounts.SiteID(*p.Site).TypeID()
	}
	text := func(n int64) string { return strconv.FormatInt(n, 10) }
	return adminapi.OperationsCapPolicy{
		Scope: scope, Category: adminapi.OperationsCapCategory(p.Category), Currency: p.Currency.String(), Rev: p.Rev,
		UpdatedAt: p.Updated, PausedAt: p.Paused, Limits: adminapi.OperationsCapLimits{
			SingleMax: text(p.Single), DailyMax: text(p.Daily), WeeklyMax: text(p.Weekly), ObjectDailyMax: text(p.ObjectDaily),
			DailyCountMax: p.DailyCount, HardSingleMax: text(p.HardSingle), HardDailyMax: text(p.HardDaily),
			HardWeeklyMax: text(p.HardWeekly),
		},
	}
}
