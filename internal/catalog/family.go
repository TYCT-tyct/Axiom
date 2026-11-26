package catalog

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"axiom/internal/platform/pg"
)

// family is what a family may be called: the contract's ModelFamily and the column's CHECK.
var family = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// FamilyRequest puts a model in a family, or takes it out with a nil Family.
type FamilyRequest struct {
	Op
	Model  ModelID `json:"model"`
	Rev    int64   `json:"rev"`
	Family *string `json:"family"`
}

// SetFamily sets the model's family as of the rev the request was decided on. The family is a label for people and
// nothing reads it to resolve a name, so it changes no snapshot and is allowed in every status.
func (c *Catalog) SetFamily(ctx context.Context, r FamilyRequest) (Result, error) {
	if r.Family != nil && !family.MatchString(*r.Family) {
		return Result{}, fmt.Errorf("%w: %q is not a family", ErrInvalid, *r.Family)
	}
	return c.apply(ctx, r.Op, "model.set_family", func(tx pg.Tx) (outcome, error) {
		row, err := lockReviewed(ctx, tx, r.Model, r.Rev)
		if err != nil {
			return outcome{}, err
		}
		o := outcome{targetKind: "model", subject: r.Model.String(), before: map[string]any{"family": row.Family, "rev": row.Rev}}
		if err := New().SetModelFamily(ctx, tx, SetModelFamilyParams{ID: row.ID, Family: r.Family}); err != nil {
			return outcome{}, err
		}
		o.after = map[string]any{"family": r.Family, "rev": row.Rev + 1}
		return o, nil
	})
}

// familyOf is the family an imported model starts in, guessed from its name: the name without its author, a trailing
// date, -latest or :free, when that is a family's form. It is only where a person starts from (FamilyRequest corrects
// it); nothing is resolved by it.
func familyOf(name string) *string {
	name = strings.ToLower(name[strings.LastIndexByte(name, '/')+1:])
	name = strings.TrimSuffix(strings.TrimSuffix(name, ":free"), "-latest")
	for _, date := range []string{"-dddd-dd-dd", "-dddddddd", "-dd-dd", "-dddd"} { // the forms hasDate knows
		if endsLike(name, date) {
			name = name[:len(name)-len(date)]
			break
		}
	}
	if !family.MatchString(name) {
		return nil
	}
	return &name
}

// endsLike reports whether s ends in the form, whose 'd' is any digit and whose other bytes are themselves.
func endsLike(s, form string) bool {
	if len(s) < len(form) {
		return false
	}
	tail := s[len(s)-len(form):]
	for i := range len(form) {
		if c := tail[i]; form[i] == 'd' && (c < '0' || c > '9') || form[i] != 'd' && c != form[i] {
			return false
		}
	}
	return true
}
