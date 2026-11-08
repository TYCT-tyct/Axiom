package main

import (
	"context"
	"errors"
	"strings"
	"uuid"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"axiom/internal/accounts"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// The console's routing presets are the account layer's and its workspaces' (docs/domains/routing.md「接口、权限与
// 审计」): an account reads the presets its keys may use, writes its own and its workspaces', and sets its own default
// and theirs. The tables' policies keep it to its tenant; these handlers only name the layer, and check that a
// workspace they name is the account's, which routing's tables cannot.

// accountLayer is the layer of the session's account.
func accountLayer(cl call) routing.Layer {
	return routing.Layer{Scope: routing.ScopeAccount, Site: routing.SiteID(cl.site.ID), Account: routing.AccountID(cl.principal.AccountID())}
}

// workspaceLayer is the layer of the session's account's workspace id, or of its default workspace, where the console
// makes keys, when id is nil. A malformed ID, another account's workspace and a deleted one are the same 404.
func (c consoleAPI) workspaceLayer(ctx context.Context, tx pg.Tx, cl call, id *string) (routing.Layer, error) {
	var named *accounts.WorkspaceID
	if id != nil {
		parsed, err := accounts.ParseWorkspaceID(*id)
		if err != nil {
			return routing.Layer{}, accounts.ErrNotFound
		}
		named = &parsed
	}
	ws, err := c.accounts.Workspace(ctx, tx, cl.principal.AccountID(), named)
	if err != nil {
		return routing.Layer{}, err
	}
	layer := accountLayer(cl)
	layer.Scope, layer.Workspace = routing.ScopeWorkspace, routing.WorkspaceID(ws)
	return layer, nil
}

func (c consoleAPI) ListRoutingPresets(ctx context.Context, r consoleapi.ListRoutingPresetsRequestObject) (consoleapi.ListRoutingPresetsResponseObject, error) {
	cl := callOf(ctx)
	account := cl.principal.AccountID()
	var out consoleapi.RoutingPresetList
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		layer, err := c.workspaceLayer(ctx, tx, cl, r.Params.WorkspaceId)
		if err != nil {
			return err
		}
		presets, err := routing.ListPresets(ctx, tx, routing.Admission{Site: layer.Site, Account: layer.Account, Workspace: layer.Workspace, Currency: cl.site.Currency})
		if err != nil {
			return err
		}
		out.Data, err = countedPresets(ctx, tx, c.accounts, &account, presets)
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.ListRoutingPresets200JSONResponse(out), nil
}

func (c consoleAPI) GetRoutingPreset(ctx context.Context, r consoleapi.GetRoutingPresetRequestObject) (consoleapi.GetRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	account := cl.principal.AccountID()
	var body []consoleapi.RoutingPreset
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		p, err := routing.GetPreset(ctx, tx, routing.PresetID(r.PresetId))
		if err != nil {
			return err
		}
		body, err = countedPresets(ctx, tx, c.accounts, &account, []routing.StoredPreset{p})
		return err
	}); err != nil {
		return nil, err
	}
	return consoleapi.GetRoutingPreset200JSONResponse(body[0]), nil
}

func (c consoleAPI) CreateRoutingPreset(ctx context.Context, r consoleapi.CreateRoutingPresetRequestObject) (consoleapi.CreateRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	var p routing.StoredPreset
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		layer := accountLayer(cl)
		if r.Body.WorkspaceId != nil {
			if layer, err = c.workspaceLayer(ctx, tx, cl, r.Body.WorkspaceId); err != nil {
				return err
			}
		}
		p, err = routing.CreatePreset(ctx, tx, actor(ctx), layer, r.Body.Slug,
			routing.PresetContent{Name: r.Body.Name, Rules: r.Body.Rules}, "")
		return rulesProblem(err)
	}); err != nil {
		return nil, err
	}
	return consoleapi.CreateRoutingPreset201JSONResponse(routingPresetBody(p)), nil
}

