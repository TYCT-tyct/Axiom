package main

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
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

// textOf is a query parameter's text, empty when the request did not send it: an absent filter and an empty one both
// mean every key.
func textOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// snapshot is the catalog the node has read, or nil when it has none: the retired-model hint is optional and the page
// shows nothing without it.
func (c consoleAPI) snapshot() *catalog.Snapshot {
	if c.catalog == nil {
		return nil
	}
	snap, err := c.catalog.Current()
	if err != nil {
		return nil
	}
	return snap
}

// retiredModels are the names among a key's restrictions that the catalog has retired: calls to them cannot succeed,
// so the page warns that the key is narrower than its owner may think.
func retiredModels(snap *catalog.Snapshot, models []string) []string {
	if snap == nil {
		return nil
	}
	var retired []string
	for _, name := range models {
		if _, err := snap.ResolvePublic(name); errors.Is(err, catalog.ErrRetired) {
			retired = append(retired, name)
		}
	}
	return retired
}

// keyBody is a key as the contract gives it. limit_remaining is what is left of the limit in its window, not below
// zero: a key that a late charge pushed past its limit has nothing left. retired_models is the hint that some of the
// key's model restrictions point at models the catalog has retired; it is absent when the node has no snapshot.
// presets are the routing presets the keys are bound to (keyPresets).
func keyBody(k accounts.Key, currency platform.Currency, used map[uuid.UUID]int64, presets map[uuid.UUID]routing.StoredPreset, snap *catalog.Snapshot) consoleapi.Key {
	kind := consoleapi.KeyKind(k.Kind)
	body := consoleapi.Key{
		Kind: &kind,
		Id:   k.ID.TypeID(), Name: k.Name, Prefix: k.Prefix, Last4: k.Last4, WorkspaceId: k.Workspace.TypeID(),
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
		if retired := retiredModels(snap, k.Models); len(retired) > 0 {
			body.RetiredModels = &retired
		}
	}
	if len(k.IPAllow) > 0 {
		ips := make([]string, len(k.IPAllow))
		for i, p := range k.IPAllow {
			ips[i] = p.String()
		}
		body.IpAllow = &ips
	}
	keyPresetBody(&body, k, presets)
	return body
}

// keyFilterEcho is the filters a page was read with, so a page can tell a node that applied them from an old node
// that ignored the parameters and returned every key.
func keyFilterEcho(q accounts.KeyQuery) *consoleapi.KeyFilterEcho {
	echo := &consoleapi.KeyFilterEcho{CreatedAfter: q.CreatedAfter, CreatedBefore: q.CreatedBefore}
	if q.Name != "" {
		echo.Name = &q.Name
	}
	if q.Prefix != "" {
		echo.Prefix = &q.Prefix
	}
	if q.Status != "" {
		status := consoleapi.KeyStatus(q.Status)
		echo.Status = &status
	}
	if q.Workspace != nil {
		ws := q.Workspace.TypeID()
		echo.WorkspaceId = &ws
	}
	return echo
}

// keyQueryOf is one page's query from a request's parameters, or the 400 for a value the contract rejects.
func keyQueryOf(p consoleapi.ListKeysParams) (accounts.KeyQuery, error) {
	q := accounts.KeyQuery{
		Limit: pageSize(p.Limit),
		Name:  textOf(p.Name), Prefix: textOf(p.Prefix),
		CreatedAfter: p.CreatedAfter, CreatedBefore: p.CreatedBefore,
	}
	if q.Limit < 1 || q.Limit > 200 {
		return accounts.KeyQuery{}, invalidField("limit", "must be between 1 and 200")
	}
	if p.Status != nil {
		if !p.Status.Valid() {
			return accounts.KeyQuery{}, invalidField("status", "must be active, disabled or expired")
		}
		q.Status = string(*p.Status)
	}
	if p.Cursor != nil {
		after, err := accounts.ParseKeyID(*p.Cursor)
		if err != nil {
			return accounts.KeyQuery{}, invalidField("cursor", "not a cursor this API gave out")
		}
		q.After = after
	}
	if p.WorkspaceId != nil {
		ws, err := accounts.ParseWorkspaceID(*p.WorkspaceId)
		if err != nil {
			return accounts.KeyQuery{}, invalidField("workspace_id", "not a workspace ID")
		}
		q.Workspace = &ws
	}
	if q.CreatedAfter != nil && q.CreatedBefore != nil && !q.CreatedAfter.Before(*q.CreatedBefore) {
		return accounts.KeyQuery{}, invalidField("created_after", "must be before created_before")
	}
	return q, nil
}

