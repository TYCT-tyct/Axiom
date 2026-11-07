package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// MaxAlertsPerHour is the most alerts a credential may raise on a node in any hour: the default, and the ceiling the
// owner can only lower (docs/domains/operations.md「运营凭证」).
const MaxAlertsPerHour = 60

var (
	// ErrLimitsChanged is a change of the agent's limits from a revision that is no longer theirs.
	ErrLimitsChanged = errors.New("operations: the agent's limits changed since they were read")
	// ErrBadLimit is a limit outside 1 and its ceiling.
	ErrBadLimit = errors.New("operations: a limit is from 1 to its ceiling")
)

// AgentLimits are what the owner set on the agent; Rev is 0 while nothing was set, when the code's values apply.
type AgentLimits struct {
	AlertsPerHour int
	Rev           int64
	Updated       *time.Time
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
	if l.AlertsPerHour < 1 || l.AlertsPerHour > MaxAlertsPerHour {
		return AgentLimits{}, ErrBadLimit
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
	alerts := int32(l.AlertsPerHour)
	var rows []OperationsAgentLimit
	if len(found) == 0 {
		rows, err = q.InsertAgentLimits(ctx, tx, alerts)
	} else {
		var row OperationsAgentLimit
		row, err = q.UpdateAgentLimits(ctx, tx, alerts)
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

func limitsOf(rows []OperationsAgentLimit) AgentLimits {
	if len(rows) == 0 {
		return AgentLimits{AlertsPerHour: MaxAlertsPerHour}
	}
	r := rows[0]
	return AgentLimits{AlertsPerHour: int(r.AlertsPerHour), Rev: r.Rev, Updated: &r.UpdatedAt}
}

func (l AgentLimits) detail() map[string]any {
	return map[string]any{"alerts_per_hour": l.AlertsPerHour, "rev": l.Rev}
}
