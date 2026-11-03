package cpa

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// Pool reads the single group's account counts and the mean room left in the tighter window of each usable account.
// The auth file list already carries CPA's passive quota snapshot, so this costs no request beyond the directory read.
func (m *manager) Pool(ctx context.Context) (adapter.PoolReading, error) {
	files, complete, err := m.list(ctx)
	if err != nil {
		return adapter.PoolReading{}, err
	}
	if len(files) == 0 {
		return adapter.PoolReading{Complete: complete}, nil
	}
	now := m.now()
	failed, usableCount, total, sampled := 0, 0, 0, 0
	for _, f := range files {
		switch {
		case f.Status == "error":
			failed++
		case usable(f, now):
			usableCount++
			room, ok, err := headroom(f, now)
			if err != nil {
				return adapter.PoolReading{}, err
			}
			if ok {
				total += room
				sampled++
			}
		}
	}
	g := adapter.GroupCapacity{GroupRef: groupRef, Accounts: len(files), Failed: failed, Usable: usableCount}
	if sampled > 0 {
		h := total / sampled
		g.Headroom = &h
	}
	return adapter.PoolReading{Groups: []adapter.GroupCapacity{g}, Complete: complete}, nil
}

// headroom is the percent left of the tighter of the account's five-hour and weekly windows. A window whose reset has
// passed is reset: its utilization no longer says what is left. ok is false when the account reported neither window;
// a recognized window that cannot be read is refused rather than read as spent.
func headroom(f authFile, now time.Time) (int, bool, error) {
	provider := strings.ToLower(strings.TrimSpace(f.Provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(f.Type))
	}
	switch provider {
	case "claude":
		return claudeHeadroom(f.Quota.Signals, f.Quota.ObservedAt, now)
	case "codex":
		return codexHeadroom(f.Quota.Signals, f.Quota.ObservedAt, now)
	default:
		return 0, false, nil
	}
}

func claudeHeadroom(signals map[string]string, observed, now time.Time) (int, bool, error) {
	var windows []window
	for _, w := range []struct {
		name, reset string
	}{
		{"anthropic-ratelimit-unified-5h-utilization", "anthropic-ratelimit-unified-5h-reset"},
		{"anthropic-ratelimit-unified-7d-utilization", "anthropic-ratelimit-unified-7d-reset"},
	} {
		v, ok := signal(signals, w.name)
		if !ok {
			continue
		}
		used, err := fractionPercent(v)
		if err != nil {
			return 0, false, err
		}
		reset, has, err := resetAt(signals, []string{w.reset}, observed)
		if err != nil {
			return 0, false, err
		}
		windows = append(windows, window{used: used, reset: reset, hasReset: has})
	}
	return remaining(windows, now)
}

func codexHeadroom(signals map[string]string, observed, now time.Time) (int, bool, error) {
	var windows []window
	for _, w := range []struct {
		name, reset, after string
	}{
		{"x-codex-primary-used-percent", "x-codex-primary-reset-at", "x-codex-primary-reset-after-seconds"},
		{"x-codex-secondary-used-percent", "x-codex-secondary-reset-at", "x-codex-secondary-reset-after-seconds"},
	} {
		v, ok := signal(signals, w.name)
		if !ok {
			continue
		}
		used, err := percent(v)
		if err != nil {
			return 0, false, err
		}
		reset, has, err := resetAt(signals, []string{w.reset, w.after}, observed)
		if err != nil {
			return 0, false, err
		}
		windows = append(windows, window{used: used, reset: reset, hasReset: has})
	}
	return remaining(windows, now)
}

type window struct {
	used     float64
	reset    time.Time
	hasReset bool
}

func remaining(windows []window, now time.Time) (int, bool, error) {
	left, seen := 100, false
	for _, w := range windows {
		if w.hasReset && !w.reset.After(now) {
			w.used = 0
		}
		if w.used < 0 || w.used > 100 {
			return 0, false, fmt.Errorf("%w: a window utilization outside 0 to 100", adapter.ErrUnrecognised)
		}
		left = min(left, max(0, int(100-w.used)))
		seen = true
	}
	return left, seen, nil
}

func signal(signals map[string]string, name string) (string, bool) {
	for key, value := range signals {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// fractionPercent reads Anthropic's 0 to 1 share.
func fractionPercent(v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: a utilization that is not a share from 0 to 1", adapter.ErrUnrecognised)
	}
	return f * 100, nil
}

// percent reads a 0 to 100 percentage.
func percent(v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 100 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: a utilization that is not a percentage from 0 to 100", adapter.ErrUnrecognised)
	}
	return f, nil
}

// resetAt reads the first reset time present. It accepts RFC3339 and Unix seconds; a reset-after-seconds member is
// relative to the observation. A member that is present but cannot be read is refused, because guessing a reset would
// make a full window look empty.
func resetAt(signals map[string]string, names []string, observed time.Time) (time.Time, bool, error) {
	for _, name := range names {
		v, ok := signal(signals, name)
		if !ok {
			continue
		}
		if strings.Contains(name, "reset-after-seconds") {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 || observed.IsZero() {
				return time.Time{}, false, fmt.Errorf("%w: a reset-after-seconds that is not a duration", adapter.ErrUnrecognised)
			}
			return observed.Add(time.Duration(n) * time.Second), true, nil
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC(), true, nil
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0).UTC(), true, nil
		}
		return time.Time{}, false, fmt.Errorf("%w: a reset time that is neither RFC3339 nor Unix seconds", adapter.ErrUnrecognised)
	}
	return time.Time{}, false, nil
}
