package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/typeid"
	"axiom/internal/websearch"
)

func (a adminAPI) GetSystemSettings(ctx context.Context, r adminapi.GetSystemSettingsRequestObject) (adminapi.GetSystemSettingsResponseObject, error) {
	id, _, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	var v accounts.SystemSettings
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { v, err = accounts.ReadSystemSettings(ctx, tx, id); return err })
	if err != nil {
		return nil, err
	}
	return adminJSON[adminapi.GetSystemSettings200JSONResponse](adminSystemSettings(v))
}

// adminSystemSettings adds the capability that lets the settings page save a name without the registration mode. A node
// of the previous release does not send it, so the page keeps the name read-only rather than overwriting a mode another
// administrator changed with the one its form read.
func adminSystemSettings(v accounts.SystemSettings) adminapi.SystemSettings {
	editable := true
	registration := adminapi.SystemSettingsRegistration(v.Registration)
	return adminapi.SystemSettings{Name: &v.Name, Registration: &registration, NameEditable: &editable}
}

func (a adminAPI) SetSystemSettings(ctx context.Context, r adminapi.SetSystemSettingsRequestObject) (adminapi.SetSystemSettingsResponseObject, error) {
	id, _, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	v := accounts.SystemSettings{Name: r.Body.Settings.Name}
	if r.Body.Settings.Registration != nil {
		v.Registration = string(*r.Body.Settings.Registration)
	}
	body, replayed, err := idempotently(ctx, a.db, "setSystemSettings", r.Params.IdempotencyKey, []any{r.SiteId, r.Body}, func(tx pg.Tx) (adminapi.SystemSettings, error) {
		if err := accounts.SetSystemSettings(ctx, tx, actor(ctx), id, v, r.Body.Reason); err != nil {
			return adminapi.SystemSettings{}, settingProblem(err)
		}
		saved, err := accounts.ReadSystemSettings(ctx, tx, id)
		if err != nil {
			return adminapi.SystemSettings{}, err
		}
		return adminJSON[adminapi.SystemSettings](adminSystemSettings(saved))
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "system settings saved but snapshot not refreshed", "err", err)
		}
	}
	return adminapi.SetSystemSettings200JSONResponse{Body: body, Headers: adminapi.SetSystemSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func paymentSettingsBody(v payments.SiteSettings, c platform.Currency, buffer int32) adminapi.PaymentSettings {
	body := adminapi.PaymentSettings{Open: &v.Open}
	if v.Min != nil {
		m := adminMoney(*v.Min, c)
		body.CreditsMin = &m
	}
	if v.Max != nil {
		m := adminMoney(*v.Max, c)
		body.CreditsMax = &m
	}
	ppm := int(buffer)
	body.TopUpBufferPpm = &ppm
	return body
}

func (a adminAPI) GetPaymentSettings(ctx context.Context, r adminapi.GetPaymentSettingsRequestObject) (adminapi.GetPaymentSettingsResponseObject, error) {
	id, site, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	var v payments.SiteSettings
	var buffer int32
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		if v, err = payments.ReadSiteSettings(ctx, tx, uuid.UUID(id)); err != nil {
			return err
		}
		buffer, err = billing.TopUpBufferPPM(ctx, tx, uuid.UUID(id))
		return err
	})
	if err != nil {
		return nil, err
	}
	return adminapi.GetPaymentSettings200JSONResponse(paymentSettingsBody(v, site.Currency, buffer)), nil
}

// paymentSettingsInput reads the collection controls, answering every field that is wrong at once.
func paymentSettingsInput(body adminapi.PaymentSettingsUpdate, currency platform.Currency) (payments.SiteSettings, error) {
	v := payments.SiteSettings{Open: body.Settings.Open}
	var p problems
	for _, f := range []struct {
		name string
		in   *adminapi.Money
		out  **int64
	}{{"credits_min", body.Settings.CreditsMin, &v.Min}, {"credits_max", body.Settings.CreditsMax, &v.Max}} {
		if f.in != nil {
			n, ok := moneyIn(*f.in, currency, "/settings/"+f.name, &p)
			if ok {
				*f.out = &n
			}
		}
	}
	return v, p.err()
}

