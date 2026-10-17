package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// defaultPage is how many items a page holds when the request does not say (the contract's default).
const defaultPage = 50

// pageSize is the page size a request asks for.
func pageSize(limit *consoleapi.Limit) int {
	if limit == nil {
		return defaultPage
	}
	return *limit
}

// keyID reads a key's ID from a path. A malformed ID and an unknown one are the same 404.
func keyID(s string) (accounts.KeyID, error) {
	id, err := accounts.ParseKeyID(s)
	if err != nil {
		return accounts.KeyID{}, accounts.ErrKeyNotFound
	}
	return id, nil
}

// keysUsed is what each of keys that has a spend limit has used in the window of its reset: the figure admission
// subtracts from the limit.
func keysUsed(ctx context.Context, tx pg.Tx, keys []accounts.Key) (map[uuid.UUID]int64, error) {
	var windows []billing.KeyWindow
	for _, k := range keys {
		if k.SpendLimit != nil {
			windows = append(windows, billing.KeyWindow{Key: uuid.UUID(k.ID), Reset: billing.Reset(k.LimitReset)})
		}
	}
	if len(windows) == 0 {
		return nil, nil //nolint:nilnil // nothing has a limit to measure against
	}
	return billing.KeysUsed(ctx, tx, windows)
}

// keyBody is a key as the contract gives it. limit_remaining is what is left of the limit in its window, not below
// zero: a key that a late charge pushed past its limit has nothing left.
func keyBody(k accounts.Key, currency platform.Currency, used map[uuid.UUID]int64) consoleapi.Key {
	body := consoleapi.Key{
		Id: k.ID.TypeID(), Name: k.Name, Prefix: k.Prefix, Last4: k.Last4, WorkspaceId: k.Workspace.TypeID(),
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, Disabled: k.Disabled,
	}
	if k.SpendLimit != nil {
		limit, left := moneyOf(*k.SpendLimit, currency), moneyOf(max(0, *k.SpendLimit-used[uuid.UUID(k.ID)]), currency)
		body.SpendLimit, body.LimitRemaining = &limit, &left
		if k.LimitReset != "" {
			reset := consoleapi.LimitReset(k.LimitReset)
			body.LimitReset = &reset
		}
	}
	if len(k.Models) > 0 {
		body.Models = &k.Models
	}
	if len(k.IPAllow) > 0 {
		ips := make([]string, len(k.IPAllow))
		for i, p := range k.IPAllow {
			ips[i] = p.String()
		}
		body.IpAllow = &ips
	}
	return body
}

// ListKeys lists the account's keys, newest first, a page at a time.
func (c consoleAPI) ListKeys(ctx context.Context, request consoleapi.ListKeysRequestObject) (consoleapi.ListKeysResponseObject, error) {
	cl := callOf(ctx)
	var after accounts.KeyID
	if request.Params.Cursor != nil {
		var err error
		if after, err = accounts.ParseKeyID(*request.Params.Cursor); err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
	}
	var keys []accounts.Key
	var used map[uuid.UUID]int64
	var more bool
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		if keys, more, err = c.accounts.ListKeys(ctx, tx, cl.principal.AccountID(), after, pageSize(request.Params.Limit)); err != nil {
			return err
		}
		used, err = keysUsed(ctx, tx, keys)
		return err
	})
	if err != nil {
		return nil, err
	}
	list := consoleapi.KeyList{Data: make([]consoleapi.Key, len(keys))}
	for i, k := range keys {
		list.Data[i] = keyBody(k, cl.site.Currency, used)
	}
	if more { // the next page starts after the last key of this one
		next := keys[len(keys)-1].ID.TypeID()
		list.NextCursor = &next
	}
	return consoleapi.ListKeys200JSONResponse(list), nil
}

// CreateKey makes a key in the account's default workspace; its text is in this response only.
func (c consoleAPI) CreateKey(ctx context.Context, request consoleapi.CreateKeyRequestObject) (consoleapi.CreateKeyResponseObject, error) {
	cl := callOf(ctx)
	settings, expires, err := createInput(request.Body).settings(cl.site.Currency, time.Now())
	if err != nil {
		return nil, err
	}
	var created accounts.CreatedKey
	var key accounts.Key
	err = c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		if created, err = c.accounts.CreateKey(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), accounts.KeyRequest{
			Site: cl.site.ID, Account: cl.principal.AccountID(), CreatedBy: cl.principal.UserID(), ExpiresAt: expires, KeySettings: settings,
		}); err != nil {
			return err
		}
		key, err = c.accounts.GetKey(ctx, tx, created.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	// A new key has used nothing, so no query is needed for its remaining limit.
	return consoleapi.CreateKey201JSONResponse{Key: keyBody(key, cl.site.Currency, nil), Secret: created.Text.Reveal()}, nil
}

// GetKey reads one key.
func (c consoleAPI) GetKey(ctx context.Context, request consoleapi.GetKeyRequestObject) (consoleapi.GetKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := keyID(request.KeyId)
	if err != nil {
		return nil, err
	}
	var key accounts.Key
	var used map[uuid.UUID]int64
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		if key, err = c.accounts.GetKey(ctx, tx, id); err != nil {
			return err
		}
		used, err = keysUsed(ctx, tx, []accounts.Key{key})
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.GetKey200JSONResponse(keyBody(key, cl.site.Currency, used)), nil
}

// UpdateKey replaces the settings of a key.
func (c consoleAPI) UpdateKey(ctx context.Context, request consoleapi.UpdateKeyRequestObject) (consoleapi.UpdateKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := keyID(request.KeyId)
	if err != nil {
		return nil, err
	}
	settings, _, err := updateInput(request.Body).settings(cl.site.Currency, time.Now())
	if err != nil {
		return nil, err
	}
	var key accounts.Key
	var used map[uuid.UUID]int64
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		if key, err = c.accounts.UpdateKey(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), id, settings); err != nil {
			return err
		}
		used, err = keysUsed(ctx, tx, []accounts.Key{key})
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.UpdateKey200JSONResponse(keyBody(key, cl.site.Currency, used)), nil
}

// DeleteKey deletes a key for good.
func (c consoleAPI) DeleteKey(ctx context.Context, request consoleapi.DeleteKeyRequestObject) (consoleapi.DeleteKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := keyID(request.KeyId)
	if err != nil {
		return nil, err
	}
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		return c.accounts.DeleteKey(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), id)
	}); err != nil {
		return nil, err
	}
	return consoleapi.DeleteKey204Response{}, nil
}
