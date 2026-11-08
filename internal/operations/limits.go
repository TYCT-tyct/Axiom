package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The most a credential may do on a node, each the default and the ceiling the owner can only lower
// (docs/domains/operations.md「运营凭证」): requests a minute, money writes in any minute and in any hour, and alerts in
// any hour. The table's checks repeat them.
const (
	MaxRequestsPerMinute    = 600
	MaxMoneyWritesPerMinute = 30
	MaxMoneyWritesPerHour   = 300
	MaxAlertsPerHour        = 60
)

var (
	// ErrLimitsChanged is a change of the agent's limits from a revision that is no longer theirs.
	ErrLimitsChanged = errors.New("operations: the agent's limits changed since they were read")
	// ErrBadLimit is a limit outside 1 and its ceiling.
	ErrBadLimit = errors.New("operations: a limit is from 1 to its ceiling")
)

// AgentLimits are what the owner set on the agent; Rev is 0 while nothing was set, when the code's values apply.
type AgentLimits struct {
	RequestsPerMinute    int
	MoneyWritesPerMinute int
	MoneyWritesPerHour   int
	AlertsPerHour        int
	Rev                  int64
	Updated              *time.Time
}

// ReadAgentLimits reads the agent's limits as they apply now.
func ReadAgentLimits(ctx context.Context, tx pg.Tx) (AgentLimits, error) {
	rows, err := New().AgentLimits(ctx, tx)
	if err != nil {
		return AgentLimits{}, fmt.Errorf("read the agent's limits: %w", err)
	}
	return limitsOf(rows), nil
}

// SetAgentLimits replaces the agent's limits as actor from the revision rev the caller read (0 when nothing was set)
// and records it; another revision is ErrLimitsChanged.
func SetAgentLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, l AgentLimits, rev int64, reason string) (AgentLimits, error) {
	params, err := l.row()
	if err != nil {
		return AgentLimits{}, err
	}
	q := New()
	found, err := q.LockAgentLimits(ctx, tx)
	if err != nil {
		return AgentLimits{}, fmt.Errorf("lock the agent's limits: %w", err)
	}
	before := limitsOf(found)
	if before.Rev != rev {
		return AgentLimits{}, ErrLimitsChanged
	}
	var rows []OperationsAgentLimit
	if len(found) == 0 {
		rows, err = q.InsertAgentLimits(ctx, tx, params)
	} else {
		var row OperationsAgentLimit
		row, err = q.UpdateAgentLimits(ctx, tx, UpdateAgentLimitsParams(params))
		rows = []OperationsAgentLimit{row}
	}
	if err != nil {
		return AgentLimits{}, fmt.Errorf("write the agent's limits: %w", err)
	}
	if len(rows) == 0 {
		return AgentLimits{}, ErrLimitsChanged
	}
	set := limitsOf(rows)
	return set, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "agent_limits.set", Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "agent_limits", ID: "platform"},
		Detail: map[string]any{"before": before.detail(), "after": set.detail()},
	})
}

// row is l as the table holds it; a limit outside 1 and its ceiling is ErrBadLimit.
func (l AgentLimits) row() (InsertAgentLimitsParams, error) {
	if l.RequestsPerMinute < 1 || l.RequestsPerMinute > MaxRequestsPerMinute ||
		l.MoneyWritesPerMinute < 1 || l.MoneyWritesPerMinute > MaxMoneyWritesPerMinute ||
		l.MoneyWritesPerHour < 1 || l.MoneyWritesPerHour > MaxMoneyWritesPerHour ||
		l.AlertsPerHour < 1 || l.AlertsPerHour > MaxAlertsPerHour {
		return InsertAgentLimitsParams{}, ErrBadLimit
	}
	requests, perMinute, perHour := int32(l.RequestsPerMinute), int32(l.MoneyWritesPerMinute), int32(l.MoneyWritesPerHour)
	return InsertAgentLimitsParams{
		AlertsPerHour: int32(l.AlertsPerHour), RequestsPerMinute: &requests, MoneyWritesPerMinute: &perMinute, MoneyWritesPerHour: &perHour,
	}, nil
}

// limitsOf is the limits the rows hold, a limit no row holds being its ceiling: before the first set, and those an old
// node's row lacks.
func limitsOf(rows []OperationsAgentLimit) AgentLimits {
	l := AgentLimits{
		RequestsPerMinute: MaxRequestsPerMinute, MoneyWritesPerMinute: MaxMoneyWritesPerMinute,
		MoneyWritesPerHour: MaxMoneyWritesPerHour, AlertsPerHour: MaxAlertsPerHour,
	}
	if len(rows) == 0 {
		return l
	}
	r := rows[0]
	l.AlertsPerHour, l.Rev, l.Updated = int(r.AlertsPerHour), r.Rev, &r.UpdatedAt
	for _, set := range []struct {
		to   *int
		from *int32
	}{{&l.RequestsPerMinute, r.RequestsPerMinute}, {&l.MoneyWritesPerMinute, r.MoneyWritesPerMinute}, {&l.MoneyWritesPerHour, r.MoneyWritesPerHour}} {
		if set.from != nil {
			*set.to = int(*set.from)
		}
	}
	return l
}

func (l AgentLimits) detail() map[string]any {
	return map[string]any{
		"requests_per_minute": l.RequestsPerMinute, "money_writes_per_minute": l.MoneyWritesPerMinute,
		"money_writes_per_hour": l.MoneyWritesPerHour, "alerts_per_hour": l.AlertsPerHour, "rev": l.Rev,
	}
}
