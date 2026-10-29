-- +goose Up
-- A transactional journal, drained by the notifications worker without network access in settlement.
CREATE TABLE billing.balance_notices (
    id uuid PRIMARY KEY DEFAULT uuidv7(), site_id uuid NOT NULL, account_id uuid NOT NULL,
    previous bigint NOT NULL, balance bigint NOT NULL, top_up bigint NOT NULL DEFAULT 0
);
ALTER TABLE billing.balance_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing.balance_notices FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.balance_notices FOR SELECT USING (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
 OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
CREATE POLICY inserts ON billing.balance_notices FOR INSERT WITH CHECK (
 (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
 OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]));
CREATE POLICY drains ON billing.balance_notices FOR DELETE USING ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, DELETE ON billing.balance_notices TO axiom_app;

-- Capture old rolling-release writers too, at the wallet and paid-batch boundaries.
-- +goose StatementBegin
CREATE FUNCTION billing.balance_notice() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    INSERT INTO billing.balance_notices(site_id,account_id,previous,balance)
    VALUES(NEW.site_id,NEW.account_id,OLD.balance,NEW.balance);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER balance_notice AFTER UPDATE OF balance ON billing.balances
FOR EACH ROW WHEN (NEW.balance <> OLD.balance) EXECUTE FUNCTION billing.balance_notice();
-- +goose StatementBegin
CREATE FUNCTION billing.top_up_notice() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    INSERT INTO billing.balance_notices(id,site_id,account_id,previous,balance,top_up)
    VALUES(NEW.id,NEW.site_id,NEW.account_id,0,0,NEW.amount);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER top_up_notice AFTER INSERT ON billing.credit_batches
FOR EACH ROW WHEN (NEW.source = 'paid') EXECUTE FUNCTION billing.top_up_notice();
-- PostgreSQL requires UPDATE privilege for row-locking reads; only the platform consumer may claim rows.
GRANT UPDATE (id) ON billing.balance_notices TO axiom_app;
CREATE POLICY claims ON billing.balance_notices FOR UPDATE USING ((SELECT platform.in_platform_scope())) WITH CHECK ((SELECT platform.in_platform_scope()));
