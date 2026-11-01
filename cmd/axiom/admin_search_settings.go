package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/websearch"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

func engineBody(e websearch.Engine) adminapi.SearchEngine {
	return adminapi.SearchEngine{
		Id: openapi_types.UUID(e.ID), Name: e.Name, Kind: adminapi.SearchEngineKind(e.Kind), Url: e.URL,
		HasKey: e.Key.Reveal() != "", Enabled: e.Enabled, Default: e.Default,
		CostPerSearch: e.CostSearch.String(), CostPerExtraResult: e.CostResult.String(), Markup: e.Markup,
		PricePerSearch: e.PriceSearch.String(), PricePerExtraResult: e.PriceResult.String(), PriceVersion: e.PriceVersion,
	}
}

func searchSettingsBody(l websearch.Loaded, restart *bool) adminapi.SearchSettings {
	engines := make([]adminapi.SearchEngine, 0, len(l.Engines))
	for _, e := range l.Engines {
		engines = append(engines, engineBody(e))
	}
	source := adminapi.SearchSettingsSource(l.Source)
	body := adminapi.SearchSettings{Engines: &engines, Source: &source}
	// The old page reads only url and has_key. The default engine is what its single backend field edited.
	url, hasKey := "", false
	for _, e := range l.Engines {
		if e.Default || url == "" {
			url, hasKey = e.URL, e.Key.Reveal() != ""
			if e.Default {
				break
			}
		}
	}
	body.Url, body.HasKey = &url, &hasKey
	if restart != nil {
		body.RestartRequired = restart
	}
	return body
}

func (a adminAPI) GetSearchSettings(ctx context.Context, _ adminapi.GetSearchSettingsRequestObject) (adminapi.GetSearchSettingsResponseObject, error) {
	loaded, err := a.savedEngines(ctx)
	if err != nil {
		return nil, err
	}
	return adminapi.GetSearchSettings200JSONResponse(searchSettingsBody(loaded, nil)), nil
}

func (a adminAPI) savedEngines(ctx context.Context) (loaded websearch.Loaded, err error) {
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var e error
		loaded, e = websearch.Load(ctx, tx, a.keys, a.search)
		return e
	})
	return loaded, err
}

// engineSettings is one input in the form the module validates, without its key.
func engineSettings(e adminapi.SearchEngineInput) websearch.EngineSettings {
	s := websearch.EngineSettings{
		Name: strings.TrimSpace(e.Name), Kind: string(e.Kind), Enabled: e.Enabled, Default: e.Default,
		CostSearch: strings.TrimSpace(e.CostPerSearch), CostResult: strings.TrimSpace(e.CostPerExtraResult),
		Markup: strings.TrimSpace(e.Markup), Key: e.Key,
	}
	if e.Id != nil && uuid.UUID(*e.Id) != uuid.Nil() {
		s.ID = e.Id.String()
	}
	if e.Url != nil {
		s.URL = strings.TrimSpace(*e.Url)
	}
	return s
}

func mergeLegacyEngine(edited websearch.EngineSettings, saved []websearch.Engine) []websearch.EngineSettings {
	out := make([]websearch.EngineSettings, 0, len(saved)+1)
	replaced := false
	for _, e := range saved {
		if e.Default && !replaced {
			edited.ID, edited.Name, edited.Kind = e.ID.String(), e.Name, string(e.Kind)
			edited.Enabled, edited.Default = e.Enabled, e.Default
			edited.CostSearch, edited.CostResult, edited.Markup = e.CostSearch.String(), e.CostResult.String(), e.Markup
			out, replaced = append(out, edited), true
			continue
		}
		out = append(out, engineSettingsFrom(e))
	}
	if !replaced {
		out = append(out, edited)
	}
	return out
}

func engineSettingsFrom(e websearch.Engine) websearch.EngineSettings {
	return websearch.EngineSettings{
		ID: e.ID.String(), Name: e.Name, Kind: string(e.Kind), URL: e.URL, Enabled: e.Enabled, Default: e.Default,
		CostSearch: e.CostSearch.String(), CostResult: e.CostResult.String(), Markup: e.Markup,
	}
}