func (c consoleAPI) UpdateRoutingPreset(ctx context.Context, r consoleapi.UpdateRoutingPresetRequestObject) (consoleapi.UpdateRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	var p routing.StoredPreset
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		p, err = routing.UpdatePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Body.Revision,
			routing.PresetContent{Name: r.Body.Name, Rules: r.Body.Rules}, "")
		return rulesProblem(err)
	}); err != nil {
		return nil, err
	}
	return consoleapi.UpdateRoutingPreset200JSONResponse(routingPresetBody(p)), nil
}

func (c consoleAPI) DisableRoutingPreset(ctx context.Context, r consoleapi.DisableRoutingPresetRequestObject) (consoleapi.DisableRoutingPresetResponseObject, error) {
	cl := callOf(ctx)
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		return routing.DisablePreset(ctx, tx, actor(ctx), routing.PresetID(r.PresetId), r.Params.Revision, "")
	}); err != nil {
		return nil, err
	}
	return consoleapi.DisableRoutingPreset204Response{}, nil
}

// CheckRoutingRules answers for the keys of the session's account, at its tier: an editor checks a preset's rules
// against the endpoints the keys bound to it would be routed among.
func (c consoleAPI) CheckRoutingRules(ctx context.Context, r consoleapi.CheckRoutingRulesRequestObject) (consoleapi.CheckRoutingRulesResponseObject, error) {
	cl := callOf(ctx)
	tier, err := c.accounts.AccountTier(ctx, cl.principal)
	if err != nil {
		return nil, err
	}
	layer := accountLayer(cl)
	a := routing.Admission{Tier: tier, Account: layer.Account, Site: layer.Site, Currency: cl.site.Currency}
	body, err := checkRules(c.router, routing.Preset{Scope: routing.ScopeAccount}, r.Body.Rules, a)
	return consoleapi.CheckRoutingRules200JSONResponse(body), err
}

func (c consoleAPI) GetRoutingDefault(ctx context.Context, _ consoleapi.GetRoutingDefaultRequestObject) (consoleapi.GetRoutingDefaultResponseObject, error) {
	body, err := c.readDefault(ctx, nil)
	return consoleapi.GetRoutingDefault200JSONResponse(body), err
}

func (c consoleAPI) SetRoutingDefault(ctx context.Context, r consoleapi.SetRoutingDefaultRequestObject) (consoleapi.SetRoutingDefaultResponseObject, error) {
	body, err := c.setDefault(ctx, nil, *r.Body)
	return consoleapi.SetRoutingDefault200JSONResponse(body), err
}

func (c consoleAPI) GetWorkspaceRoutingDefault(ctx context.Context, r consoleapi.GetWorkspaceRoutingDefaultRequestObject) (consoleapi.GetWorkspaceRoutingDefaultResponseObject, error) {
	body, err := c.readDefault(ctx, &r.WorkspaceId)
	return consoleapi.GetWorkspaceRoutingDefault200JSONResponse(body), err
}

func (c consoleAPI) SetWorkspaceRoutingDefault(ctx context.Context, r consoleapi.SetWorkspaceRoutingDefaultRequestObject) (consoleapi.SetWorkspaceRoutingDefaultResponseObject, error) {
	body, err := c.setDefault(ctx, &r.WorkspaceId, *r.Body)
	return consoleapi.SetWorkspaceRoutingDefault200JSONResponse(body), err
}

// readDefault is the workspace's own default, or the account's when workspace is nil, with what a key made now in the
// workspace takes: in the account's default workspace, where the console makes keys, for the account's default.
func (c consoleAPI) readDefault(ctx context.Context, workspace *string) (consoleapi.AccountRoutingDefault, error) {
	cl := callOf(ctx)
	var body consoleapi.AccountRoutingDefault
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		ws, layer, err := c.defaultLayers(ctx, tx, cl, workspace)
		if err != nil {
			return err
		}
		d, err := routing.ReadDefault(ctx, tx, layer)
		if err != nil {
			return err
		}
		body, err = newKeysDefault(ctx, tx, cl, ws, d)
		return err
	})
	return body, err
}

