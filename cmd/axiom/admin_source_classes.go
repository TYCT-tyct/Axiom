package main

import (
	"context"
	"errors"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/quality"
)

func (a adminAPI) ListSourceClasses(ctx context.Context, _ adminapi.ListSourceClassesRequestObject) (adminapi.ListSourceClassesResponseObject, error) {
	var classes []quality.SourceClass
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		classes, err = quality.ReadSourceClasses(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	out := adminapi.SourceClassList{Classes: make([]adminapi.SourceClass, len(classes))}
	for i, c := range classes {
		out.Classes[i] = sourceClassView(c)
	}
	return adminapi.ListSourceClasses200JSONResponse(out), nil
}

func (a adminAPI) CreateSourceClass(ctx context.Context, r adminapi.CreateSourceClassRequestObject) (adminapi.CreateSourceClassResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	in := quality.SourceClassInput{
		Slug: r.Body.Slug, Name: r.Body.Name, Position: r.Body.Position,
		Rules: rulesIn(r.Body.Rules),
	}
	if r.Body.NameZh != nil {
		in.NameZh = *r.Body.NameZh
	}
	if r.Body.Description != nil {
		in.Description = *r.Body.Description
	}
	body, replayed, err := idempotently(ctx, a.db, "createSourceClass", r.Params.IdempotencyKey, []any{r.Body},
		func(tx pg.Tx) (adminapi.SourceClass, error) {
			c, err := quality.CreateSourceClass(ctx, tx, actor(ctx), in, r.Body.Reason)
			if err != nil {
				return adminapi.SourceClass{}, classProblem(err)
			}
			return sourceClassView(c), nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateSourceClass201JSONResponse{
		Body: body, Headers: adminapi.CreateSourceClass201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) UpdateSourceClass(ctx context.Context, r adminapi.UpdateSourceClassRequestObject) (adminapi.UpdateSourceClassResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	in := quality.SourceClassChanges{
		Name: r.Body.Name, NameZh: r.Body.NameZh, Description: r.Body.Description,
		Position: r.Body.Position, Rev: r.Body.Rev,
	}
	if r.Body.Rules != nil {
		in.Rules = rulesIn(*r.Body.Rules)
	}
	body, replayed, err := idempotently(ctx, a.db, "updateSourceClass", r.Params.IdempotencyKey, []any{r.Slug, r.Body},
		func(tx pg.Tx) (adminapi.SourceClass, error) {
			c, err := quality.UpdateSourceClass(ctx, tx, actor(ctx), r.Slug, in, r.Body.Reason)
			if err != nil {
				return adminapi.SourceClass{}, classProblem(err)
			}
			return sourceClassView(c), nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateSourceClass200JSONResponse{
		Body: body, Headers: adminapi.UpdateSourceClass200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func (a adminAPI) DeleteSourceClass(ctx context.Context, r adminapi.DeleteSourceClassRequestObject) (adminapi.DeleteSourceClassResponseObject, error) {
	if r.Body == nil {
		return nil, invalidField("/", "a body is required")
	}
	_, replayed, err := idempotently(ctx, a.db, "deleteSourceClass", r.Params.IdempotencyKey, []any{r.Slug, r.Body},
		func(tx pg.Tx) (struct{}, error) {
			if err := quality.DeleteSourceClass(ctx, tx, actor(ctx), r.Slug, r.Body.Rev, r.Body.Reason); err != nil {
				return struct{}{}, classProblem(err)
			}
			return struct{}{}, nil
		})
	if err != nil {
		return nil, err
	}
	return adminapi.DeleteSourceClass204Response{
		Headers: adminapi.DeleteSourceClass204ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

func sourceClassView(c quality.SourceClass) adminapi.SourceClass {
	out := adminapi.SourceClass{
		Slug: c.Slug, Name: c.Name, NameZh: c.NameZh, Description: c.Description, Position: c.Position,
		Fallback: c.Fallback, System: c.System, Revision: c.Revision, UpdatedAt: c.UpdatedAt,
		Rules: adminapi.SourceClassRules{
			Match:      adminapi.SourceClassRulesMatch(c.Rules.Match),
			Conditions: make([]adminapi.SourceClassCondition, len(c.Rules.Conditions)),
		},
	}
	for i, cond := range c.Rules.Conditions {
		row := adminapi.SourceClassCondition{
			Evidence: adminapi.SourceClassConditionEvidence(cond.Evidence),
			Operator: adminapi.SourceClassConditionOperator(cond.Operator),
		}
		if cond.Name != "" {
			row.Name = &cond.Name
		}
		if cond.Item != "" {
			row.Item = &cond.Item
		}
		if cond.Value != "" {
			row.Value = &cond.Value
		}
		out.Rules.Conditions[i] = row
	}
	return out
}

func rulesIn(r adminapi.SourceClassRules) *quality.Rules {
	out := quality.Rules{Match: string(r.Match), Conditions: make([]quality.Condition, len(r.Conditions))}
	for i, cond := range r.Conditions {
		out.Conditions[i] = quality.Condition{
			Evidence: string(cond.Evidence), Operator: string(cond.Operator),
		}
		if cond.Name != nil {
			out.Conditions[i].Name = *cond.Name
		}
		if cond.Item != nil {
			out.Conditions[i].Item = *cond.Item
		}
		if cond.Value != nil {
			out.Conditions[i].Value = *cond.Value
		}
	}
	return &out
}

func classProblem(err error) error {
	if e, ok := errors.AsType[*quality.ClassError](err); ok {
		field := e.Field
		if field != "" && field[0] != '/' {
			field = "/" + field
		}
		return invalidField(field, e.Reason)
	}
	return err
}
