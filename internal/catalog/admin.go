package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The admin operations run in the platform scope. Each is one transaction that locks the model it changes, checks its
// preconditions against the locked row, writes, records the change in catalog.change and in the audit log, and raises
// the revision when the write changes what a snapshot holds (README.md「管理操作」).

// The ways an admin operation is refused.
var (
	ErrNoOpKey       = errors.New("op_key is required and must not be the zero UUID")
	ErrNameLength    = fmt.Errorf("a name is 1 to %d bytes", maxNameLen)
	ErrReason        = errors.New("a reason of 1 to 1000 characters is required")
	ErrOpKeyReused   = errors.New("op_key was already used by another operation")
	ErrAuthorMissing = errors.New("the author does not exist")
	ErrNameTaken     = errors.New("the name, normalized, is already taken")
	ErrStaleRev      = errors.New("the model changed since it was reviewed")
	ErrWrongStatus   = errors.New("the model's status does not allow this")
	ErrNotReady      = errors.New("the model is not ready to be listed")
	// ErrInvalid is a request whose spec or schedule is not one the catalog can keep: a rule of its structure, which the
	// request's schema cannot state.
	ErrInvalid = errors.New("the spec or the schedule breaks a rule of the catalog")
)

// Op is what every admin operation carries: who does it, which attempt it is, and why. Key identifies the intended
// operation, not the attempt: a retry with the key of one that went through gets that one's result.
type Op struct {
	Actor  audit.Actor `json:"-"`
	Key    uuid.UUID   `json:"op_key"`
	Reason string      `json:"reason"`
	// Tx, when set, is the transaction the operation runs in, the caller's own (the admin API claims its idempotency
	// key in it), instead of one of its own. The caller refreshes the snapshot after it commits.
	Tx *pg.Tx `json:"-"`
}

// SetActor names who does the operation, for a request read from a file.
func (o *Op) SetActor(a audit.Actor) { o.Actor = a }

// Result says what an operation changed. Replayed is set when Key had already been applied: nothing was done again,
// and Change and Subject are those of the first run. Subject is the model's id, or the author's slug.
type Result struct {
	Change   uuid.UUID
	Subject  string
	Replayed bool
}

// outcome is what an operation's body reports for the history.
type outcome struct {
	targetKind      string // the audit log's target kind: model or author
	subject         string
	before, after   any
	evidence        any
	changesSnapshot bool // raises the revision
}

func (c *Catalog) apply(ctx context.Context, op Op, kind string, do func(tx pg.Tx) (outcome, error)) (Result, error) {
	if n := utf8.RuneCountInString(op.Reason); n == 0 || n > 1000 {
		return Result{}, ErrReason
	}
	if op.Key == (uuid.UUID{}) {
		return Result{}, ErrNoOpKey
	}
	if op.Tx != nil {
		// The caller's idempotency claim already answers its own retries, so a key this one finds applied was used
		// elsewhere (the command line): replaying it would answer for another actor, target or content.
		res, _, err := once(ctx, *op.Tx, op, kind, do)
		if err == nil && res.Replayed {
			return Result{}, ErrOpKeyReused
		}
		return res, err
	}
	var res Result
	var raised bool
	err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) (err error) {
		res, raised, err = once(ctx, tx, op, kind, do)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	if raised {
		c.refreshAfterWrite(ctx)
	}
	return res, nil
}

// once runs the operation unless its key was applied before, in which case it returns that run's result. It reports
// whether the revision was raised.
func once(ctx context.Context, tx pg.Tx, op Op, kind string, do func(tx pg.Tx) (outcome, error)) (Result, bool, error) {
	first, err := New().GetChangeByOpKey(ctx, tx, op.Key)
	switch {
	case err == nil && first.Kind != kind:
		return Result{}, false, ErrOpKeyReused
	case err == nil:
		return Result{Change: first.ID, Subject: first.Subject, Replayed: true}, false, nil
	case !errors.Is(err, pg.ErrNoRows):
		return Result{}, false, err
	}
	o, err := do(tx)
	if err != nil {
		return Result{}, false, fmt.Errorf("%s: %w", kind, err)
	}
	id, err := record(ctx, tx, op, kind, o)
	if err != nil {
		return Result{}, false, err
	}
	if o.changesSnapshot {
		err = New().BumpRevision(ctx, tx)
	}
	return Result{Change: id, Subject: o.subject}, o.changesSnapshot, err
}

// refreshAfterWrite rebuilds this node's snapshot at once. Its failure is not the operation's: the write is done, and
// the next poll tries again.
func (c *Catalog) refreshAfterWrite(ctx context.Context) {
	if err := c.Refresh(ctx); err != nil {
		c.cfg.Logger.Warn("catalog refresh after a write failed", "err", err)
	}
}

