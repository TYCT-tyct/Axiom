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
		if err := checkAliasTarget(norm, row); err != nil {
			return outcome{}, err
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

// checkAliasTarget holds an alias's target to the alias's author: a listed concrete model of that author, never another
// vendor's family and never a :free entry, which is a model of its own rather than a family's latest.
func checkAliasTarget(norm string, row CatalogModel) error {
	if Status(row.Status) != StatusListed {
		return fmt.Errorf("%w: it is %s, not listed", ErrWrongStatus, row.Status)
	}
	author, _, _ := strings.Cut(strings.TrimPrefix(norm, "~"), "/")
	if row.Author != author {
		return fmt.Errorf("%w: alias %q belongs to %q, but the model belongs to %q", ErrInvalid, norm, author, row.Author)
	}
	if freeName(Normalize(strings.SplitN(row.Slug, "@", 2)[0])) {
		return fmt.Errorf("%w: a :free entry is a concrete model, not a family's latest", ErrInvalid)
	}
	return nil
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

// freeSlug reports whether a model's slug is a :free entry, with or without its @version.
func freeSlug(slug string) bool {
	return freeName(Normalize(strings.SplitN(slug, "@", 2)[0]))
}

// Free reports whether the model is a :free entry, whose sale price must be zero for every audience.
func (m Model) Free() bool { return freeSlug(m.Slug) }
