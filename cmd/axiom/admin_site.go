package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// siteOf reads a site's ID from a path and finds the site. A malformed ID and an unknown one are the same 404.
func (a adminAPI) siteOf(id string) (accounts.SiteID, accounts.Site, error) {
	site, err := accounts.ParseSiteID(id)
	if err != nil {
		return accounts.SiteID{}, accounts.Site{}, accounts.ErrNotFound
	}
	found, ok := a.accounts.Site(site)
	if !ok {
		return accounts.SiteID{}, accounts.Site{}, accounts.ErrNotFound
	}
	return site, found, nil
}

func seconds(d time.Duration) *int {
	if d == 0 {
		return nil
	}
	n := int(d / time.Second)
	return &n
}

// settingsBody is a site's settings as the contract writes them; a setting that is not set is left out.
func settingsBody(v accounts.SiteSettings, currency platform.Currency) adminapi.SiteSettings {
	body := adminapi.SiteSettings{
		ConsoleIdleTimeoutSeconds: seconds(v.ConsoleIdle), ConsoleMaxAgeSeconds: seconds(v.ConsoleMaxAge),
		AdminIdleTimeoutSeconds: seconds(v.AdminIdle), AdminMaxAgeSeconds: seconds(v.AdminMaxAge),
	}
	if v.NewKeyDailyLimit != nil {
		limit := adminMoney(*v.NewKeyDailyLimit, currency)
		body.NewKeyDailyLimit = &limit
	}
	return body
}

func settingsView(own accounts.SiteSettings, currency platform.Currency) adminapi.SiteSettingsView {
	return adminapi.SiteSettingsView{Settings: settingsBody(own, currency), Defaults: settingsBody(accounts.DefaultSiteSettings(), currency)}
}

// duration is a length in seconds as the module holds it; not set is 0.
func duration(seconds *int) time.Duration {
	if seconds == nil {
		return 0
	}
	return time.Duration(*seconds) * time.Second
}

// settingsOf reads the settings a request gives. The bounds of each value are the contract's and the module's; what
// is checked here is the money, which the contract cannot tie to the site's currency.
func settingsOf(b adminapi.SiteSettings, currency platform.Currency) (accounts.SiteSettings, error) {
	v := accounts.SiteSettings{
		ConsoleIdle: duration(b.ConsoleIdleTimeoutSeconds), ConsoleMaxAge: duration(b.ConsoleMaxAgeSeconds),
		AdminIdle: duration(b.AdminIdleTimeoutSeconds), AdminMaxAge: duration(b.AdminMaxAgeSeconds),
	}
	var p problems
	if b.NewKeyDailyLimit != nil {
		if limit, ok := moneyIn(*b.NewKeyDailyLimit, currency, "/settings/new_key_daily_limit", &p); ok {
			v.NewKeyDailyLimit = &limit
		}
	}
	return v, p.err()
}

