package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
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

// RevokeOperationsCredential ends a credential for good.
func (a adminAPI) RevokeOperationsCredential(ctx context.Context, r adminapi.RevokeOperationsCredentialRequestObject) (adminapi.RevokeOperationsCredentialResponseObject, error) {
	id, err := agentIDOf(r.CredentialId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "revokeOperationsCredential", r.Params.IdempotencyKey, r, func(tx pg.Tx) (adminapi.OperationsCredentialRevoked, error) {
		return adminapi.OperationsCredentialRevoked{CredentialId: id.TypeID()}, a.accounts.RevokeAgent(ctx, tx, actor(ctx), id, r.Body.Reason)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.RevokeOperationsCredential200JSONResponse{Body: body, Headers: adminapi.RevokeOperationsCredential200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// StopOperationsAgent is the emergency stop: every live credential revoked at once.
func (a adminAPI) StopOperationsAgent(ctx context.Context, r adminapi.StopOperationsAgentRequestObject) (adminapi.StopOperationsAgentResponseObject, error) {
	body, replayed, err := idempotently(ctx, a.db, "stopOperationsAgent", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.OperationsAgentStopped, error) {
		n, err := a.accounts.StopAgents(ctx, tx, actor(ctx), r.Body.Reason)
		return adminapi.OperationsAgentStopped{Revoked: n}, err
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
