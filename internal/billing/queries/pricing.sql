-- The pricing rules of a site, in the platform scope.

-- name: ListPricingRules :many
SELECT id, rev, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints, updated_at
FROM billing.pricing_rules WHERE site_id = @site_id;

-- name: InsertPricingRule :one
INSERT INTO billing.pricing_rules (id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
VALUES (@id, @site_id, @enabled, @target_kind, @target_id, @audience_kind, @audience_id, @method, @constraints)
RETURNING updated_at;

-- name: LockPricingRule :many
-- The rule's turn: two changes of one rule are applied one after the other, the second against the first's rev.
SELECT id, rev, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints, updated_at
FROM billing.pricing_rules WHERE id = @id AND site_id = @site_id FOR UPDATE;

-- name: UpdatePricingRule :one
UPDATE billing.pricing_rules SET enabled = @enabled, target_kind = @target_kind, target_id = @target_id,
    audience_kind = @audience_kind, audience_id = @audience_id, method = @method, constraints = @constraints,
    rev = rev + 1, updated_at = now()
WHERE id = @id
RETURNING rev, updated_at;

-- name: InsertPricingRuleVersion :exec
INSERT INTO billing.pricing_rule_versions (rule_id, rev, body, actor, reason) VALUES (@rule_id, @rev, @body, @actor, @reason);
