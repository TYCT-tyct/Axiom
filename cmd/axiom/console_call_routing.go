package main

import (
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
)

func (c consoleAPI) callRoutingBody(call observability.OwnCall, body *consoleapi.Call, charged bool) {
	codes := map[string]string{}
	if c.upstreams != nil {
		// A retired endpoint or stale snapshot has no current public identity. Keep the
		// rest of the historical record readable without substituting an internal ID.
		if endpoints, err := c.upstreams.Routable(); err == nil {
			for _, e := range endpoints {
				codes[e.ID.String()] = e.Codename
			}
		}
	}
	attempts := make([]consoleapi.CallAttempt, len(call.Attempts))
	for i, a := range call.Attempts {
		attempts[i] = consoleapi.CallAttempt{
			Sequence: new(a.Sequence), EndpointCode: optionalText(codes[a.Endpoint]), SourceClass: new(a.SourceClass),
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
			SelectedEndpoint: optionalText(codes[r.SelectedEndpoint]), FastestUnmeasured: &r.FastestUnmeasured,
			PresetUnavailable: &r.PresetUnavailable,
		}
		if r.Preset != nil {
			route.Preset = &consoleapi.OwnCallRoutePreset{Id: &r.Preset.ID, Version: &r.Preset.Version}
		}
		body.Route = route
	}
}

// The log lacks billing's loop membership. Distinct-endpoint fallback attempts
// cannot share the final model-side loop; repeated final endpoints are unknown.
func attemptCharged(call observability.OwnCall, i int, charged bool) *bool {
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
