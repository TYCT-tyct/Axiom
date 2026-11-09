-- +goose Up

-- A refund the operations agent asked for (docs/domains/operations.md「运营接口」) names the operations credential as
-- its requester instead of a user, so the operator's list shows who asked. An older node never writes one and writes
-- false; it shows the credential's ID as a user's, which nobody can sign in as.
ALTER TABLE payments.refunds ADD COLUMN requested_by_agent boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE payments.refunds DROP COLUMN requested_by_agent;