// GetSiteSettings tells a site's own settings and the defaults that apply where it sets none.
func (a adminAPI) GetSiteSettings(ctx context.Context, request adminapi.GetSiteSettingsRequestObject) (adminapi.GetSiteSettingsResponseObject, error) {
	id, site, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var own accounts.SiteSettings
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		own, err = a.accounts.SiteSettings(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteSettings200JSONResponse(settingsView(own, site.Currency)), nil
}

// UpdateSiteSettings replaces a site's own settings. Sessions shorter than before end on their next request: this node
// reloads the sites at once, the others within their reload interval.
func (a adminAPI) UpdateSiteSettings(ctx context.Context, request adminapi.UpdateSiteSettingsRequestObject) (adminapi.UpdateSiteSettingsResponseObject, error) {
	id, site, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	set, err := settingsOf(request.Body.Settings, site.Currency)
	if err != nil {
		return nil, err
	}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteSettings", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.SiteSettingsView, error) {
			if err := a.accounts.SetSiteSettings(ctx, tx, actor(ctx), id, set, request.Body.Reason); err != nil {
				return adminapi.SiteSettingsView{}, settingProblem(err)
			}
			stored, err := a.accounts.SiteSettings(ctx, tx, id)
			return settingsView(stored, site.Currency), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		// The change is committed. A failure to reload is not its failure: every node reloads within its interval.
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site settings saved but this node did not reload them", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteSettings200JSONResponse{Body: view, Headers: adminapi.UpdateSiteSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// settingProblem is a setting the module found out of bounds as a 400 naming it.
func settingProblem(err error) error {
	if e, ok := errors.AsType[*accounts.SettingError](err); ok {
		return invalidField("/settings/"+e.Field, e.Reason)
	}
	return err
}

// ListTiers lists the tiers of a site, the main site's unless the request names one.
func (a adminAPI) ListTiers(ctx context.Context, request adminapi.ListTiersRequestObject) (adminapi.ListTiersResponseObject, error) {
	var site accounts.SiteID
	if request.Params.SiteId != nil {
		var err error
		if site, err = accounts.ParseSiteID(*request.Params.SiteId); err != nil {
			return nil, invalidField("site_id", "not a site ID")
		}
	} else {
		main, ok := a.accounts.MainSite()
		if !ok { // the admin domain belongs to the main site, so a node that serves it has loaded it
			return nil, accounts.ErrNotFound
		}
		site = main.ID
	}
	var tiers []accounts.Tier
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		tiers, err = a.accounts.Tiers(ctx, tx, site)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.TierList{Data: make([]adminapi.TierRef, len(tiers))}
	for i, t := range tiers {
		list.Data[i] = tierRef(t)
	}
	return adminapi.ListTiers200JSONResponse(list), nil
}

func tierRef(t accounts.Tier) adminapi.TierRef {
	return adminapi.TierRef{Id: t.ID.TypeID(), Name: t.Name}
}

// registrationPolicyBody is a site's registration policy as the contract writes it.
func registrationPolicyBody(p accounts.RegistrationPolicy) adminapi.RegistrationPolicy {
	return adminapi.RegistrationPolicy{
		Mode: adminapi.RegistrationPolicyMode(p.Mode), HumanCheck: p.HumanCheck, HumanCheckBits: p.HumanCheckBits,
		DomainBurst: p.DomainBurst, DomainPerHour: p.DomainPerHour, DomainAllow: p.DomainAllow, DomainDeny: p.DomainDeny,
	}
}

func registrationPolicyView(own accounts.RegistrationPolicy) adminapi.RegistrationPolicyView {
	return adminapi.RegistrationPolicyView{
		Policy: registrationPolicyBody(own), Defaults: registrationPolicyBody(accounts.DefaultRegistrationPolicy()),
	}
}

// registrationPolicyOf reads a policy a request gives. The bounds are the contract's and the module's; the module
// normalizes the domain lists.
func registrationPolicyOf(b adminapi.RegistrationPolicy) accounts.RegistrationPolicy {
	return accounts.RegistrationPolicy{
		Mode: string(b.Mode), HumanCheck: b.HumanCheck, HumanCheckBits: b.HumanCheckBits,
		DomainBurst: b.DomainBurst, DomainPerHour: b.DomainPerHour, DomainAllow: b.DomainAllow, DomainDeny: b.DomainDeny,
	}
}

// registrationRefusal names the contract's field for a setting the module refused.
func registrationRefusal(err error) error {
	e, ok := errors.AsType[*accounts.SettingError](err)
	if !ok {
		return err
	}
	return invalidField("/policy/"+strings.TrimPrefix(e.Field, "registration_"), e.Reason)
}

// GetSiteRegistration tells a site's open-registration policy and the defaults that apply where it sets none.
func (a adminAPI) GetSiteRegistration(ctx context.Context, request adminapi.GetSiteRegistrationRequestObject) (adminapi.GetSiteRegistrationResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var own accounts.RegistrationPolicy
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		own, err = a.accounts.SiteRegistration(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteRegistration200JSONResponse(registrationPolicyView(own)), nil
}

// UpdateSiteRegistration replaces a site's open-registration policy. This node reloads the sites at once; the others
// follow within their reload interval.
func (a adminAPI) UpdateSiteRegistration(ctx context.Context, request adminapi.UpdateSiteRegistrationRequestObject) (adminapi.UpdateSiteRegistrationResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	set := registrationPolicyOf(request.Body.Policy)
	view, replayed, err := idempotently(ctx, a.db, "updateSiteRegistration", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.RegistrationPolicyView, error) {
			if err := a.accounts.SetSiteRegistration(ctx, tx, actor(ctx), id, set, request.Body.Reason); err != nil {
				return adminapi.RegistrationPolicyView{}, registrationRefusal(err)
			}
			saved, err := a.accounts.SiteRegistration(ctx, tx, id)
			if err != nil {
				return adminapi.RegistrationPolicyView{}, err
			}
			return registrationPolicyView(saved), nil
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site registration policy saved but this node did not reload it", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteRegistration200JSONResponse{Body: view, Headers: adminapi.UpdateSiteRegistration200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// siteBrandBody is a site's brand colour as the contract writes it; the default palette leaves the field out.
func siteBrandBody(b accounts.SiteBrand) adminapi.SiteBrand {
	out := adminapi.SiteBrand{}
	if b.Color != "" {
		out.BrandColor = &b.Color
	}
	return out
}

// brandRefusal names the contract's field for a colour the module refused.
func brandRefusal(err error) error {
	e, ok := errors.AsType[*accounts.SettingError](err)
	if !ok {
		return err
	}
	return invalidField("/"+e.Field, e.Reason)
}

// GetSiteBrand tells a site's brand colour, absent when it uses the default palette.
func (a adminAPI) GetSiteBrand(ctx context.Context, request adminapi.GetSiteBrandRequestObject) (adminapi.GetSiteBrandResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var brand accounts.SiteBrand
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		brand, err = accounts.ReadSiteBrand(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteBrand200JSONResponse(siteBrandBody(brand)), nil
}

// UpdateSiteBrand sets or clears a site's brand colour. This node reloads the sites at once; the others follow within
// their reload interval.
func (a adminAPI) UpdateSiteBrand(ctx context.Context, request adminapi.UpdateSiteBrandRequestObject) (adminapi.UpdateSiteBrandResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	set := accounts.SiteBrand{Color: request.Body.BrandColor}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteBrand", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.SiteBrand, error) {
			if err := a.accounts.SetSiteBrand(ctx, tx, actor(ctx), id, set, request.Body.Reason); err != nil {
				return adminapi.SiteBrand{}, brandRefusal(err)
			}
			saved, err := accounts.ReadSiteBrand(ctx, tx, id)
			if err != nil {
				return adminapi.SiteBrand{}, err
			}
			return siteBrandBody(saved), nil
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "site brand colour saved but this node did not reload it", "site", request.SiteId, "err", err)
		}
	}
	return adminapi.UpdateSiteBrand200JSONResponse{Body: view, Headers: adminapi.UpdateSiteBrand200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
