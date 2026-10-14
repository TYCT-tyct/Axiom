-- +goose Up

GRANT USAGE ON SCHEMA platform TO axiom_app;

-- in_scope reports whether a row of the given site and account lies in the transaction's scope, which pg.DB sets at
-- the start of every transaction with set_config(..., true), so it ends with the transaction. The scope is a union:
-- every row, the rows of some sites, the rows of some accounts. An unset or empty setting is the empty set, so a
-- transaction that set no scope sees nothing; a malformed one makes the query fail instead of widening it.
-- No SET clause: it would keep the planner from inlining the function into every policy.
CREATE FUNCTION platform.in_scope(site uuid, account uuid) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN coalesce(current_setting('axiom.scope_platform', true), '') = 'on'
        OR site = ANY (coalesce(nullif(current_setting('axiom.scope_sites', true), '')::uuid[], '{}'))
        OR account = ANY (coalesce(nullif(current_setting('axiom.scope_accounts', true), '')::uuid[], '{}'));

-- protect_account_table puts a table of account data, with non-null site_id and account_id, under row-level
-- security: forced, so the table's owner is filtered too, with one scope check for reading and writing.
-- +goose StatementBegin
CREATE PROCEDURE platform.protect_account_table(t regclass)
    LANGUAGE plpgsql
AS $$
BEGIN
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant ON %s USING (platform.in_scope(site_id, account_id))', t);
END
$$;
-- +goose StatementEnd