// record writes the change and its audit entry, which fails the operation if it cannot be written.
func record(ctx context.Context, tx pg.Tx, op Op, kind string, o outcome) (uuid.UUID, error) {
	var encoded [3][]byte
	for i, v := range []any{o.before, o.after, o.evidence} {
		if v == nil {
			continue
		}
		b, err := jsonx.MarshalCanonical(v)
		if err != nil {
			return uuid.UUID{}, fmt.Errorf("%s: encode the change: %w", kind, err)
		}
		encoded[i] = b
	}
	id, err := New().InsertChange(ctx, tx, InsertChangeParams{
		OpKey: op.Key, Actor: op.Actor.String(), Reason: op.Reason, Kind: kind, Subject: o.subject,
		Before: encoded[0], After: encoded[1], Evidence: encoded[2],
	})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%s: record the change: %w", kind, err)
	}
	err = audit.Record(ctx, tx, audit.Event{
		Actor: op.Actor, Action: kind, Target: audit.Target{Module: "catalog", Kind: o.targetKind, ID: o.subject},
		Reason: op.Reason, Detail: map[string]any{"change_id": id.String()},
	})
	return id, err
}

// lockedModel locks the model's row, which makes operations on one model take turns.
func lockedModel(ctx context.Context, tx pg.Tx, id ModelID) (CatalogModel, error) {
	row, err := New().LockModel(ctx, tx, uuid.UUID(id))
	if errors.Is(err, pg.ErrNoRows) {
		return row, ErrModelNotFound
	}
	return row, err
}

// lockReviewed is lockModel for an operation decided on the model as of rev: a model changed since is refused, so two
// edits made from one reading cannot overwrite each other.
func lockReviewed(ctx context.Context, tx pg.Tx, id ModelID, rev int64) (CatalogModel, error) {
	row, err := lockedModel(ctx, tx, id)
	if err == nil && row.Rev != rev {
		err = fmt.Errorf("%w: it is at rev %d, the request is for %d", ErrStaleRev, row.Rev, rev)
	}
	return row, err
}

// saveModel writes the row's status, price source and spec, and returns it as stored, with the rev raised.
func saveModel(ctx context.Context, tx pg.Tx, row CatalogModel) (CatalogModel, error) {
	err := New().UpdateModel(ctx, tx, UpdateModelParams{ID: row.ID, Status: row.Status, PriceSource: row.PriceSource, Spec: row.Spec})
	row.Rev++
	return row, err
}

func summary(row CatalogModel) map[string]any {
	return map[string]any{"status": row.Status, "price_source": row.PriceSource, "spec": jsonx.Raw(row.Spec), "rev": row.Rev}
}

