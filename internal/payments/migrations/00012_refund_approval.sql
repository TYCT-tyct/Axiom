-- +goose Up

-- A refund above the site's limit needs two operators; the first approval is kept apart from approved_by so the
-- release can still be the single field the poller and older nodes read. second_required is a snapshot taken when the
-- refund is frozen, so changing the limit never changes a request already under review.
ALTER TABLE payments.settings ADD COLUMN refund_second_approval_credits bigint
    CHECK (refund_second_approval_credits >= 0);
ALTER TABLE payments.refunds
    ADD COLUMN second_required boolean,
    ADD COLUMN first_approved_by uuid;

-- Old nodes insert a refund without the new column. The column has no default, so the trigger can tell that omission
-- from a new node's explicit snapshot and still applies the current rule before the old node can approve the row.
-- +goose StatementBegin
CREATE FUNCTION payments.refund_second_required() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    limit_credits bigint;
BEGIN
    IF NEW.second_required IS NULL THEN
        SELECT COALESCE(s.refund_second_approval_credits, 100000000000)
        INTO limit_credits
        FROM payments.settings AS s
        WHERE s.site_id = NEW.site_id;
        NEW.second_required := NEW.credits > COALESCE(limit_credits, 100000000000);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER refunds_second_required BEFORE INSERT ON payments.refunds
    FOR EACH ROW EXECUTE FUNCTION payments.refund_second_required();

-- Existing requests have no snapshot. Apply the new default threshold to requests still waiting for release so a large
-- pending refund follows the new rule; rows already approved were released under the old rule and keep their shape.
UPDATE payments.refunds SET second_required = approved_by IS NULL AND credits > 100000000000;
ALTER TABLE payments.refunds ALTER COLUMN second_required SET NOT NULL;

ALTER TABLE payments.refunds ADD CONSTRAINT refunds_first_approver
    CHECK (first_approved_by IS NULL OR first_approved_by <> requested_by);
ALTER TABLE payments.refunds ADD CONSTRAINT refunds_second_approval
    CHECK (
        (NOT second_required AND first_approved_by IS NULL)
        OR (second_required AND (approved_by IS NULL OR first_approved_by IS NOT NULL AND first_approved_by <> approved_by))
    );

-- +goose Down

ALTER TABLE payments.refunds DROP CONSTRAINT refunds_second_approval, DROP CONSTRAINT refunds_first_approver;
DROP TRIGGER refunds_second_required ON payments.refunds;
DROP FUNCTION payments.refund_second_required();
ALTER TABLE payments.refunds DROP COLUMN first_approved_by, DROP COLUMN second_required;
ALTER TABLE payments.settings DROP COLUMN refund_second_approval_credits;
