-- +goose Up

-- The queue's record of the discarded jobs it has reported, so one job is reported once however often it is scanned,
-- by whichever node, and after a restart. A discarded job is final: a job whose last attempt's node died is moved here
-- by River's rescuer without any worker seeing it, so the signal comes from this persisted state and not from a worker
-- error. The table is the queue's, in its own schema, and holds no tenant's rows.
CREATE TABLE river.axiom_discarded_job (
    job_id bigint PRIMARY KEY,
    kind text NOT NULL,
    attempt smallint NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT now()
);

-- Pruning by age: a row may go once the job is beyond River's discarded-job retention, which is six days shorter.
CREATE INDEX axiom_discarded_job_seen_at ON river.axiom_discarded_job (seen_at);

GRANT SELECT, INSERT, DELETE ON river.axiom_discarded_job TO axiom_app;
