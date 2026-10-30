package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// aliasName is the one shape an alias has: OpenRouter's ~author/family-latest. The family part has the same
// characters as a public standard ID's model part, so a vendor name with a dot or underscore still has an alias.
var aliasName = regexp.MustCompile(`^~[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}-latest$`)

// SaveAliasRequest points an alias at the concrete model it stands for, or moves it to another one.
type SaveAliasRequest struct {
	Op
	Name  string  `json:"name"`
	Model ModelID `json:"model"`
}

// SaveAlias appends a revision of the alias. The target must be listed and belong to the alias's author, so an alias
// never silently reaches another vendor's family and never points at a model a client cannot call by its standard ID.
// A model alias, not a prefix rule, so a new member of the family takes over only when an operator says it does.
func (c *Catalog) SaveAlias(ctx context.Context, r SaveAliasRequest) (Result, error) {
	norm := Normalize(r.Name)
	if len(norm) > maxNameLen || !aliasName.MatchString(norm) {
		return Result{}, fmt.Errorf("%w: an alias is ~author/family-latest, got %q", ErrInvalid, r.Name)
	}
	return c.apply(ctx, r.Op, "alias.save", func(tx pg.Tx) (outcome, error) {
		if err := New().LockAliases(ctx, tx); err != nil {
			return outcome{}, err
		}
		row, err := lockedModel(ctx, tx, r.Model)
		if err != nil {
			return outcome{}, err
		}
		if Status(row.Status) != StatusListed {
			return outcome{}, fmt.Errorf("%w: it is %s, not listed", ErrWrongStatus, row.Status)
		}
		author, _, _ := strings.Cut(strings.TrimPrefix(norm, "~"), "/")
		if row.Author != author {
			return outcome{}, fmt.Errorf("%w: alias %q belongs to %q, but the model belongs to %q", ErrInvalid, norm, author, row.Author)
		}
		standard := Normalize(strings.SplitN(row.Slug, "@", 2)[0])
		if freeName(standard) {
			return outcome{}, fmt.Errorf("%w: a :free entry is a concrete model, not a family's latest", ErrInvalid)
		}
		o := outcome{targetKind: "alias", subject: norm, changesSnapshot: true}
		prev, err := New().GetAlias(ctx, tx, norm)
		rev := int64(1)
		switch {
		case err == nil:
			o.before = map[string]any{"model": ModelID(prev.ModelID), "rev": prev.Rev}
			rev = prev.Rev + 1
		case !errors.Is(err, pg.ErrNoRows):
			return outcome{}, err
		}
		if err := New().InsertAlias(ctx, tx, InsertAliasParams{Name: norm, Rev: rev, ModelID: uuid.UUID(r.Model)}); err != nil {
			return outcome{}, err
		}
		o.after = map[string]any{"model": r.Model, "rev": rev}
		return o, nil
	})
}

// Alias is the current revision of one alias: the model a client's ~author/family-latest call reaches.
type Alias struct {
	Name  string    `json:"name"`
	Model ModelID   `json:"model"`
	Rev   int64     `json:"rev"`
	SetAt time.Time `json:"set_at"`
}

// Aliases lists the current alias revisions in name order.
func (c *Catalog) Aliases(ctx context.Context) ([]Alias, error) {
	var aliases []Alias
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) error {
		rows, err := New().ListAliases(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			aliases = append(aliases, Alias{Name: row.Name, Model: ModelID(row.ModelID), Rev: row.Rev, SetAt: row.SetAt})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catalog: list aliases: %w", err)
	}
	return aliases, nil
}

// freeName reports whether a normalized name is a :free entry: exactly one colon, before the suffix. The database's
// checks say the same, so a name this refuses cannot be stored by another path either.
func freeName(norm string) bool {
	return len(norm) > len(":free") && strings.Count(norm, ":") == 1 && strings.HasSuffix(norm, ":free")
}

// zeroPrice reports whether every price in the schedule is zero, including prices for items the spec does not declare:
// a :free entry must not charge for anything.
func zeroPrice(s Schedule) bool {
	for st := range numServiceTiers {
		for it := range NumItems {
			for band := range len(s.above) + 1 {
				if p, ok := s.price(st, band, it); ok && p.String() != "0" {
					return false
				}
			}
		}
	}
	return true
}
