package main

import (
	"context"
	"net/url"
	"uuid"

	"axiom/internal/platform/consoleapi"
)

func (c consoleAPI) GetInvitations(ctx context.Context, r consoleapi.GetInvitationsRequestObject) (consoleapi.GetInvitationsResponseObject, error) {
	cl := callOf(ctx)
	var after *uuid.UUID
	if r.Params.Cursor != nil {
		id, err := cursorID(r.Params.Cursor)
		if err != nil {
			return nil, err
		}
		after = &id
	}
	code, rows, next, err := c.accounts.Invitations(ctx, cl.principal, after, pageSize(r.Params.Limit))
	if err != nil {
		return nil, err
	}
	origin, err := c.accounts.Origin(cl.r.Host)
	if err != nil {
		return nil, err
	}
	link := origin + "/console/register?invite=" + url.QueryEscape(code)
	invitees := make([]consoleapi.Invitee, len(rows))
	for i, row := range rows {
		invitees[i] = consoleapi.Invitee{RegisteredAt: &row.RegisteredAt, Available: &row.Available}
	}
	response := consoleapi.Invitations{Code: &code, Link: &link, Invitees: &invitees}
	if next != nil {
		response.NextCursor = new(next.String())
	}
	return consoleapi.GetInvitations200JSONResponse(response), nil
}
