package main

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/typeid"
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
	return adminJSON[adminapi.GetSystemSettings200JSONResponse](v)
}

func (a adminAPI) SetSystemSettings(ctx context.Context, r adminapi.SetSystemSettingsRequestObject) (adminapi.SetSystemSettingsResponseObject, error) {
	id, _, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	v := accounts.SystemSettings{Name: r.Body.Settings.Name, Registration: string(r.Body.Settings.Registration)}
	body, replayed, err := idempotently(ctx, a.db, "setSystemSettings", r.Params.IdempotencyKey, []any{r.SiteId, r.Body}, func(tx pg.Tx) (adminapi.SystemSettings, error) {
		if err := accounts.SetSystemSettings(ctx, tx, actor(ctx), id, v, r.Body.Reason); err != nil {
			return adminapi.SystemSettings{}, settingProblem(err)
		}
		return adminJSON[adminapi.SystemSettings](v)
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

func paymentSettingsBody(v payments.SiteSettings, c platform.Currency) adminapi.PaymentSettings {
	body := adminapi.PaymentSettings{Open: &v.Open}
	if v.Min != nil {
		m := adminMoney(*v.Min, c)
		body.CreditsMin = &m
	}
	if v.Max != nil {
		m := adminMoney(*v.Max, c)
		body.CreditsMax = &m
	}
	return body
}

func (a adminAPI) GetPaymentSettings(ctx context.Context, r adminapi.GetPaymentSettingsRequestObject) (adminapi.GetPaymentSettingsResponseObject, error) {
	id, site, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	var v payments.SiteSettings
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { v, err = payments.ReadSiteSettings(ctx, tx, uuid.UUID(id)); return err })
	if err != nil {
		return nil, err
	}
	return adminapi.GetPaymentSettings200JSONResponse(paymentSettingsBody(v, site.Currency)), nil
}

func (a adminAPI) SetPaymentSettings(ctx context.Context, r adminapi.SetPaymentSettingsRequestObject) (adminapi.SetPaymentSettingsResponseObject, error) {
	id, site, err := a.siteOf(r.SiteId)
	if err != nil {
		return nil, err
	}
	v := payments.SiteSettings{Open: r.Body.Settings.Open}
	var p problems
	for _, f := range []struct {
		name string
		in   *adminapi.Money
		out  **int64
	}{{"credits_min", r.Body.Settings.CreditsMin, &v.Min}, {"credits_max", r.Body.Settings.CreditsMax, &v.Max}} {
		if f.in != nil {
			n, ok := moneyIn(*f.in, site.Currency, "/settings/"+f.name, &p)
			if ok {
				*f.out = &n
			}
		}
	}
	if err = p.err(); err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "setPaymentSettings", r.Params.IdempotencyKey, []any{r.SiteId, r.Body}, func(tx pg.Tx) (adminapi.PaymentSettings, error) {
		err := payments.SetSiteSettings(ctx, tx, actor(ctx), uuid.UUID(id), v, r.Body.Reason)
		if e, ok := errors.AsType[*payments.FieldError](err); ok {
			return adminapi.PaymentSettings{}, invalidField("/settings"+e.Field, e.Reason)
		}
		return paymentSettingsBody(v, site.Currency), err
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
	p, err := payments.Open(payments.Config{DB: a.db, Platform: callOf(ctx).scope, Keyring: a.keys, Client: a.exit, Logger: a.logger, Plugins: paymentPlugins})
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
