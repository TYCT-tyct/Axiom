package main

import (
	"context"
	"errors"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/quality"
)

func (a adminAPI) GetQualityProbeSettings(ctx context.Context, _ adminapi.GetQualityProbeSettingsRequestObject) (adminapi.GetQualityProbeSettingsResponseObject, error) {
	var v quality.ProbeSettings
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		v, err = quality.ReadProbeSettings(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetQualityProbeSettings200JSONResponse(probeSettingsView(v)), nil
}

func (a adminAPI) SetQualityProbeSettings(ctx context.Context, r adminapi.SetQualityProbeSettingsRequestObject) (adminapi.SetQualityProbeSettingsResponseObject, error) {
	v := probeSettingsInput(r.Body.Settings)
	body, replayed, err := idempotently(ctx, a.db, "setQualityProbeSettings", r.Params.IdempotencyKey, []any{r.Body}, func(tx pg.Tx) (adminapi.ProbeSettingsView, error) {
		if err := quality.SetProbeSettings(ctx, tx, actor(ctx), v, r.Body.Reason); err != nil {
			return adminapi.ProbeSettingsView{}, probeProblem(err)
		}
		saved, err := quality.ReadProbeSettings(ctx, tx)
		if err != nil {
			return adminapi.ProbeSettingsView{}, err
		}
		return probeSettingsView(saved), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetQualityProbeSettings200JSONResponse{
		Body: body, Headers: adminapi.SetQualityProbeSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func probeSettingsView(s quality.ProbeSettings) adminapi.ProbeSettingsView {
	d := quality.DefaultProbeSettings()
	return adminapi.ProbeSettingsView{
		Settings: probeSettingsBody(s),
		Defaults: probeSettingsBody(d),
	}
}

func probeSettingsBody(s quality.ProbeSettings) adminapi.ProbeSettings {
	out := adminapi.ProbeSettings{}
	if s.HourlyBudget != 0 {
		out.HourlyBudget = &s.HourlyBudget
	}
	if s.Concurrency != 0 {
		out.Concurrency = &s.Concurrency
	}
	seconds := func(d time.Duration) *int {
		if d == 0 {
			return nil
		}
		n := int(d / time.Second)
		return &n
	}
	out.VerifyEverySeconds = seconds(s.VerifyEvery)
	out.VerifyMaxAgeSeconds = seconds(s.VerifyMaxAge)
	out.VerifyRetrySeconds = seconds(s.VerifyRetry)
	out.ProbeBaseSeconds = seconds(s.ProbeBase)
	return out
}

func probeSettingsInput(s adminapi.ProbeSettings) quality.ProbeSettings {
	out := quality.ProbeSettings{}
	if s.HourlyBudget != nil {
		out.HourlyBudget = *s.HourlyBudget
	}
	if s.Concurrency != nil {
		out.Concurrency = *s.Concurrency
	}
	seconds := func(n *int) time.Duration {
		if n == nil {
			return 0
		}
		return time.Duration(*n) * time.Second
	}
	out.VerifyEvery = seconds(s.VerifyEverySeconds)
	out.VerifyMaxAge = seconds(s.VerifyMaxAgeSeconds)
	out.VerifyRetry = seconds(s.VerifyRetrySeconds)
	out.ProbeBase = seconds(s.ProbeBaseSeconds)
	return out
}

func probeProblem(err error) error {
	if e, ok := errors.AsType[*quality.SettingError](err); ok {
		return invalidField("/settings/"+e.Field, e.Reason)
	}
	return err
}
