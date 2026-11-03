package quality

import (
	"context"
	"errors"
	"fmt"
	"time"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// SourceClassInput is a new class. Position nil asks for one after the last non-fallback class; Rules nil means no
// conditions, a class that never matches.
type SourceClassInput struct {
	Slug        string
	Name        string
	NameZh      string
	Description string
	Position    *int
	Rules       *Rules
}

// SourceClassChanges edits one class. Nil fields keep their value. Rev is the revision the administrator read.
type SourceClassChanges struct {
	Name        *string
	NameZh      *string
	Description *string
	Position    *int
	Rules       *Rules
	Rev         int64
}

// ReadSourceClasses lists the live classes in evaluation order.
func ReadSourceClasses(ctx context.Context, tx pg.Tx) ([]SourceClass, error) {
	return querySourceClasses(ctx, tx, `WHERE deleted_at IS NULL ORDER BY position, slug`)
}

// querySourceClasses reads class rows matching where; where is a constant of this file, never caller input.
func querySourceClasses(ctx context.Context, tx pg.Tx, where string) ([]SourceClass, error) {
	rows, err := tx.Query(ctx, `SELECT slug,name,name_zh,description,position,fallback,system,rules,revision,
		created_at,updated_at FROM quality.source_classes `+where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceClass
	for rows.Next() {
		c, err := scanSourceClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanSourceClass(row scanner) (SourceClass, error) {
	var c SourceClass
	var rules []byte
	if err := row.Scan(&c.Slug, &c.Name, &c.NameZh, &c.Description, &c.Position, &c.Fallback, &c.System,
		&rules, &c.Revision, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return SourceClass{}, err
	}
	if err := jsonx.Unmarshal(rules, &c.Rules); err != nil {
		return SourceClass{}, fmt.Errorf("quality: decode the rules of %s: %w", c.Slug, err)
	}
	if c.Rules.Conditions == nil {
		c.Rules.Conditions = []Condition{}
	}
	return c, nil
}

func readSourceClass(ctx context.Context, tx pg.Tx, slug string) (SourceClass, error) {
	row := tx.QueryRow(ctx, `SELECT slug,name,name_zh,description,position,fallback,system,rules,revision,
		created_at,updated_at FROM quality.source_classes WHERE slug=$1 AND deleted_at IS NULL`, slug)
	c, err := scanSourceClass(row)
	if errors.Is(err, pg.ErrNoRows) {
		return SourceClass{}, ErrClassNotFound
	}
	if err != nil {
		return SourceClass{}, err
	}
	return c, nil
}

// CreateSourceClass writes a class and its audit entry in one transaction. Position, the global class count and the
// headers all classes may read are checked against the same locked table, so two administrators cannot create a
// duplicate position or a rule set the call path cannot hold.
//
//nolint:cyclop,gocognit // validation, ordering and the audit write are one transaction; each refusal names its field
func CreateSourceClass(ctx context.Context, tx pg.Tx, actor audit.Actor, in SourceClassInput, reason string) (SourceClass, error) {
	if !validSlug(in.Slug) {
		return SourceClass{}, classErr("slug", "must start with a lower-case letter, then lower-case letters, digits or hyphens, at most 32 characters")
	}
	rules := Rules{Match: "all"}
	if in.Rules != nil {
		rules = in.Rules.Normalized()
	}
	c := SourceClass{Slug: in.Slug, Name: in.Name, NameZh: in.NameZh, Description: in.Description, Rules: rules}
	if err := c.Check(); err != nil {
		return SourceClass{}, err
	}
	if err := lockClasses(ctx, tx); err != nil {
		return SourceClass{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM quality.source_classes WHERE slug=$1)`, in.Slug).Scan(&exists); err != nil {
		return SourceClass{}, err
	}
	if exists {
		return SourceClass{}, ErrClassExists
	}
	live, err := ReadSourceClasses(ctx, tx)
	if err != nil {
		return SourceClass{}, err
	}
	if len(live) >= MaxSourceClasses {
		return SourceClass{}, classErr("classes", fmt.Sprintf("at most %d classes are allowed", MaxSourceClasses))
	}
	if in.Position == nil {
		position, err := nextPosition(live)
		if err != nil {
			return SourceClass{}, err
		}
		c.Position = position
	} else {
		c.Position = *in.Position
	}
	next := append(live, c)
	if _, err := compileClasses(next); err != nil {
		return SourceClass{}, err
	}
	encoded, err := rulesJSON(c.Rules)
	if err != nil {
		return SourceClass{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO quality.source_classes (slug,name,name_zh,description,position,rules)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING revision,created_at,updated_at`,
		c.Slug, c.Name, c.NameZh, c.Description, c.Position, encoded).
		Scan(&c.Revision, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return SourceClass{}, fmt.Errorf("quality: create source class %s: %w", c.Slug, err)
	}
	if err := recordClass(ctx, tx, actor, "source_class.create", nil, &c, reason); err != nil {
		return SourceClass{}, err
	}
	return c, nil
}

// UpdateSourceClass edits a live class. The revision is the one the administrator read; a stale revision changes
// nothing. The fallback keeps its position and conditions: it is defined by matching nothing, not by a rule.
//
//nolint:cyclop,gocognit,funlen // applying optional fields and revalidating the set reads as one guarded body
func UpdateSourceClass(ctx context.Context, tx pg.Tx, actor audit.Actor, slug string, in SourceClassChanges, reason string) (SourceClass, error) {
	if err := lockClasses(ctx, tx); err != nil {
		return SourceClass{}, err
	}
	before, err := readSourceClass(ctx, tx, slug)
	if err != nil {
		return SourceClass{}, err
	}
	if in.Rev != before.Revision {
		return SourceClass{}, ErrClassStale
	}
	next := before
	if in.Name != nil {
		next.Name = *in.Name
	}
	if in.NameZh != nil {
		next.NameZh = *in.NameZh
	}
	if in.Description != nil {
		next.Description = *in.Description
	}
	if in.Position != nil {
		next.Position = *in.Position
	}
	if in.Rules != nil {
		next.Rules = in.Rules.Normalized()
	}
	if before.Fallback {
		if next.Position != before.Position {
			return SourceClass{}, classErr("position", "the fallback class keeps its position")
		}
		if in.Rules != nil {
			return SourceClass{}, classErr("rules", "the fallback class recognizes the endpoints no rule matches and has no rules")
		}
	}
	if err := next.Check(); err != nil {
		return SourceClass{}, err
	}
	live, err := ReadSourceClasses(ctx, tx)
	if err != nil {
		return SourceClass{}, err
	}
	for i := range live {
		if live[i].Slug == slug {
			live[i] = next
			break
		}
	}
	if _, err := compileClasses(live); err != nil {
		return SourceClass{}, err
	}
	encoded, err := rulesJSON(next.Rules)
	if err != nil {
		return SourceClass{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE quality.source_classes SET name=$2,name_zh=$3,description=$4,position=$5,
		rules=$6,revision=revision+1,updated_at=now() WHERE slug=$1 AND deleted_at IS NULL AND revision=$7
		RETURNING revision,updated_at`, slug, next.Name, next.NameZh, next.Description, next.Position,
		encoded, in.Rev).Scan(&next.Revision, &next.UpdatedAt); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return SourceClass{}, ErrClassStale
		}
		return SourceClass{}, fmt.Errorf("quality: update source class %s: %w", slug, err)
	}
	next.CreatedAt = before.CreatedAt
	if err := recordClass(ctx, tx, actor, "source_class.update", &before, &next, reason); err != nil {
		return SourceClass{}, err
	}
	return next, nil
}

// DeleteSourceClass retires a class: the row and slug stay, so a preset naming it keeps meaning the class that was
// retired rather than a later class that might reuse the name. Seeded classes cannot be retired.
func DeleteSourceClass(ctx context.Context, tx pg.Tx, actor audit.Actor, slug string, rev int64, reason string) error {
	if err := lockClasses(ctx, tx); err != nil {
		return err
	}
	before, err := readSourceClass(ctx, tx, slug)
	if err != nil {
		return err
	}
	if rev != before.Revision {
		return ErrClassStale
	}
	if before.System {
		return ErrClassSystem
	}
	if _, err := tx.Exec(ctx, `UPDATE quality.source_classes SET deleted_at=now(),revision=revision+1,
		updated_at=now() WHERE slug=$1 AND deleted_at IS NULL AND revision=$2`, slug, rev); err != nil {
		return fmt.Errorf("quality: retire source class %s: %w", slug, err)
	}
	return recordClass(ctx, tx, actor, "source_class.delete", &before, nil, reason)
}

func lockClasses(ctx context.Context, tx pg.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('quality.source_classes'))`)
	return err
}

// nextPosition leaves room between classes and before the fallback, whose seed is at 1000.
func nextPosition(live []SourceClass) (int, error) {
	maxPosition, fallback := 0, maxPositionForNew
	found := false
	for _, c := range live {
		if c.Fallback {
			fallback = c.Position
			continue
		}
		if !found || c.Position > maxPosition {
			maxPosition, found = c.Position, true
		}
	}
	next := maxPosition + 10
	if !found {
		next = 10
	}
	if next >= fallback {
		return 0, classErr("position", "there is no room before the fallback class; move a class or name a position")
	}
	return next, nil
}

const maxPositionForNew = 1_000_000

func rulesJSON(r Rules) ([]byte, error) {
	if r.Conditions == nil {
		r.Conditions = []Condition{}
	}
	data, err := jsonx.MarshalCanonical(r)
	if err != nil {
		return nil, fmt.Errorf("quality: encode the source class rules: %w", err)
	}
	return data, nil
}

func recordClass(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, before, after *SourceClass, reason string) error {
	id := ""
	switch {
	case after != nil:
		id = after.Slug
	case before != nil:
		id = before.Slug
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "quality", Kind: "source_class", ID: id},
		Detail: map[string]any{"before": classDetail(before), "after": classDetail(after)},
	})
}

func classDetail(c *SourceClass) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"slug": c.Slug, "name": c.Name, "name_zh": c.NameZh, "description": c.Description,
		"position": c.Position, "fallback": c.Fallback, "system": c.System,
		"rules": c.Rules, "revision": c.Revision,
	}
}

// LoadSourceClasses reads the table for a node's loop; a caller without a database keeps the relay fallback New installed.
func LoadSourceClasses(ctx context.Context, db *pg.DB, scope pg.Scope) ([]SourceClass, error) {
	var classes []SourceClass
	err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		var err error
		classes, err = ReadSourceClasses(ctx, tx)
		return err
	})
	return classes, err
}

