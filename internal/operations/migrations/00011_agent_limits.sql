-- +goose Up

-- The limits the owner sets on what the operations agent may do (docs/domains/operations.md「运营凭证」): one row for
-- the platform, none until the owner first sets them, when the code's values apply. The code's values are also the
-- ceilings, so a limit can only be lowered. Only the alerts' limit is here for now; each further limit joins it as a
-- nullable column, so an old node's row stays readable.
CREATE TABLE operations.agent_limits (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    alerts_per_hour integer NOT NULL CHECK (alerts_per_hour BETWEEN 1 AND 60),
    rev bigint NOT NULL CHECK (rev >= 1),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE operations.agent_limits ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.agent_limits FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.agent_limits FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.agent_limits FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON operations.agent_limits TO axiom_app;
GRANT UPDATE (alerts_per_hour, rev, updated_at) ON operations.agent_limits TO axiom_app;

-- +goose Down
DROP TABLE operations.agent_limits;
