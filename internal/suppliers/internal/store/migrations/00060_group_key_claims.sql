-- +goose Up

-- A group's claim on making its key at the supplier (docs/domains/suppliers.md「一个分组只建一把」): whoever holds it
-- (key_claim_id) may send the one create; key_claim_sent_at is when the create was sent, after which the result is
-- unknown until the supplier's id of the key (key_claim_ref) is recorded. key_claim_listing is where a held claim's
-- search by name stopped at the supplier's limits, for whoever goes on with it. All NULL is no claim. Every write
-- compares key_claim_id, so a holder that was taken over, or whose claim was released, writes nothing. The constraint
-- is added NOT VALID, so adding it scans nothing; every row it would check is NULL, and every write from now on is
-- checked.
ALTER TABLE suppliers.supplier_groups
    ADD COLUMN key_claim_id uuid,
    ADD COLUMN key_claimed_at timestamptz,
    ADD COLUMN key_claim_sent_at timestamptz,
    ADD COLUMN key_claim_ref text,
    ADD COLUMN key_claim_listing bytea,
    ADD CONSTRAINT supplier_groups_key_claim_check CHECK (
        (key_claim_id IS NULL) = (key_claimed_at IS NULL)
        AND (key_claim_sent_at IS NULL OR key_claim_id IS NOT NULL)
        AND (key_claim_ref IS NULL OR key_claim_sent_at IS NOT NULL)
        AND (key_claim_listing IS NULL OR (key_claim_id IS NOT NULL AND key_claim_sent_at IS NULL))
    ) NOT VALID;

GRANT UPDATE (key_claim_id, key_claimed_at, key_claim_sent_at, key_claim_ref, key_claim_listing)
    ON TABLE suppliers.supplier_groups TO axiom_app;
