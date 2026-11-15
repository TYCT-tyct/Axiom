package observability

import (
	"time"

	"axiom/internal/platform/jsonx"
)

// OwnAttempt contains only the metadata the account may read. Endpoint is an internal lookup key;
// the HTTP adapter replaces it with the public codename before serialization.
type OwnAttempt struct {
	Sequence              int
	Endpoint, SourceClass string
	StartedAt, FinishedAt time.Time
	FirstTokenMs          int
	Result, ErrorKind     string
}

// OwnRoute is an allowlist of routing's stored summary, never the raw JSON.
type OwnRoute struct {
	Preset            *OwnRoutePreset `json:"preset,omitempty"`
	ExcludedReasons   map[string]int  `json:"excluded_reasons"`
	ExcludedMore      int             `json:"excluded_more"`
	SelectedEndpoint  string          `json:"-"`
	FastestUnmeasured bool            `json:"fastest_unmeasured"`
	PresetUnavailable bool            `json:"preset_unavailable"`
}

// OwnRoutePreset identifies the frozen rules used by the call.
type OwnRoutePreset struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func ownRoute(raw string) (*OwnRoute, error) {
	var stored struct {
		Preset   jsonx.Raw `json:"preset"`
		Excluded []struct {
			Reason string `json:"reason"`
		} `json:"excluded"`
		ExcludedReasons   map[string]int `json:"excluded_reasons"`
		ExcludedMore      int            `json:"excluded_more"`
		Route             []string       `json:"route"`
		FastestUnmeasured bool           `json:"fastest_unmeasured"`
		PresetUnavailable bool           `json:"preset_unavailable"`
	}
	if err := jsonx.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, err
	}
	r := &OwnRoute{
		ExcludedReasons: stored.ExcludedReasons, ExcludedMore: stored.ExcludedMore,
		FastestUnmeasured: stored.FastestUnmeasured, PresetUnavailable: stored.PresetUnavailable,
	}
	if len(stored.Preset) > 0 && stored.Preset[0] == '{' {
		r.Preset = &OwnRoutePreset{}
		if err := jsonx.Unmarshal(stored.Preset, r.Preset); err != nil {
			return nil, err
		}
	}
	if r.ExcludedReasons == nil {
		r.ExcludedReasons = map[string]int{}
		for _, e := range stored.Excluded {
			r.ExcludedReasons[e.Reason]++
		}
	}
	if len(stored.Route) > 0 {
		r.SelectedEndpoint = stored.Route[0]
	}
	return r, nil
}