// setDefault is readDefault after the layer's default is set as u says.
func (c consoleAPI) setDefault(ctx context.Context, workspace *string, u consoleapi.RoutingDefaultUpdate) (consoleapi.AccountRoutingDefault, error) {
	cl := callOf(ctx)
	var body consoleapi.AccountRoutingDefault
	err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		ws, layer, err := c.defaultLayers(ctx, tx, cl, workspace)
		if err != nil {
			return err
		}
		d, err := routing.SetDefault(ctx, tx, actor(ctx), layer, cl.site.Currency, (*routing.PresetID)(u.PresetId), u.Version, "")
		if err != nil {
			return err
		}
		body, err = newKeysDefault(ctx, tx, cl, ws, d)
		return err
	})
	return body, err
}

// defaultLayers are the workspace whose new keys a default is read for, and the layer whose default it is: the
// workspace named, or the account of its default one.
func (c consoleAPI) defaultLayers(ctx context.Context, tx pg.Tx, cl call, workspace *string) (ws, layer routing.Layer, err error) {
	if ws, err = c.workspaceLayer(ctx, tx, cl, workspace); err != nil || workspace != nil {
		return ws, ws, err
	}
	return ws, accountLayer(cl), nil
}

// ListRoutingSourceClasses answers from the classes the node routes by, so a page offers exactly the slugs a rule can
// name here; the table is the platform's and its fingerprints are not for tenants to read.
func (c consoleAPI) ListRoutingSourceClasses(context.Context, consoleapi.ListRoutingSourceClassesRequestObject) (consoleapi.ListRoutingSourceClassesResponseObject, error) {
	names := c.classes.SourceClassNames()
	out := consoleapi.SourceClassNameList{Data: make([]consoleapi.SourceClassName, len(names))}
	for i, n := range names {
		out.Data[i] = consoleapi.SourceClassName{Slug: n.Slug, Name: n.Name, NameZh: n.NameZh}
	}
	return consoleapi.ListRoutingSourceClasses200JSONResponse(out), nil
}

// rulesProblem is a refusal of a preset's rules as the 400 that names them, with what is wrong; every other error is
// itself.
func rulesProblem(err error) error {
	if !errors.Is(err, routing.ErrRules) {
		return err
	}
	return invalidField("/rules", strings.TrimPrefix(err.Error(), routing.ErrRules.Error()+": "))
}

// checkRules is the answer to a check of rules held as p's layer holds them, for the keys of admission a.
func checkRules(router *routing.Router, p routing.Preset, rules []byte, a routing.Admission) (consoleapi.RoutingRulesCheck, error) {
	summary, unmatched, err := router.CheckRules(p, rules, a)
	if err != nil {
		return consoleapi.RoutingRulesCheck{}, rulesProblem(err)
	}
	out := consoleapi.RoutingRulesCheck{Summary: rulesSummary(summary), Unmatched: make([]consoleapi.RoutingRuleUnmatched, len(unmatched))}
	for i, u := range unmatched {
		out.Unmatched[i].Fields = make([]consoleapi.RoutingRuleUnmatchedFields, len(u.Fields))
		for j, f := range u.Fields {
			out.Unmatched[i].Fields[j] = consoleapi.RoutingRuleUnmatchedFields(f)
		}
		if u.Rule >= 0 {
			out.Unmatched[i].Index = &u.Rule
		}
	}
	return out, nil
}

func rulesSummary(s routing.RulesSummary) consoleapi.RoutingRulesSummary {
	out := consoleapi.RoutingRulesSummary{Sort: consoleapi.RoutingSort(s.Sort), VerifiedOnly: s.VerifiedOnly, Priced: s.Priced, Overrides: s.Overrides}
	if s.Classes != nil {
		out.Classes = &s.Classes
	}
	return out
}