// loadClasses reads the administrator's classes before the node serves traffic. The relay fallback New installed is
// used only until this read: a table that is missing or unusable fails the start rather than routing on stale data.
func (l *Loop) loadClasses(ctx context.Context, now time.Time) error {
	if l.db == nil {
		l.classesAt = now
		return nil
	}
	classes, err := LoadSourceClasses(ctx, l.db, l.scope)
	if err != nil {
		return fmt.Errorf("quality: read the source classes: %w", err)
	}
	if err := l.monitor.SetSourceClasses(classes); err != nil {
		return fmt.Errorf("quality: the saved source classes are not usable: %w", err)
	}
	l.classesAt = now
	return nil
}

// refreshClasses rereads the classes every classesEvery and reclassifies endpoints from their last successful
// response. A failed read or an unusable table keeps the previous rules and is logged: the node that saved them
// checked them, and a reload must not stop classification.
func (l *Loop) refreshClasses(ctx context.Context, now time.Time) {
	if l.db == nil || now.Sub(l.classesAt) < classesEvery {
		return
	}
	l.classesAt = now
	classes, err := LoadSourceClasses(ctx, l.db, l.scope)
	if err == nil {
		err = l.monitor.SetSourceClasses(classes)
	}
	if err != nil {
		l.logger.Error("quality: reload the source classes; the previous value stays in force", "err", err)
	}
}