// SaveAuthorRequest creates an author or renames it.
type SaveAuthorRequest struct {
	Op
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// SaveAuthor creates the author, or changes its display name.
func (c *Catalog) SaveAuthor(ctx context.Context, r SaveAuthorRequest) (Result, error) {
	return c.apply(ctx, r.Op, "author.save", func(tx pg.Tx) (outcome, error) {
		o := outcome{targetKind: "author", subject: r.Slug, after: map[string]any{"name": r.Name}}
		prev, err := New().GetAuthor(ctx, tx, r.Slug)
		switch {
		case errors.Is(err, pg.ErrNoRows):
			err = New().InsertAuthor(ctx, tx, InsertAuthorParams{Slug: r.Slug, Name: r.Name})
		case err == nil:
			o.before = map[string]any{"name": prev.Name}
			err = New().RenameAuthor(ctx, tx, RenameAuthorParams{Slug: r.Slug, Name: r.Name})
		}
		return o, err
	})
}

// CreateModelRequest drafts a model. The author is the slug's first part and must exist.
type CreateModelRequest struct {
	Op
	Slug         string       `json:"slug"`
	VersionBasis VersionBasis `json:"version_basis"`
	PriceSource  PriceSource  `json:"price_source"`
	Spec         jsonx.Raw    `json:"spec"`
}

// CreateModel adds a draft model, visible only to the platform until it is listed.
func (c *Catalog) CreateModel(ctx context.Context, r CreateModelRequest) (Result, error) {
	if _, err := parseSavedSpec(r.Spec); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c.apply(ctx, r.Op, "model.create", func(tx pg.Tx) (outcome, error) {
		author, _, _ := strings.Cut(r.Slug, "/")
		if _, err := New().GetAuthor(ctx, tx, author); errors.Is(err, pg.ErrNoRows) {
			return outcome{}, fmt.Errorf("%w: %q", ErrAuthorMissing, author)
		} else if err != nil {
			return outcome{}, err
		}
		row, err := New().InsertModel(ctx, tx, InsertModelParams{
			Author: author, Slug: r.Slug, VersionBasis: string(r.VersionBasis), PriceSource: string(r.PriceSource), Spec: r.Spec,
		})
		return outcome{targetKind: "model", subject: ModelID(row.ID).String(), after: summary(row)}, err
	})
}

// AddNameRequest gives a model another name.
type AddNameRequest struct {
	Op
	Model      ModelID    `json:"model"`
	Name       string     `json:"name"`
	Visibility Visibility `json:"visibility"`
}

// AddName attaches a name, written as the vendor writes it. Names are never changed or removed, and normalized they
// are unique across all models, so a name never starts to mean another model.
func (c *Catalog) AddName(ctx context.Context, r AddNameRequest) (Result, error) {
	return c.apply(ctx, r.Op, "name.add", func(tx pg.Tx) (outcome, error) {
		row, err := lockedModel(ctx, tx, r.Model)
		if err != nil {
			return outcome{}, err
		}
		if Status(row.Status) == StatusRetired {
			return outcome{}, fmt.Errorf("%w: it is retired", ErrWrongStatus)
		}
		if n := len(r.Name); n == 0 || n > maxNameLen {
			return outcome{}, ErrNameLength
		}
		norm := Normalize(r.Name)
		if strings.Contains(norm, ":") && !freeName(norm) {
			return outcome{}, fmt.Errorf("%w: a name may end in :free, and has no other colon in it", ErrInvalid)
		}
		standard := Normalize(strings.SplitN(row.Slug, "@", 2)[0])
		if r.Visibility == VisibilityPublic {
			switch {
			case freeName(norm) && norm != standard:
				return outcome{}, fmt.Errorf("%w: a :free name must be its model's standard ID %q", ErrInvalid, standard)
			case !freeName(norm) && freeName(standard):
				return outcome{}, fmt.Errorf("%w: a :free model's public names must end in :free", ErrInvalid)
			}
		}
		if taken, err := New().GetNameByNorm(ctx, tx, norm); err == nil {
			return outcome{}, fmt.Errorf("%w: %q is %q", ErrNameTaken, norm, taken.Name)
		} else if !errors.Is(err, pg.ErrNoRows) {
			return outcome{}, err
		}
		err = New().InsertName(ctx, tx, InsertNameParams{
			Name: r.Name, Norm: norm, Visibility: string(r.Visibility), ModelID: row.ID,
		})
		return outcome{
			targetKind: "model", subject: r.Model.String(), changesSnapshot: Status(row.Status) == StatusListed,
			after: map[string]any{"name": r.Name, "norm": norm, "visibility": r.Visibility},
		}, err
	})
}

// UpdateSpecRequest replaces a model's baseline.
type UpdateSpecRequest struct {
	Op
	Model ModelID   `json:"model"`
	Rev   int64     `json:"rev"`
	Spec  jsonx.Raw `json:"spec"`
}

// UpdateSpec replaces the spec as of the rev the request was decided on. The old spec stays in the change. A listed
// model's new spec must still be priced in full by every current reference price.
func (c *Catalog) UpdateSpec(ctx context.Context, r UpdateSpecRequest) (Result, error) {
	spec, err := parseSavedSpec(r.Spec)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c.apply(ctx, r.Op, "model.update_spec", func(tx pg.Tx) (outcome, error) {
		row, err := lockReviewed(ctx, tx, r.Model, r.Rev)
		if err != nil {
			return outcome{}, err
		}
		if Status(row.Status) == StatusRetired {
			return outcome{}, fmt.Errorf("%w: it is retired", ErrWrongStatus)
		}
		if Status(row.Status) == StatusListed {
			if err := requireDeclared(spec); err != nil {
				return outcome{}, fmt.Errorf("%w: %w", ErrNotReady, err)
			}
			if err := requirePricedInFull(ctx, tx, row.ID, spec); err != nil {
				return outcome{}, err
			}
		}
		o := outcome{targetKind: "model", subject: r.Model.String(), before: summary(row), changesSnapshot: Status(row.Status) == StatusListed}
		row.Spec = r.Spec
		saved, err := saveModel(ctx, tx, row)
		o.after = summary(saved)
		return o, err
	})
}

// ModelRequest is the common shape of the operations that act on a model at a rev: list it, retire it.
type ModelRequest struct {
	Op
	Model ModelID `json:"model"`
	Rev   int64   `json:"rev"`
}

// ListModel makes a draft model callable. The review is the gate: the model declares what an upstream may report, it
// has its standard ID as a public name, it has a reference price, and every current reference price covers
// everything it declares (Schedule.Missing). Billing's own gate on sale prices comes on top.
func (c *Catalog) ListModel(ctx context.Context, r ModelRequest) (Result, error) {
	return c.apply(ctx, r.Op, "model.list", func(tx pg.Tx) (outcome, error) {
		row, err := lockReviewed(ctx, tx, r.Model, r.Rev)
		if err != nil {
			return outcome{}, err
		}
		if Status(row.Status) != StatusDraft {
			return outcome{}, fmt.Errorf("%w: it is %s, not draft", ErrWrongStatus, row.Status)
		}
		if err := requireListable(ctx, tx, row); err != nil {
			return outcome{}, fmt.Errorf("%w: %w", ErrNotReady, err)
		}
		o := outcome{targetKind: "model", subject: r.Model.String(), before: summary(row), changesSnapshot: true}
		row.Status = string(StatusListed)
		saved, err := saveModel(ctx, tx, row)
		o.after = summary(saved)
		return o, err
	})
}

// Retire takes a listed model out of use: its names answer "retired" from then on, and are never reused.
func (c *Catalog) Retire(ctx context.Context, r ModelRequest) (Result, error) {
	return c.apply(ctx, r.Op, "model.retire", func(tx pg.Tx) (outcome, error) {
		row, err := lockReviewed(ctx, tx, r.Model, r.Rev)
		if err != nil {
			return outcome{}, err
		}
		if Status(row.Status) != StatusListed {
			return outcome{}, fmt.Errorf("%w: it is %s, not listed", ErrWrongStatus, row.Status)
		}
		o := outcome{targetKind: "model", subject: r.Model.String(), before: summary(row), changesSnapshot: true}
		row.Status = string(StatusRetired)
		saved, err := saveModel(ctx, tx, row)
		o.after = summary(saved)
		return o, err
	})
}

// requireListable checks the parts of the listing gate that are not about prices, then the prices.
func requireListable(ctx context.Context, tx pg.Tx, row CatalogModel) error {
	spec, err := parseSpec(row.Spec)
	if err != nil {
		return err
	}
	if err := requireDeclared(spec); err != nil {
		return err
	}
	standard := Normalize(strings.SplitN(row.Slug, "@", 2)[0])
	names, err := New().ListNamesOfModel(ctx, tx, row.ID)
	if err != nil {
		return err
	}
	if !hasName(names, standard) {
		return fmt.Errorf("it has no public name for its standard ID %q", standard)
	}
	prices, err := currentPrices(ctx, tx, row.ID)
	if err != nil {
		return err
	}
	if len(prices.priced()) == 0 {
		return errors.New("it has no reference price")
	}
	if freeName(standard) {
		if err := requireFreeBase(ctx, tx, standard, prices); err != nil {
			return err
		}
	}
	return prices.cover(spec.reportable)
}

// requireFreeBase holds a :free entry to the base it names and to a zero price in every currency: a paid price behind
// a free name is the one thing the suffix promises not to do. The base must be public and listed, because the suffix
// only means something if a client can call the model it belongs to.
func requireFreeBase(ctx context.Context, tx pg.Tx, standard string, prices prices) error {
	for _, currency := range prices.priced() {
		if !zeroPrice(*prices[currency].schedule) {
			return fmt.Errorf("a :free entry must have a zero price in every currency, but %s is not zero", currency)
		}
	}
	base := strings.TrimSuffix(standard, ":free")
	name, err := New().GetNameByNorm(ctx, tx, base)
	switch {
	case errors.Is(err, pg.ErrNoRows):
		return fmt.Errorf("its base model %q has no public name", base)
	case err != nil:
		return err
	case Visibility(name.Visibility) != VisibilityPublic:
		return fmt.Errorf("its base model %q has no public name", base)
	}
	model, err := New().GetModel(ctx, tx, name.ModelID)
	switch {
	case errors.Is(err, pg.ErrNoRows):
		return fmt.Errorf("its base model %q does not exist", base)
	case err != nil:
		return err
	case Status(model.Status) != StatusListed:
		return fmt.Errorf("its base model %q is %s, not listed", base, model.Status)
	}
	return nil
}

// requireDeclared refuses a spec that leaves out what the upstream may report: absent or null reads as nothing
// declared, which would make every later coverage check pass vacuously. An explicit empty list is a declaration.
func requireDeclared(s spec) error {
	if !s.declared {
		return errors.New(`the spec must declare "items" and "tiers", even if empty: what the upstream may report`)
	}
	return nil
}

func hasName(names []CatalogName, norm string) bool {
	for _, n := range names {
		if Visibility(n.Visibility) == VisibilityPublic && n.Norm == norm {
			return true
		}
	}
	return false
}

// requirePricedInFull refuses a spec that declares what a current reference price of the model cannot price.
func requirePricedInFull(ctx context.Context, tx pg.Tx, id uuid.UUID, spec spec) error {
	prices, err := currentPrices(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := prices.cover(spec.reportable); err != nil {
		return fmt.Errorf("%w: %w", ErrNotReady, err)
	}
	return nil
}