func routingPresetBody(p routing.StoredPreset) consoleapi.RoutingPreset {
	body := consoleapi.RoutingPreset{
		Id: openapi_types.UUID(p.ID), Scope: consoleapi.RoutingPresetScope(p.Scope.String()), Slug: p.Slug, Name: p.Name,
		Revision: p.Revision, Version: p.Version, Rules: p.Rules, Disabled: p.Disabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.Currency != (platform.Currency{}) {
		code := p.Currency.String()
		body.Currency = &code
	}
	if p.Scope == routing.ScopeWorkspace {
		ws := accounts.WorkspaceID(p.Workspace).TypeID()
		body.WorkspaceId = &ws
	}
	if s, ok := p.Summary(); ok {
		summary := rulesSummary(s)
		body.Summary = &summary
	}
	return body
}

// countedPresets are the presets' bodies with the keys bound to each, account's alone when it is not nil. The system
// preset is not counted: every unbound key follows it too, and nobody edits or disables it.
func countedPresets(ctx context.Context, tx pg.Tx, keys *accounts.Service, account *accounts.AccountID, presets []routing.StoredPreset) ([]consoleapi.RoutingPreset, error) {
	ids := make([]uuid.UUID, 0, len(presets))
	for _, p := range presets {
		if !p.System {
			ids = append(ids, uuid.UUID(p.ID))
		}
	}
	counts, err := keys.PresetKeys(ctx, tx, account, ids)
	if err != nil {
		return nil, err
	}
	out := make([]consoleapi.RoutingPreset, len(presets))
	for i, p := range presets {
		out[i] = routingPresetBody(p)
		if !p.System {
			n := counts[uuid.UUID(p.ID)]
			out[i].Keys = &n
		}
	}
	return out, nil
}

// newKeysDefault is a layer's default d with the preset a call key made now in the workspace ws is bound to, which the
// keys page sets each key's preset against: the chain from the workspace up, the one key creation resolves. A default
// the key could not use is said, not failed: it is making the key that it refuses.
func newKeysDefault(ctx context.Context, tx pg.Tx, cl call, ws routing.Layer, d routing.Default) (consoleapi.AccountRoutingDefault, error) {
	body := consoleapi.AccountRoutingDefault{PresetId: (*openapi_types.UUID)(d.Preset), Version: d.Version}
	id, layer, err := routing.DefaultPreset(ctx, tx, ws.Site, ws.Account, ws.Workspace, cl.site.Currency)
	from := consoleapi.RoutingPresetScope(layer.String())
	body.NewKeysLayer = &from
	switch {
	case errors.Is(err, routing.ErrDefaultInvalid):
		refused := true
		body.NewKeysRefused = &refused
	case err != nil:
		return body, err
	default:
		body.NewKeysPresetId = (*openapi_types.UUID)(&id)
	}
	return body, nil
}

// keyPresets are the presets the keys are bound to, by ID, as the account can read them: a preset it can no longer
// read, which no key can be bound to but data may still name, is left out, and its keys show the ID alone. A page of
// keys names a few presets at most, the ones its account may choose from.
func keyPresets(ctx context.Context, tx pg.Tx, keys []accounts.Key) (map[uuid.UUID]routing.StoredPreset, error) {
	ids := map[uuid.UUID]bool{}
	for _, k := range keys {
		if k.Preset != nil {
			ids[*k.Preset] = true
		}
	}
	out := make(map[uuid.UUID]routing.StoredPreset, len(ids))
	for id := range ids {
		p, err := routing.GetPreset(ctx, tx, routing.PresetID(id))
		if errors.Is(err, routing.ErrPresetNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}

// keyPresetBody sets the key's routing preset on its body: its ID, and what the account can read of it.
func keyPresetBody(body *consoleapi.Key, k accounts.Key, presets map[uuid.UUID]routing.StoredPreset) {
	if k.Preset == nil {
		return
	}
	id := openapi_types.UUID(*k.Preset)
	body.PresetId = &id
	if p, ok := presets[*k.Preset]; ok {
		body.Preset = &consoleapi.RoutingPresetSummary{
			Id: id, Name: p.Name, Scope: consoleapi.RoutingPresetScope(p.Scope.String()), Version: p.Version, Disabled: &p.Disabled,
		}
	}
}
