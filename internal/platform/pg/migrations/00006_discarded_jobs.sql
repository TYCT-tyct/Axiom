-- +goose Up

-- The queue's record of the discarded jobs it has reported, so one job is reported once however often it is scanned,
-- by whichever node, and after a restart. A discarded job is final: a job whose last attempt's node died is moved here
-- by River's rescuer without any worker seeing it, so the signal comes from this persisted state and not from a worker
-- error. The table is platform's, holds no tenant's rows, and only the platform scope reads or writes it.
CREATE TABLE platform.discarded_jobs (
    job_id bigint PRIMARY KEY,
    kind text NOT NULL,
    attempt smallint NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE platform.discarded_jobs ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.discarded_jobs
    USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

GRANT SELECT, INSERT, DELETE ON platform.discarded_jobs TO axiom_app;
