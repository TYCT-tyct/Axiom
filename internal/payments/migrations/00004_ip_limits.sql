-- +goose Up

-- The per-IP limits of a site (docs/domains/payments.md「防欺诈」). NULL is the default in code, as in the other limits:
-- 20 orders and 10 failed orders per IP group per hour.
ALTER TABLE payments.settings
    ADD COLUMN ip_order_limit bigint CHECK (ip_order_limit > 0),
    ADD COLUMN ip_failure_limit bigint CHECK (ip_failure_limit > 0);

-- One row per site and IP group (an IPv4 address, or the /64 of an IPv6 one): the orders placed and the orders that
-- failed since the window opened. Like merchant_usage it is the serialization point of a limit that spans accounts,
-- which an account-scoped transaction cannot read from orders; the order that takes the last place and the one that
-- finds none are decided by the row lock (README.md「决策与理由」). A row exists only after an order was placed from the
-- IP, so the table is no larger than orders.
CREATE TABLE payments.ip_usage (
    site_id uuid NOT NULL,
    ip inet NOT NULL,
    window_start timestamptz NOT NULL,
    orders bigint NOT NULL CHECK (orders >= 0),
    failures bigint NOT NULL CHECK (failures >= 0),
    PRIMARY KEY (site_id, ip)
);

ALTER TABLE payments.ip_usage ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON payments.ip_usage FOR SELECT USING (platform.sees_site(site_id));
CREATE POLICY inserts ON payments.ip_usage FOR INSERT WITH CHECK (platform.sees_site(site_id));
CREATE POLICY updates ON payments.ip_usage FOR UPDATE USING (platform.sees_site(site_id))
    WITH CHECK (platform.sees_site(site_id));
GRANT SELECT, INSERT (site_id, ip, window_start, orders, failures), UPDATE (window_start, orders, failures)
    ON payments.ip_usage TO axiom_app;
