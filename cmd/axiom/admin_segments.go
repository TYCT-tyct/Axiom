package main

import (
	"context"
	"errors"
	"time"

	"axiom/internal/billing"
	"axiom/internal/gateway"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func (a adminAPI) GetSegmentSettings(ctx context.Context, _ adminapi.GetSegmentSettingsRequestObject) (adminapi.GetSegmentSettingsResponseObject, error) {
	var v billing.SegmentSettings
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		v, err = billing.ReadSegmentSettings(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSegmentSettings200JSONResponse(segmentSettingsView(v)), nil
}

func (a adminAPI) SetSegmentSettings(ctx context.Context, r adminapi.SetSegmentSettingsRequestObject) (adminapi.SetSegmentSettingsResponseObject, error) {
	v := segmentSettingsInput(r.Body.Settings)
	body, replayed, err := idempotently(ctx, a.db, "setSegmentSettings", r.Params.IdempotencyKey, []any{r.Body}, func(tx pg.Tx) (adminapi.SegmentSettingsView, error) {
		if err := billing.SetSegmentSettings(ctx, tx, actor(ctx), v, gateway.DefaultConfig().ExtendWait, r.Body.Reason); err != nil {
			return adminapi.SegmentSettingsView{}, segmentProblem(err)
		}
		saved, err := billing.ReadSegmentSettings(ctx, tx)
		if err != nil {
			return adminapi.SegmentSettingsView{}, err
		}
		return segmentSettingsView(saved), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSegmentSettings200JSONResponse{
		Body: body, Headers: adminapi.SetSegmentSettings200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func segmentSettingsView(s billing.SegmentSettings) adminapi.SegmentSettingsView {
	d := billing.DefaultSegments()
	output, overshoot := d.Output, int(d.Overshoot/time.Second)
	return adminapi.SegmentSettingsView{
		Settings: segmentSettingsBody(s),
		Defaults: adminapi.SegmentSettings{OutputTokens: &output, OvershootSeconds: &overshoot},
	}
}

func segmentSettingsBody(s billing.SegmentSettings) adminapi.SegmentSettings {
	out := adminapi.SegmentSettings{OutputTokens: s.Output}
	if s.Overshoot != nil {
		n := int(*s.Overshoot / time.Second)
		out.OvershootSeconds = &n
	}
	return out
}

func segmentSettingsInput(s adminapi.SegmentSettings) billing.SegmentSettings {
	out := billing.SegmentSettings{Output: s.OutputTokens}
	if s.OvershootSeconds != nil {
		d := time.Duration(*s.OvershootSeconds) * time.Second
		out.Overshoot = &d
	}
	return out
}

func segmentProblem(err error) error {
	if e, ok := errors.AsType[*billing.SettingError](err); ok {
		return invalidField("/settings/"+e.Field, e.Reason)
	}
	return err
}
