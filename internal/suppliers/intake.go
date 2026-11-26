package suppliers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// A supplier's intake rule (docs/domains/suppliers.md「接入规则」): which of its upstream names the sync's projection makes
// endpoints of. The projection reads it at every commit that projects a listing (persist.intakeRule).

// The ways a rule is refused.
var (
	// ErrIntakeRule is a rule whose patterns do not fit its mode: `names` needs include, and only `names` takes patterns.
	ErrIntakeRule = errors.New("suppliers: names takes include and exclude patterns, and only names does")
	// ErrIntakeRevision is a rule decided against a revision that is no longer current.
	ErrIntakeRevision = errors.New("suppliers: the intake rule changed since it was read")
	// ErrNotSynced is a sync or an intake rule asked of a supplier the sync never runs for: a direct API's or a cloud
	// supplier's models come from its keys when it is connected, and nothing would ever apply the change.
	ErrNotSynced = errors.New("suppliers: the sync does not run for a direct API or a cloud supplier")
)

// IntakeRule is a supplier's rule. Rev 0 is the default rule `listed`, never written.
type IntakeRule struct {
	Mode             string
	Include, Exclude []string
	Rev              int64
}

// defaultIntakeRule is the rule of a supplier that has none stored: a name of a model on sale, as before rules existed.
func defaultIntakeRule() IntakeRule {
	return IntakeRule{Mode: "listed", Include: []string{}, Exclude: []string{}}
}

// checked is the rule if its patterns fit its mode, with no pattern lists that are nil.
func (r IntakeRule) checked() (IntakeRule, error) {
	if (r.Mode == "names") != (len(r.Include) > 0) || (r.Mode != "names" && len(r.Exclude) > 0) {
		return IntakeRule{}, ErrIntakeRule
	}
	if r.Include == nil {
		r.Include = []string{}
	}
	if r.Exclude == nil {
		r.Exclude = []string{}
	}
	return r, nil
}

// lockedIntakeRule is the supplier's rule, the default when none is stored, held for a change.
func lockedIntakeRule(ctx context.Context, tx pg.Tx, q *store.Queries, supplier uuid.UUID) (IntakeRule, error) {
	rows, err := q.LockIntakeRule(ctx, tx, supplier)
	if err != nil {
		return IntakeRule{}, fmt.Errorf("read the intake rule: %w", err)
	}
	if len(rows) == 0 {
		return defaultIntakeRule(), nil
	}
	return IntakeRule{Mode: rows[0].Mode, Include: rows[0].Include, Exclude: rows[0].Exclude, Rev: rows[0].Rev}, nil
}

// synced is nil for a supplier the sync runs for and ErrNotSynced for any other; the supplier exists.
func synced(ctx context.Context, tx pg.Tx, supplier uuid.UUID) error {
	relay, err := store.New().SupplierSynced(ctx, tx, supplier)
	switch {
	case err != nil:
		return fmt.Errorf("read the supplier's mode: %w", err)
	case !relay:
		return ErrNotSynced
	}
	return nil
}

// IntakeRuleOf reads the supplier's rule, the default when none is stored.
func (*Admin) IntakeRuleOf(ctx context.Context, tx pg.Tx, supplier uuid.UUID) (IntakeRule, error) {
	rows, err := store.New().GetIntakeRule(ctx, tx, supplier)
	if err != nil {
		return IntakeRule{}, fmt.Errorf("read the intake rule: %w", err)
	}
	if len(rows) == 0 {
		return defaultIntakeRule(), nil
	}
	return IntakeRule{Mode: rows[0].Mode, Include: rows[0].Include, Exclude: rows[0].Exclude, Rev: rows[0].Rev}, nil
}

// SetIntakeRule replaces the supplier's rule, decided against rev. A rule as it is changes nothing and records nothing.
// It applies from the projection of the next sync; an endpoint the rule no longer takes then leaves routing. A supplier
// the sync does not run for is ErrNotSynced.
func (a *Admin) SetIntakeRule(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, rule IntakeRule, reason string) (IntakeRule, error) {
	rule, err := rule.checked()
	if err != nil {
		return IntakeRule{}, err
	}
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return IntakeRule{}, err
	}
	if err := synced(ctx, tx, supplier); err != nil {
		return IntakeRule{}, err
	}
	q := store.New()
	current, err := lockedIntakeRule(ctx, tx, q, supplier)
	if err != nil {
		return IntakeRule{}, err
	}
	if rule.Rev != current.Rev {
		return IntakeRule{}, ErrIntakeRevision
	}
	if rule.Mode == current.Mode && slices.Equal(rule.Include, current.Include) && slices.Equal(rule.Exclude, current.Exclude) {
		return current, nil
	}
	rule.Rev = current.Rev + 1
	if err := q.PutIntakeRule(ctx, tx, store.PutIntakeRuleParams{
		SupplierID: supplier, Mode: rule.Mode, Include: rule.Include, Exclude: rule.Exclude, Rev: rule.Rev,
	}); err != nil {
		return IntakeRule{}, fmt.Errorf("write the intake rule: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.intake_rule", supplier, reason, map[string]any{
		"from": current.Mode, "to": rule.Mode, "include": rule.Include, "exclude": rule.Exclude, "rev": rule.Rev,
	}); err != nil {
		return IntakeRule{}, err
	}
	return rule, nil
}
