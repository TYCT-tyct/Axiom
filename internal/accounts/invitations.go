package accounts

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// Invitee is a registration attributed to the caller, without the invitee's identity.
type Invitee struct {
	RegisteredAt time.Time
	Available    bool
}

// Invitations reads only p's invitation relations. Disabled users remain as placeholders;
// UUIDv7 positions remain stable when an invitee changes their email or status.
func (s *Service) Invitations(ctx context.Context, p Principal, after *uuid.UUID, limit int) (code string, invitees []Invitee, next *uuid.UUID, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return "", nil, nil, ErrNoSession
	}
	limit = min(max(limit, 1), 200)
	invitees = make([]Invitee, 0)
	err = s.db.Tx(ctx, pg.SiteScope(uuid.UUID(p.site)), func(tx pg.Tx) error {
		var err error
		code, err = s.queries.InvitationCode(ctx, tx, InvitationCodeParams{
			SiteID: uuid.UUID(p.site), AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user),
		})
		if err != nil {
			return err
		}
		rows, err := s.queries.ListInvitees(ctx, tx, ListInviteesParams{
			SiteID: uuid.UUID(p.site), UserID: new(uuid.UUID(p.user)), After: after, PageSize: int32(limit + 1), // #nosec G115 -- limit is clamped to 1..200 above.
		})
		if err != nil {
			return err
		}
		if len(rows) > limit {
			next = new(rows[limit-1].ID)
			rows = rows[:limit]
		}
		for _, row := range rows {
			invitees = append(invitees, Invitee{RegisteredAt: row.RegisteredAt.Time, Available: row.Status == "active"})
		}
		return nil
	})
	return code, invitees, next, err
}
