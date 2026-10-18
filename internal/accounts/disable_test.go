package accounts_test

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// disableKey turns the key off the way the console does, with the settings it has otherwise.
func disableKey(ctx context.Context, tx pg.Tx, s *accounts.Service, id accounts.KeyID) error {
	k, err := s.GetKey(ctx, tx, id)
	if err != nil {
		return err
	}
	_, err = s.UpdateKey(ctx, tx, system, id, accounts.KeySettings{
		Name: k.Name, Disabled: true, SpendLimit: k.SpendLimit, LimitReset: k.LimitReset, Models: k.Models, IPAllow: k.IPAllow,
	})
	return err
}

// disabler is disableKey as a change of a key, like Service.DeleteKey.
func disabler(s *accounts.Service) func(context.Context, pg.Tx, audit.Actor, accounts.KeyID) error {
	return func(ctx context.Context, tx pg.Tx, _ audit.Actor, id accounts.KeyID) error {
		return disableKey(ctx, tx, s, id)
	}
}
