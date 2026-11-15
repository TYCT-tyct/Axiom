package observability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
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

// OwnCallDetail is optional console metadata, read separately from the basic call.
// Its endpoint identifiers are lookup keys: HTTP must replace them with public codenames.
type OwnCallDetail struct {
	Attempts []OwnAttempt
	Route    *OwnRoute
}

// OwnRoute is an allowlist of the stored summary. It contains internal lookup IDs;
// it is not a wire representation and must not be serialized directly.
type OwnRoute struct {
	Preset            *OwnRoutePreset
	ExcludedReasons   map[string]int
	ExcludedMore      int
	SelectedEndpoint  string
	FastestUnmeasured bool
	PresetUnavailable bool
}

// OwnRoutePreset identifies the frozen rules used by the call.
type OwnRoutePreset struct {
	ID      string
	Version int64
}

// ReadOwnDetail reads a call already obtained through GetOwn in the same scoped
// transaction. Both reads use its partition key; malformed optional routing data
// is omitted without logging its contents or preventing access to the call.
func ReadOwnDetail(ctx context.Context, tx pg.Tx, call OwnCall, logger *slog.Logger) (OwnCallDetail, error) {
	q := store.New()
	raw, err := q.GetOwnRoute(ctx, tx, store.GetOwnRouteParams{ID: call.ID.String(), At: call.At})
	if err != nil {
		return OwnCallDetail{}, fmt.Errorf("get own route: %w", err)
	}
	rows, err := q.ListTraceAttempts(ctx, tx, store.ListTraceAttemptsParams{RequestID: call.ID.String(), At: call.At})
	if err != nil {
		return OwnCallDetail{}, fmt.Errorf("get own attempts: %w", err)
	}
	detail := OwnCallDetail{Attempts: make([]OwnAttempt, len(rows))}
	for i, a := range rows {
		detail.Attempts[i] = OwnAttempt{
			Sequence: int(a.AttemptSeq), Endpoint: a.EndpointID, SourceClass: a.SourceClass,
			StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, FirstTokenMs: int(a.FirstTokenMs), Result: a.Outcome, ErrorKind: a.ErrorKind,
		}
	}
	detail.Route = optionalOwnRoute(ctx, call, raw, logger)
	return detail, nil
}

// optionalOwnRoute isolates compatibility failures from database failures. The
// diagnostic names the request, never the stored JSON or offending value.
func optionalOwnRoute(ctx context.Context, call OwnCall, raw string, logger *slog.Logger) *OwnRoute {
	var route *OwnRoute
	if raw != "{}" {
		parsed, err := ownRoute(raw)
		if err != nil {
			logger.WarnContext(ctx, "a call's stored route is unreadable", "request_id", call.ID.String())
			return nil
		}
		route = parsed
	}
	if call.ErrorKind == "preset_unavailable" {
		if route == nil {
			route = &OwnRoute{ExcludedReasons: map[string]int{}}
		}
		route.PresetUnavailable = true
	}
	return route
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
		var preset struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		}
		if err := jsonx.Unmarshal(stored.Preset, &preset); err != nil {
			return nil, err
		}
		r.Preset = &OwnRoutePreset{ID: preset.ID, Version: preset.Version}
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