// searchSettingsInput accepts both the engine list and the old single-backend settings object. A page built before
// engines existed sends the latter; an empty URL there means "turn search off", the same as an empty list.
func searchSettingsInput(body adminapi.SearchSettingsUpdate) (websearch.Settings, error) {
	if body.Engines != nil {
		in := websearch.Settings{Engines: make([]websearch.EngineSettings, len(*body.Engines))}
		for i, e := range *body.Engines {
			in.Engines[i] = engineSettings(e)
		}
		return in, nil
	}
	if body.Settings == nil {
		return websearch.Settings{}, invalidField("/settings", "send engines, or the old settings object")
	}
	var url string
	if body.Settings.Url != nil {
		url = strings.TrimSpace(*body.Settings.Url)
	}
	if url == "" {
		return websearch.Settings{}, nil
	}
	return websearch.Settings{Engines: []websearch.EngineSettings{{
		Name: "searxng", Kind: string(websearch.KindSearXNG), URL: url, Key: body.Settings.Key,
		Enabled: true, Default: true, CostSearch: "0", CostResult: "0", Markup: "0",
	}}}, nil
}

func (a adminAPI) SetSearchSettings(ctx context.Context, r adminapi.SetSearchSettingsRequestObject) (adminapi.SetSearchSettingsResponseObject, error) {
	in, err := a.searchSettingsFor(ctx, *r.Body)
	if err != nil {
		return nil, err
	}
	identity := searchIdentity(in, r.Body.Reason)
	body, replayed, err := idempotently(ctx, a.db, "setSearchSettings", r.Params.IdempotencyKey, identity, func(tx pg.Tx) (adminapi.SearchSettings, error) {
		if err := a.checkEngines(ctx, in); err != nil {
			return adminapi.SearchSettings{}, err
		}
		loaded, err := websearch.Save(ctx, tx, a.keys, actor(ctx), in, a.search, r.Body.Reason)
		if errors.Is(err, websearch.ErrInvalidConfig) {
			return adminapi.SearchSettings{}, invalidField("/settings/engines", "invalid search engine configuration; verify each name, kind, URL, cost, markup and key, and reenter the key after a URL or kind change")
		}
		if err != nil {
			return adminapi.SearchSettings{}, err
		}
		return searchSettingsBody(loaded, new(true)), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSearchSettings200JSONResponse{Body: body, Headers: adminapi.SetSearchSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// mergeLegacy edits the default engine an old form showed and keeps the engines it never showed.
func (a adminAPI) mergeLegacy(ctx context.Context, in websearch.Settings) (websearch.Settings, error) {
	loaded, err := a.savedEngines(ctx)
	if err != nil {
		return in, err
	}
	if len(loaded.Engines) > 0 {
		in.Engines = mergeLegacyEngine(in.Engines[0], loaded.Engines)
	}
	return in, nil
}

// searchIdentity is the idempotency fingerprint: names and destinations, never a key.
func searchIdentity(in websearch.Settings, reason string) []any {
	identity := make([]any, 0, 2*len(in.Engines)+1)
	identity = append(identity, reason)
	for _, e := range in.Engines {
		identity = append(identity, e.ID, e.Name, e.Kind, e.URL, e.Enabled, e.Default, e.CostSearch, e.CostResult,
			e.Markup, e.Key != nil)
	}
	return identity
}

func (a adminAPI) searchSettingsFor(ctx context.Context, body adminapi.SearchSettingsUpdate) (websearch.Settings, error) {
	in, err := searchSettingsInput(body)
	if err != nil {
		return in, err
	}
	if body.Engines == nil && body.Settings != nil && len(in.Engines) == 1 && strings.TrimSpace(in.Engines[0].URL) != "" {
		return a.mergeLegacy(ctx, in)
	}
	return in, nil
}

func (a adminAPI) checkEngines(ctx context.Context, in websearch.Settings) error {
	for _, e := range in.Engines {
		if e.URL == "" {
			continue
		}
		if err := a.checkSearchURL(ctx, e.URL); err != nil {
			return err
		}
	}
	return nil
}

// checkSearchURL refuses a backend URL this node cannot use: not an http(s) base URL, or a host the egress rules keep
// off the public internet.
func (a adminAPI) checkSearchURL(ctx context.Context, raw string) error {
	cfg, err := websearch.Parse(raw, "")
	if err != nil {
		return invalidField("/settings/engines/url", "the search backend must be an http(s) base URL with a host and no credentials, query or fragment")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, err := url.Parse(cfg.URL)
	if err != nil {
		return invalidField("/settings/engines/url", "the search backend must be an http(s) base URL with a host")
	}
	if err := a.exit.Check(checkCtx, host.Hostname()); err != nil {
		return invalidField("/settings/engines/url", "the search backend could not be validated as public")
	}
	return nil
}

// testEngine fills a key the admin omitted from the saved engine only when the destination is unchanged: the same
// rule Save applies. An explicit empty key stays empty; a changed destination never receives the old credential.
func (a adminAPI) testEngine(ctx context.Context, in adminapi.SearchEngineInput) (websearch.Engine, error) {
	s := engineSettings(in)
	saved, found, err := a.savedTestEngine(ctx, s)
	if err != nil {
		return websearch.Engine{}, err
	}
	kind, url, err := testDestination(s)
	if err != nil {
		return websearch.Engine{}, err
	}
	if url == "" {
		return websearch.Engine{Name: s.Name, Kind: kind, Enabled: true}, nil
	}
	key := testKey(s, saved, found, url, kind)
	if kind == websearch.KindFirecrawl && key == "" {
		return websearch.Engine{}, invalidField("/settings/engines/key", "firecrawl needs an API key; a changed destination needs it entered again")
	}
	return websearch.Engine{Name: s.Name, Kind: kind, URL: url, Key: secret.New(key), Enabled: true}, nil
}

func (a adminAPI) savedTestEngine(ctx context.Context, s websearch.EngineSettings) (websearch.Engine, bool, error) {
	loaded, err := a.savedEngines(ctx)
	if err != nil {
		return websearch.Engine{}, false, err
	}
	for i := range loaded.Engines {
		e := loaded.Engines[i]
		if s.ID != "" && e.ID.String() == s.ID || s.ID == "" && s.Name != "" && e.Name == s.Name {
			return e, true, nil
		}
	}
	return websearch.Engine{}, false, nil
}

func testDestination(s websearch.EngineSettings) (websearch.Kind, string, error) {
	kind, err := websearch.ParseKind(s.Kind)
	if err != nil {
		return "", "", invalidField("/settings/engines/kind", "kind must be searxng or firecrawl")
	}
	raw := s.URL
	if raw == "" && kind == websearch.KindFirecrawl {
		raw = "https://api.firecrawl.dev/v2"
	}
	if raw == "" {
		return kind, "", nil
	}
	cfg, err := websearch.Parse(raw, "")
	if err != nil {
		return "", "", invalidField("/settings/engines/url", "the search backend must be an http(s) base URL with a host, no credentials, query or fragment")
	}
	return kind, cfg.URL, nil
}

func testKey(s websearch.EngineSettings, saved websearch.Engine, found bool, url string, kind websearch.Kind) string {
	if s.Key != nil {
		return strings.TrimSpace(*s.Key)
	}
	if found && saved.URL == url && saved.Kind == kind {
		return saved.Key.Reveal()
	}
	return ""
}

func (a adminAPI) TestSearchSettings(ctx context.Context, r adminapi.TestSearchSettingsRequestObject) (adminapi.TestSearchSettingsResponseObject, error) {
	if err := a.tests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	body, replayed, err := idempotently(ctx, a.db, "testSearchSettings", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.ConfigTestResult, error) {
		engine, err := a.testTarget(ctx, r.Body.Engine)
		if err != nil {
			return adminapi.ConfigTestResult{}, err
		}
		result := websearch.TestEngine(ctx, engine, a.exit, a.logger)
		err = audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: "search_engines.test", Reason: r.Body.Reason, Target: audit.Target{Module: "platform", Kind: "search_engines", ID: uuid.Nil().String()}, Detail: map[string]any{"status": result, "engine": engine.Name}})
		body, e := adminJSON[adminapi.ConfigTestResult](map[string]string{"status": result})
		if err != nil {
			return body, err
		}
		return body, e
	})
	if err != nil {
		return nil, err
	}
	return adminapi.TestSearchSettings200JSONResponse{Body: body, Headers: adminapi.TestSearchSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// testTarget reads the engine the request names, or the saved default for the old {reason} body.
func (a adminAPI) testTarget(ctx context.Context, in *adminapi.SearchEngineInput) (websearch.Engine, error) {
	if in != nil {
		return a.testEngine(ctx, *in)
	}
	loaded, err := a.savedEngines(ctx)
	if err != nil {
		return websearch.Engine{}, err
	}
	var first *websearch.Engine
	for i := range loaded.Engines {
		if loaded.Engines[i].Default {
			return loaded.Engines[i], nil
		}
		if first == nil {
			first = &loaded.Engines[i]
		}
	}
	if first != nil {
		return *first, nil
	}
	return websearch.Engine{}, nil
}
