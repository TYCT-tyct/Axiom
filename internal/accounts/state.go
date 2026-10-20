package accounts

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Status is the state an account or a user is in, as the tables hold it: active, or disabled. The two are separate
// switches with separate effects (docs/domains/accounts.md「停用与注销」).
type Status string

const (
	// StatusActive is an account whose keys are admitted and a user who may sign in.
	StatusActive Status = "active"
	// StatusDisabled is a suspended account or a user whose login is disabled.
	StatusDisabled Status = "disabled"
)

// SetAccountStatus puts the account in to and records the change with the reason, in the transaction of the change. A
// disabled account's API keys are refused at admission from the next call on, because admission reads the account's
// state from the database on every call, and they are admitted again once it is active. Its console session is left
// alone, so a person can still sign in and look; the users of the account are not touched either, because stopping a
// login is SetUserStatus. A change to the state the account is already in writes nothing, records nothing and is not
// an error.
func (s *Service) SetAccountStatus(ctx context.Context, tx pg.Tx, actor audit.Actor, id AccountID, to Status, reason string) (AccountDetail, error) {
	rows, err := s.queries.LockAccountState(ctx, tx, uuid.UUID(id))
	if err != nil {
		return AccountDetail{}, fmt.Errorf("lock the account: %w", err)
	}
	if len(rows) == 0 {
		return AccountDetail{}, ErrNotFound
	}
	if from := Status(rows[0].Status); from != to {
		action := "account.suspend"
		if to == StatusActive {
			action = "account.resume"
		}
		if err := s.queries.SetAccountStatus(ctx, tx, SetAccountStatusParams{ID: uuid.UUID(id), Status: string(to)}); err != nil {
			return AccountDetail{}, fmt.Errorf("set the account's state: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Event{
			Actor: actor, Action: action, Reason: reason,
			Target: audit.Target{Module: "accounts", Kind: "account", ID: id.TypeID(), Site: rows[0].SiteID, Account: uuid.UUID(id)},
			Detail: map[string]any{"from": string(from), "to": string(to)},
		}); err != nil {
			return AccountDetail{}, err
		}
	}
	return s.Account(ctx, tx, id)
}

// SetUserStatus disables or re-enables the user's login, in the one transaction: disabling ends every session the user
// has, on both surfaces, so a page that is open stops working at its next request — ValidSession refuses it for the
// revocation and for the user's state both — while the account's API keys keep working, because stopping calls is the
// account's state. Enabling lets the user sign in again and brings no session back. The audit entry says how many
// sessions the disabling ended. A change to the state the user is already in ends no session, writes nothing and is
// not an error.
func (s *Service) SetUserStatus(ctx context.Context, tx pg.Tx, actor audit.Actor, id UserID, to Status, reason string) (UserSummary, error) {
	rows, err := s.queries.LockUserState(ctx, tx, uuid.UUID(id))
	if err != nil {
		return UserSummary{}, fmt.Errorf("lock the user: %w", err)
	}
	if len(rows) == 0 {
		return UserSummary{}, ErrNotFound
	}
	if from := Status(rows[0].Status); from != to {
		if err := s.queries.SetUserStatus(ctx, tx, SetUserStatusParams{ID: uuid.UUID(id), Status: string(to)}); err != nil {
			return UserSummary{}, fmt.Errorf("set the user's state: %w", err)
		}
		action := "user.enable"
		detail := map[string]any{"from": string(from), "to": string(to)}
		if to == StatusDisabled {
			action = "user.disable"
			ended, err := s.queries.RevokeUserSessions(ctx, tx, uuid.UUID(id))
			if err != nil {
				return UserSummary{}, fmt.Errorf("end the user's sessions: %w", err)
			}
			detail["sessions"] = ended
		}
		if err := audit.Record(ctx, tx, audit.Event{
			Actor: actor, Action: action, Reason: reason,
			Target: audit.Target{
				Module: "accounts", Kind: "user", ID: id.TypeID(), Site: rows[0].SiteID, Account: rows[0].AccountID,
			},
			Detail: detail,
		}); err != nil {
			return UserSummary{}, err
		}
	}
	return s.User(ctx, tx, id)
}
