package main

import (
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
)

func (c consoleAPI) callRoutingBody(call observability.OwnCallDetail, body *consoleapi.Call, charged bool) {
	attempts := make([]consoleapi.CallAttempt, len(call.Attempts))
	for i, a := range call.Attempts {
		attempts[i] = consoleapi.CallAttempt{
			Sequence: new(a.Sequence), EndpointCode: c.callEndpointCode(a.Endpoint), SourceClass: new(a.SourceClass),
			StartedAt: &a.StartedAt, FinishedAt: &a.FinishedAt, FirstTokenMs: new(a.FirstTokenMs),
			Result: new(consoleapi.CallAttemptResult(a.Result)), ErrorKind: new(a.ErrorKind),
		}
		attempts[i].Charged = attemptCharged(call, i, charged)
	}
	body.Attempts = &attempts
	if call.Route != nil {
		r := call.Route
		route := &consoleapi.OwnCallRoute{
			ExcludedReasons: &r.ExcludedReasons, ExcludedMore: &r.ExcludedMore,
			SelectedEndpoint: c.callEndpointCode(r.SelectedEndpoint), FastestUnmeasured: &r.FastestUnmeasured,
			PresetUnavailable: &r.PresetUnavailable,
		}
		if r.Preset != nil {
			route.Preset = &consoleapi.OwnCallRoutePreset{Id: &r.Preset.ID, Version: &r.Preset.Version}
		}
		body.Route = route
	}
}

func (c consoleAPI) callEndpointCode(endpoint string) *string {
	if c.upstreams == nil {
		return nil
	}
	id, err := uuid.Parse(endpoint)
	if err != nil {
		return nil
	}
	return optionalText(c.upstreams.EndpointCode(id))
}

// The log lacks billing's loop membership. Distinct-endpoint fallback attempts
// cannot share the final model-side loop; repeated final endpoints are unknown.
func attemptCharged(call observability.OwnCallDetail, i int, charged bool) *bool {
	if !charged {
		return new(false)
	}
	last := len(call.Attempts) - 1
	if call.Attempts[i].Endpoint != call.Attempts[last].Endpoint {
		return new(false)
	}
	for j := range last {
		if call.Attempts[j].Endpoint == call.Attempts[last].Endpoint {
			return nil
		}
	}
	return new(i == last)
}