// ListKeys lists the account's keys, newest first, a page at a time, narrowed by the optional filters.
func (c consoleAPI) ListKeys(ctx context.Context, request consoleapi.ListKeysRequestObject) (consoleapi.ListKeysResponseObject, error) {
	cl := callOf(ctx)
	q, err := keyQueryOf(request.Params)
	if err != nil {
		return nil, err
	}
	var keys []accounts.Key
	var used map[uuid.UUID]int64
	var presets map[uuid.UUID]routing.StoredPreset
	var more bool
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		if keys, more, err = c.accounts.ListKeys(ctx, tx, cl.principal.AccountID(), q); err != nil {
			return err
		}
		if used, err = keysUsed(ctx, tx, keys); err != nil {
			return err
		}
		presets, err = keyPresets(ctx, tx, keys)
		return err
	})
	if err != nil {
		return nil, err
	}
	snap := c.snapshot()
	list := consoleapi.KeyList{Filters: keyFilterEcho(q), Data: make([]consoleapi.Key, len(keys))}
	for i, k := range keys {
		list.Data[i] = keyBody(k, cl.site.Currency, used, presets, snap)
	}
	if more { // the next page starts after the last key of this one
		next := keys[len(keys)-1].ID.TypeID()
		list.NextCursor = &next
	}
	return consoleapi.ListKeys200JSONResponse(list), nil
}

// CreateKey makes a key in the workspace the request names, or the account's default one; its text is in this response
// only.
func (c consoleAPI) CreateKey(ctx context.Context, request consoleapi.CreateKeyRequestObject) (consoleapi.CreateKeyResponseObject, error) {
	cl := callOf(ctx)
	settings, expires, err := createInput(request.Body).settings(cl.site.Currency, time.Now())
	if err != nil {
		return nil, err
	}
	var workspace accounts.WorkspaceID // zero: the default workspace
	if request.Body.WorkspaceId != nil {
		if workspace, err = accounts.ParseWorkspaceID(*request.Body.WorkspaceId); err != nil {
			return nil, invalidField("/workspace_id", "not a workspace ID")
		}
	}
	kind := accounts.CallKey
	if request.Body.Kind != nil {
		kind = string(*request.Body.Kind)
	}
	if kind == accounts.ManagementKey {
		if expires == nil {
			return nil, invalidField("/expires_at", "management keys require an expiry")
		}
		if _, err := c.accounts.Authorize(ctx, cl.principal, accounts.ViewKeys, accounts.AccountTarget(cl.principal.AccountID())); err != nil {
			return nil, err
		}
	}
	var created accounts.CreatedKey
	var key accounts.Key
	var presets map[uuid.UUID]routing.StoredPreset
	err = c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		if created, err = c.accounts.CreateKey(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), accounts.KeyRequest{
			Kind: kind, Site: cl.site.ID, Account: cl.principal.AccountID(), Workspace: workspace, CreatedBy: cl.principal.UserID(), ExpiresAt: expires, KeySettings: settings,
		}); err != nil {
			return err
		}
		if key, err = c.accounts.GetKey(ctx, tx, created.ID); err != nil {
			return err
		}
		presets, err = keyPresets(ctx, tx, []accounts.Key{key})
		return err
	})
	if err != nil {
		return nil, err
	}
	// A new key has used nothing, so no query is needed for its remaining limit.
	return consoleapi.CreateKey201JSONResponse{Key: keyBody(key, cl.site.Currency, nil, presets, c.snapshot()), Secret: created.Text.Reveal()}, nil
}

// GetKey reads one key.
func (c consoleAPI) GetKey(ctx context.Context, request consoleapi.GetKeyRequestObject) (consoleapi.GetKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := keyID(request.KeyId)
	if err != nil {
		return nil, err
	}
	key, used, presets, err := c.readKey(ctx, cl, func(tx pg.Tx) (accounts.Key, error) { return c.accounts.GetKey(ctx, tx, id) }, true)
	if err != nil {
		return nil, err
	}
	return consoleapi.GetKey200JSONResponse(keyBody(key, cl.site.Currency, used, presets, c.snapshot())), nil
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
	key, used, presets, err := c.readKey(ctx, cl, func(tx pg.Tx) (accounts.Key, error) {
		return c.accounts.UpdateKey(ctx, tx, audit.User(uuid.UUID(cl.principal.UserID())), id, settings)
	}, false)
	if err != nil {
		return nil, err
	}
	return consoleapi.UpdateKey200JSONResponse(keyBody(key, cl.site.Currency, used, presets, c.snapshot())), nil
}

// readKey is the key get gives, in a read-only transaction or one that writes, with what its body shows besides: the
// spend left in its window and its routing preset.
func (c consoleAPI) readKey(ctx context.Context, cl call, get func(pg.Tx) (accounts.Key, error), readOnly bool) (key accounts.Key, used map[uuid.UUID]int64, presets map[uuid.UUID]routing.StoredPreset, err error) {
	run := c.db.Tx
	if readOnly {
		run = c.db.ReadTx
	}
	err = run(ctx, cl.scope, func(tx pg.Tx) (err error) {
		if key, err = get(tx); err != nil {
			return err
		}
		if used, err = keysUsed(ctx, tx, []accounts.Key{key}); err != nil {
			return err
		}
		presets, err = keyPresets(ctx, tx, []accounts.Key{key})
		return err
	})
	return key, used, presets, err
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