func (a adminAPI) SetPaymentSettings(ctx context.Context, r adminapi.SetPaymentSettingsRequestObject) (adminapi.SetPaymentSettingsResponseObject, error) {
	id, site, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	v, err := paymentSettingsInput(*r.Body, site.Currency)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "setPaymentSettings", r.Params.IdempotencyKey, []any{r.SiteId, r.Body}, func(tx pg.Tx) (adminapi.PaymentSettings, error) {
		if err := payments.SetSiteSettings(ctx, tx, actor(ctx), uuid.UUID(id), v, r.Body.Reason); err != nil {
			if e, ok := errors.AsType[*payments.FieldError](err); ok {
				return adminapi.PaymentSettings{}, invalidField("/settings"+e.Field, e.Reason)
			}
			return adminapi.PaymentSettings{}, err
		}
		if r.Body.Settings.TopUpBufferPpm != nil {
			ppm := int32(*r.Body.Settings.TopUpBufferPpm) //nolint:gosec // the contract bounds it to 100000
			if err := billing.SetTopUpBuffer(ctx, tx, actor(ctx), uuid.UUID(id), ppm, r.Body.Reason); err != nil {
				return adminapi.PaymentSettings{}, err
			}
		}
		buffer, err := billing.TopUpBufferPPM(ctx, tx, uuid.UUID(id))
		if err != nil {
			return adminapi.PaymentSettings{}, err
		}
		return paymentSettingsBody(v, site.Currency, buffer), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetPaymentSettings200JSONResponse{Body: body, Headers: adminapi.SetPaymentSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func mailSettingsBody(cfg mail.Config, source string, restart *bool) (adminapi.MailSettings, error) {
	return adminJSON[adminapi.MailSettings](map[string]any{"enabled": cfg.Configured(), "host": cfg.Host, "port": cfg.Port, "username": cfg.Username, "from": cfg.From.String(), "has_password": cfg.Password.Reveal() != "", "source": source, "restart_required": restart})
}

func (a adminAPI) GetMailSettings(ctx context.Context, _ adminapi.GetMailSettingsRequestObject) (adminapi.GetMailSettingsResponseObject, error) {
	cfg, found, err := a.savedMail(ctx)
	if err != nil {
		return nil, err
	}
	source := "environment"
	if found {
		source = "database"
	}
	body, err := mailSettingsBody(cfg, source, nil)
	return adminapi.GetMailSettings200JSONResponse(body), err
}

func (a adminAPI) savedMail(ctx context.Context) (cfg mail.Config, found bool, err error) {
	cfg = a.smtp
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var stored mail.Config
		stored, found, err = mail.StoredConfig(ctx, tx, a.keys)
		if found {
			cfg = stored
		}
		return err
	})
	return cfg, found, err
}

func (a adminAPI) SetMailSettings(ctx context.Context, r adminapi.SetMailSettingsRequestObject) (adminapi.SetMailSettingsResponseObject, error) {
	v := r.Body.Settings
	s := mail.Settings{Enabled: v.Enabled, Host: textValue(v.Host), Port: 587, Username: textValue(v.Username), From: textValue(v.From), Password: v.Password}
	if v.Port != nil {
		s.Port = *v.Port
	}
	// Credentials never enter the persisted, unkeyed idempotency fingerprint.
	identity := []any{s.Enabled, s.Host, s.Port, s.Username, s.From, r.Body.Reason, s.Password != nil}
	body, replayed, err := idempotently(ctx, a.db, "setMailSettings", r.Params.IdempotencyKey, identity, func(tx pg.Tx) (adminapi.MailSettings, error) {
		if s.Enabled {
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := a.exit.Check(checkCtx, s.Host); err != nil {
				return adminapi.MailSettings{}, invalidField("/settings/host", "SMTP destination could not be validated as public")
			}
		}
		cfg, err := mail.SaveConfig(ctx, tx, a.keys, actor(ctx), s, r.Body.Reason)
		if errors.Is(err, mail.ErrInvalidConfig) {
			return adminapi.MailSettings{}, invalidField("/settings", "invalid SMTP configuration; verify host, port, sender and credentials, and reenter password after destination changes")
		}
		if err != nil {
			return adminapi.MailSettings{}, err
		}
		return mailSettingsBody(cfg, "database", new(true))
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetMailSettings200JSONResponse{Body: body, Headers: adminapi.SetMailSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func searchSettingsBody(cfg websearch.Config, source string, restart *bool) (adminapi.SearchSettings, error) {
	return adminJSON[adminapi.SearchSettings](map[string]any{"url": cfg.URL, "has_key": cfg.Key.Reveal() != "", "source": source, "restart_required": restart})
}

func (a adminAPI) GetSearchSettings(ctx context.Context, _ adminapi.GetSearchSettingsRequestObject) (adminapi.GetSearchSettingsResponseObject, error) {
	cfg, found, err := a.savedSearch(ctx)
	if err != nil {
		return nil, err
	}
	source := "environment"
	if found {
		source = "database"
	}
	body, err := searchSettingsBody(cfg, source, nil)
	return adminapi.GetSearchSettings200JSONResponse(body), err
}

func (a adminAPI) savedSearch(ctx context.Context) (cfg websearch.Config, found bool, err error) {
	cfg = a.search
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var stored websearch.Config
		stored, found, err = websearch.StoredConfig(ctx, tx, a.keys)
		if found {
			cfg = stored
		}
		return err
	})
	return cfg, found, err
}

func (a adminAPI) SetSearchSettings(ctx context.Context, r adminapi.SetSearchSettingsRequestObject) (adminapi.SetSearchSettingsResponseObject, error) {
	v := r.Body.Settings
	s := websearch.Settings{URL: strings.TrimSpace(textValue(v.Url)), Key: v.Key}
	// Credentials never enter the persisted, unkeyed idempotency fingerprint.
	identity := []any{s.URL, r.Body.Reason, s.Key != nil}
	body, replayed, err := idempotently(ctx, a.db, "setSearchSettings", r.Params.IdempotencyKey, identity, func(tx pg.Tx) (adminapi.SearchSettings, error) {
		if s.URL != "" {
			if err := a.checkSearchURL(ctx, s.URL); err != nil {
				return adminapi.SearchSettings{}, err
			}
		}
		cfg, err := websearch.SaveConfig(ctx, tx, a.keys, actor(ctx), s, a.search, r.Body.Reason)
		if errors.Is(err, websearch.ErrInvalidConfig) {
			return adminapi.SearchSettings{}, invalidField("/settings", "invalid search backend configuration; verify the URL and reenter the key after a URL change")
		}
		if err != nil {
			return adminapi.SearchSettings{}, err
		}
		return searchSettingsBody(cfg, "database", new(true))
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSearchSettings200JSONResponse{Body: body, Headers: adminapi.SetSearchSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// checkSearchURL refuses a backend URL this node cannot use: not an http(s) base URL, or a host the egress rules keep
// off the public internet.
func (a adminAPI) checkSearchURL(ctx context.Context, raw string) error {
	cfg, err := websearch.Parse(raw, "")
	if err != nil {
		return invalidField("/settings/url", "the search backend must be an http(s) base URL with a host and no credentials, query or fragment")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, err := url.Parse(cfg.URL)
	if err != nil {
		return invalidField("/settings/url", "the search backend must be an http(s) base URL with a host")
	}
	if err := a.exit.Check(checkCtx, host.Hostname()); err != nil {
		return invalidField("/settings/url", "the search backend could not be validated as public")
	}
	return nil
}

func (a adminAPI) TestSearchSettings(ctx context.Context, r adminapi.TestSearchSettingsRequestObject) (adminapi.TestSearchSettingsResponseObject, error) {
	if err := a.tests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	cfg, _, err := a.savedSearch(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	body, replayed, err := idempotently(ctx, a.db, "testSearchSettings", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.ConfigTestResult, error) {
		result := websearch.TestConfig(ctx, cfg, a.exit, a.logger)
		err := audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: "search_config.test", Reason: r.Body.Reason, Target: audit.Target{Module: "platform", Kind: "search_config", ID: uuid.Nil().String()}, Detail: map[string]any{"status": result}})
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

func (a adminAPI) TestMailSettings(ctx context.Context, r adminapi.TestMailSettingsRequestObject) (adminapi.TestMailSettingsResponseObject, error) {
	if err := a.tests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	cfg, _, err := a.savedMail(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
	defer cancel()
	body, replayed, err := idempotently(ctx, a.db, "testMailSettings", r.Params.IdempotencyKey, r.Body, func(tx pg.Tx) (adminapi.ConfigTestResult, error) {
		result := mail.TestConfig(ctx, cfg, r.Body.To, a.logger)
		err := audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: "smtp_config.test", Reason: r.Body.Reason, Target: audit.Target{Module: "platform", Kind: "smtp_config", ID: uuid.Nil().String()}, Detail: map[string]any{"status": result}})
		body, e := adminJSON[adminapi.ConfigTestResult](map[string]string{"status": result})
		if err != nil {
			return body, err
		}
		return body, e
	})
	if err != nil {
		return nil, err
	}
	return adminapi.TestMailSettings200JSONResponse{Body: body, Headers: adminapi.TestMailSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) TestMerchantSettings(ctx context.Context, r adminapi.TestMerchantSettingsRequestObject) (adminapi.TestMerchantSettingsResponseObject, error) {
	if err := a.tests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	id, err := typeid.MustPrefix("mch").Decode(r.MerchantId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	p, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "testMerchantSettings", r.Params.IdempotencyKey, []any{r.MerchantId, r.Body}, func(tx pg.Tx) (adminapi.ConfigTestResult, error) {
		merchant, err := payments.ReadMerchant(ctx, tx, payments.MerchantID(id))
		if err != nil {
			return adminapi.ConfigTestResult{}, err
		}
		status, err := p.TestMerchant(ctx, tx, payments.MerchantID(id))
		if err != nil {
			return adminapi.ConfigTestResult{}, err
		}
		err = audit.Record(ctx, tx, audit.Event{Actor: actor(ctx), Action: "merchant_config.test", Reason: r.Body.Reason, Target: audit.Target{Module: "payments", Kind: "merchant", ID: r.MerchantId, Site: merchant.SiteID}, Detail: map[string]any{"status": status}})
		body, e := adminJSON[adminapi.ConfigTestResult](map[string]string{"status": status})
		if err != nil {
			return body, err
		}
		return body, e
	})
	if err != nil {
		return nil, err
	}
	return adminapi.TestMerchantSettings200JSONResponse{Body: body, Headers: adminapi.TestMerchantSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func textValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
