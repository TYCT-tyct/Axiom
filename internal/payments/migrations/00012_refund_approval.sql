-- +goose Up

-- A refund above the site's positive limit needs two operators. The limit is site data and starts at 0 (no limit), so
-- turning the rule on is an explicit operator action and no request frozen before that changes who may release it. The
-- first approval is kept apart from approved_by so the release stays the single field older nodes read.
ALTER TABLE payments.settings ADD COLUMN refund_second_approval_credits bigint DEFAULT 0
    CHECK (refund_second_approval_credits >= 0);
ALTER TABLE payments.refunds
    ADD COLUMN second_required boolean,
    ADD COLUMN first_approved_by uuid;

-- Old nodes insert a refund without the new column. The column has no default, so the trigger can tell that omission
-- from a new node's explicit snapshot and applies the site's current limit before the old node can approve the row.
-- +goose StatementBegin
CREATE FUNCTION payments.refund_second_required() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    limit_credits bigint;
BEGIN
    IF NEW.second_required IS NULL THEN
        SELECT COALESCE(s.refund_second_approval_credits, 0)
        INTO limit_credits
        FROM payments.settings AS s
        WHERE s.site_id = NEW.site_id;
        NEW.second_required := limit_credits > 0 AND NEW.credits > limit_credits;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER refunds_second_required BEFORE INSERT ON payments.refunds
    FOR EACH ROW EXECUTE FUNCTION payments.refund_second_required();

-- Every request frozen before this release keeps the one-approver rule it was frozen under; a limit set later applies
-- to new requests.
UPDATE payments.refunds SET second_required = false;
ALTER TABLE payments.refunds ALTER COLUMN second_required SET NOT NULL;

ALTER TABLE payments.refunds ADD CONSTRAINT refunds_first_approver
    CHECK (first_approved_by IS NULL OR first_approved_by <> requested_by);
ALTER TABLE payments.refunds ADD CONSTRAINT refunds_second_approval
    CHECK (
        (NOT second_required AND first_approved_by IS NULL)
        OR (second_required AND (approved_by IS NULL OR first_approved_by IS NOT NULL AND first_approved_by <> approved_by))
    );

-- A dispute records that it suspended the account, so a repeated callback cannot suspend again after an operator lifts
-- it. A dispute an older node applied without suspending has no mark, which the poll uses to repair it after the
-- mixed-version window.
ALTER TABLE payments.order_disputes ADD COLUMN suspended_at timestamptz;
-- Disputes from before this rule keep today's behavior: no account is suspended retroactively.
UPDATE payments.order_disputes SET suspended_at = updated_at;
CREATE INDEX order_disputes_without_suspension ON payments.order_disputes (applied_at)
    WHERE state = 'applied' AND suspended_at IS NULL;

-- +goose Down

DROP INDEX payments.order_disputes_without_suspension;
ALTER TABLE payments.order_disputes DROP COLUMN suspended_at;
ALTER TABLE payments.refunds DROP CONSTRAINT refunds_second_approval, DROP CONSTRAINT refunds_first_approver;
DROP TRIGGER refunds_second_required ON payments.refunds;
DROP FUNCTION payments.refund_second_required();
ALTER TABLE payments.refunds DROP COLUMN first_approved_by, DROP COLUMN second_required;
ALTER TABLE payments.settings DROP COLUMN refund_second_approval_credits;
