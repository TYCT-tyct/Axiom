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

// MapModel creates an alias when rev is nil, or corrects a group alias at the revision the caller read. formats declares
// call formats beyond the adapter's; nil leaves an existing declaration alone, an empty list clears it. The caller
// checks the catalog model; the supplier lock serializes revisions and their audit entries.
func (a *Admin) MapModel(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, group *uuid.UUID, name string, model uuid.UUID, formats *[]string, rev *int64, reason string) (Mapping, error) {
	if err := a.lockAlias(ctx, tx, supplier, group); err != nil {
		return Mapping{}, err
	}
	q := store.New()
	rows, err := q.CurrentAlias(ctx, tx, store.CurrentAliasParams{SupplierID: supplier, SupplierGroupID: group, UpstreamName: name})
	if err != nil {
		return Mapping{}, fmt.Errorf("read the alias: %w", err)
	}
	if err := checkMappingRevision(rows, group, rev); err != nil {
		return Mapping{}, err
	}
	declared, err := declaredFormats(formats)
	if err != nil {
		return Mapping{}, err
	}
	m := Mapping{Supplier: supplier, Group: group, UpstreamName: name, Model: model, Revision: 1}
	detail := map[string]any{"upstream_name": name, "model": model.String()}
	if group != nil {
		detail["group"] = group.String()
	}
	if len(rows) == 0 {
		return a.insertMapping(ctx, tx, q, actor, supplier, group, name, model, declared, formats, m, detail, reason)
	}
	return a.reviseMapping(ctx, tx, q, actor, supplier, model, declared, formats, rows[0], m, detail, reason)
}

// MapPending makes a group alias for a name the group lists as waiting for a model (what PendingMappings lists), to a
// model the group has no name for yet, and for nothing else: the result is a new endpoint and moves no traffic that
// already flows. An alias of a name the catalog recognised or a supplier alias covers would change what that name
// means; a second name for a model the group has would take its endpoint over, since the projection keeps one endpoint
// for a group's model and prefers an alias. Both checks run under the supplier lock that MapModel takes, so no alias
// can be made between them, and they count aliases the sync has not projected yet.
func (a *Admin) MapPending(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, name string, model uuid.UUID, reason string) (Mapping, error) {
	if err := a.lockAlias(ctx, tx, supplier, &group); err != nil {
		return Mapping{}, err
	}
	q := store.New()
	rows, err := q.PendingMappingsPage(ctx, tx, store.PendingMappingsPageParams{
		SupplierID: &supplier, GroupID: &group, UpstreamName: &name, PageSize: 1,
	})
	switch {
	case err != nil:
		return Mapping{}, fmt.Errorf("read the name: %w", err)
	case len(rows) == 0:
		return Mapping{}, ErrNotPending
	}
	taken, err := q.ModelTakenInGroup(ctx, tx, store.ModelTakenInGroupParams{SupplierID: supplier, GroupID: group, ModelID: model})
	switch {
	case err != nil:
		return Mapping{}, fmt.Errorf("read the group's names for the model: %w", err)
	case taken:
		return Mapping{}, ErrModelTaken
	}
	return a.MapModel(ctx, tx, actor, supplier, &group, name, model, nil, nil, reason)
}

// lockAlias serializes alias writes for a supplier and checks that the named group belongs to it.
func (a *Admin) lockAlias(ctx context.Context, tx pg.Tx, supplier uuid.UUID, group *uuid.UUID) error {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return err
	}
	if group == nil {
		return nil
	}
	_, err := a.group(ctx, tx, supplier, *group)
	return err
}

// declaredFormats canonicalizes the request's declaration: nil keeps an existing declaration, an empty list clears it,
// and a name this node does not know is refused before anything is written.
func declaredFormats(formats *[]string) ([]string, error) {
	if formats == nil {
		return nil, nil
	}
	parsed, err := store.ParseFormats(*formats)
	if err != nil {
		if errors.Is(err, store.ErrUnsupportedFormat) {
			return nil, ErrUnknownFormat
		}
		return nil, err
	}
	declared, err := store.FormatNames(parsed)
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil // the column holds either no declaration or at least one format
	}
	return declared, nil
}

// insertMapping writes a new alias and its audit entry.
func (a *Admin) insertMapping(ctx context.Context, tx pg.Tx, q *store.Queries, actor audit.Actor, supplier uuid.UUID, group *uuid.UUID, name string, model uuid.UUID, declared []string, formats *[]string, m Mapping, detail map[string]any, reason string) (Mapping, error) {
	var inserted []string
	if formats != nil {
		inserted = declared
	}
	id, err := q.InsertAlias(ctx, tx, store.InsertAliasParams{SupplierID: supplier, SupplierGroupID: group, UpstreamName: name, ModelID: model, Formats: inserted})
	if err != nil {
		return Mapping{}, fmt.Errorf("write the alias: %w", err)
	}
	m.ID, m.Formats = id, inserted
	return a.finishMapping(ctx, tx, q, actor, supplier, "supplier_alias.create", reason, formats, declared, detail, m)
}

// reviseMapping applies a formats-only change at the current revision, or appends a model revision and audits it.
func (a *Admin) reviseMapping(ctx context.Context, tx pg.Tx, q *store.Queries, actor audit.Actor, supplier uuid.UUID, model uuid.UUID, declared []string, formats *[]string, current store.CurrentAliasRow, m Mapping, detail map[string]any, reason string) (Mapping, error) {
	m.ID, m.Revision, m.Formats = current.ID, current.Revision, current.Formats
	changed := false
	if formats != nil && !slices.Equal(current.Formats, declared) {
		// A declaration is capability configuration, not the model mapping: it changes without a revision, but it
		// is still audited like one and raises the generation so every node reprojects it.
		if err := q.UpdateAliasFormats(ctx, tx, store.UpdateAliasFormatsParams{ID: m.ID, Formats: declared}); err != nil {
			return Mapping{}, fmt.Errorf("write the alias formats: %w", err)
		}
		detail["formats_from"], detail["formats_to"] = current.Formats, declared
		m.Formats, changed = declared, true
	}
	sameModel := current.ModelID == model
	if sameModel && !changed {
		return m, nil
	}
	if sameModel {
		return a.finishMapping(ctx, tx, q, actor, supplier, "supplier_alias.formats", reason, formats, declared, detail, m)
	}
	m.Revision++
	if err := q.InsertAliasRevision(ctx, tx, store.InsertAliasRevisionParams{
		AliasID: m.ID, SupplierID: supplier, Revision: m.Revision,
		PreviousModelID: current.ModelID, ModelID: model, Actor: actor.String(), Reason: reason,
	}); err != nil {
		return Mapping{}, fmt.Errorf("write the alias: %w", err)
	}
	detail["model_from"], detail["model_to"] = current.ModelID.String(), model.String()
	return a.finishMapping(ctx, tx, q, actor, supplier, "supplier_alias.revise", reason, formats, declared, detail, m)
}

// finishMapping records the audit entry and the config generation bump after the row is written.
func (a *Admin) finishMapping(ctx context.Context, tx pg.Tx, q *store.Queries, actor audit.Actor, supplier uuid.UUID, action, reason string, formats *[]string, declared []string, detail map[string]any, m Mapping) (Mapping, error) {
	if formats != nil {
		detail["formats"] = declared
	}
	if err := a.afterMapping(ctx, tx, q, actor, supplier, action, reason, detail, m); err != nil {
		return Mapping{}, err
	}
	return m, nil
}
