-- +goose Up

-- Final pre-production schema; history and equivalence proof: scripts/compare_migrations.py.
SET LOCAL check_function_bodies = false;
GRANT USAGE, CREATE ON SCHEMA observability TO axiom_retention_job;

-- +goose StatementBegin
CREATE FUNCTION observability.journal_usage() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NOT NEW.billing_pending THEN
        INSERT INTO observability.usage_journal
            (site_id,account_id,workspace_id,key_id,model,at,currency,success,usage,physical_input,price_nano)
        VALUES (NEW.site_id,NEW.account_id,NEW.workspace_id,NEW.key_id,NEW.model,NEW.at,NEW.currency,
            NEW.error_kind = '',NEW.usage,NEW.physical_input,NEW.price_nano);
    END IF;
    RETURN NULL;
END
$$;


ALTER FUNCTION observability.journal_usage() OWNER TO axiom_owner;
-- +goose StatementEnd

CREATE TABLE observability.alert_configuration (
    singleton boolean DEFAULT true NOT NULL,
    rev bigint DEFAULT 1 NOT NULL,
    body jsonb NOT NULL,
    CONSTRAINT alert_configuration_singleton_check CHECK (singleton)
);



ALTER TABLE observability.alert_configuration OWNER TO axiom_owner;

CREATE TABLE observability.alert_events (
    id uuid NOT NULL,
    kind text NOT NULL,
    key text NOT NULL,
    severity text NOT NULL,
    fields jsonb NOT NULL,
    occurrences bigint DEFAULT 1 NOT NULL,
    state text NOT NULL,
    fired_at timestamptz NOT NULL,
    pending_recovery boolean DEFAULT false NOT NULL,
    resolved_at timestamptz,
    checked_at timestamptz DEFAULT '-infinity'::timestamptz NOT NULL,
    CONSTRAINT alert_events_state_check CHECK ((state = ANY (ARRAY['firing'::text, 'delivered'::text, 'resolved'::text])))
);



ALTER TABLE observability.alert_events OWNER TO axiom_owner;

CREATE TABLE observability.alert_receipts (
    id uuid NOT NULL,
    event_id uuid NOT NULL,
    rule_id text NOT NULL,
    sequence integer NOT NULL,
    channel jsonb NOT NULL,
    quiet_start integer NOT NULL,
    quiet_end integer NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    at timestamptz NOT NULL,
    not_before timestamptz DEFAULT '-infinity'::timestamptz NOT NULL,
    delivered_at timestamptz,
    payload jsonb,
    fields jsonb,
    CONSTRAINT alert_receipts_check CHECK (((state = 'delivered'::text) = (delivered_at IS NOT NULL))),
    CONSTRAINT alert_receipts_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'queued'::text, 'delivered'::text, 'failed'::text])))
);



ALTER TABLE observability.alert_receipts OWNER TO axiom_owner;

CREATE TABLE observability.alert_windows (
    event_id uuid NOT NULL,
    rule_id text NOT NULL,
    since timestamptz NOT NULL
);



ALTER TABLE observability.alert_windows OWNER TO axiom_owner;

CREATE TABLE observability.call_attempt (
    request_id uuid NOT NULL,
    at timestamptz NOT NULL,
    attempt_seq smallint NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    endpoint_id uuid NOT NULL,
    supplier_id uuid NOT NULL,
    source_class text DEFAULT ''::text NOT NULL,
    egress text DEFAULT ''::text NOT NULL,
    outcome text NOT NULL,
    error_kind text DEFAULT ''::text NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    first_token_ms integer NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
)
PARTITION BY RANGE (at);



ALTER TABLE observability.call_attempt OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2026_10 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2026_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2026_11 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2026_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2026_12 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2026_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_01 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_01 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_02 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_02 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_03 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_03 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_04 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_04 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_05 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_05 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_06 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_06 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_07 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_07 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_08 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_08 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_09 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_09 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_10 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_11 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2027_12 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2027_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_01 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_01 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_02 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_02 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_03 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_03 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_04 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_04 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_05 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_05 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_06 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_06 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_07 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_07 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_08 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_08 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_09 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_09 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_10 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_11 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_2028_12 (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_2028_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_attempt_default (
    request_id uuid CONSTRAINT call_attempt_request_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_attempt_at_not_null NOT NULL,
    attempt_seq smallint CONSTRAINT call_attempt_attempt_seq_not_null NOT NULL,
    site_id uuid CONSTRAINT call_attempt_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_attempt_account_id_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_attempt_endpoint_id_not_null NOT NULL,
    supplier_id uuid CONSTRAINT call_attempt_supplier_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_attempt_source_class_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_attempt_egress_not_null NOT NULL,
    outcome text CONSTRAINT call_attempt_outcome_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_attempt_error_kind_not_null NOT NULL,
    started_at timestamptz CONSTRAINT call_attempt_started_at_not_null NOT NULL,
    finished_at timestamptz CONSTRAINT call_attempt_finished_at_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_attempt_first_token_ms_not_null NOT NULL,
    CONSTRAINT call_attempt_check CHECK ((finished_at >= started_at)),
    CONSTRAINT call_attempt_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_attempt_endpoint_id_check CHECK ((endpoint_id <> '00000000-0000-0000-0000-000000000000'::uuid)),
    CONSTRAINT call_attempt_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_attempt_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_attempt_outcome_check CHECK ((outcome = ANY (ARRAY['committed'::text, 'failed'::text, 'unknown'::text]))),
    CONSTRAINT call_attempt_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_attempt_supplier_id_check CHECK ((supplier_id <> '00000000-0000-0000-0000-000000000000'::uuid))
);



ALTER TABLE observability.call_attempt_default OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log (
    id uuid NOT NULL,
    at timestamptz NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    end_user_hash text DEFAULT ''::text NOT NULL,
    ip inet NOT NULL,
    client_app text DEFAULT ''::text NOT NULL,
    session_id text DEFAULT ''::text NOT NULL,
    subagent_id text DEFAULT ''::text NOT NULL,
    request_class text NOT NULL,
    language text DEFAULT ''::text NOT NULL,
    used_tools boolean NOT NULL,
    has_image boolean NOT NULL,
    has_file boolean NOT NULL,
    model text NOT NULL,
    endpoint_id uuid NOT NULL,
    source_class text DEFAULT ''::text NOT NULL,
    attempt_count smallint NOT NULL,
    route jsonb DEFAULT '{}'::jsonb NOT NULL,
    usage jsonb NOT NULL,
    service_tier text NOT NULL,
    price_nano bigint NOT NULL,
    cost_nano bigint NOT NULL,
    currency text NOT NULL,
    free_of_charge boolean NOT NULL,
    latency_ms integer NOT NULL,
    first_token_ms integer NOT NULL,
    finish_reason text DEFAULT ''::text NOT NULL,
    error_kind text DEFAULT ''::text NOT NULL,
    consent_version text NOT NULL,
    training_allowed boolean NOT NULL,
    egress text DEFAULT ''::text NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text NOT NULL,
    app_title text DEFAULT ''::text NOT NULL,
    app_categories text DEFAULT ''::text NOT NULL,
    streamed boolean DEFAULT false NOT NULL,
    physical_input bigint DEFAULT '-1'::integer NOT NULL,
    cache_hit boolean DEFAULT false NOT NULL,
    billing_pending boolean DEFAULT false NOT NULL,
    usage_rollup_live boolean DEFAULT true NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
)
PARTITION BY RANGE (at);



ALTER TABLE observability.call_log OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2026_10 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2026_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2026_11 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2026_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2026_12 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2026_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_01 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_01 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_02 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_02 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_03 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_03 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_04 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_04 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_05 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_05 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_06 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_06 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_07 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_07 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_08 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_08 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_09 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_09 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_10 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_11 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2027_12 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2027_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_01 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_01 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_02 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_02 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_03 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_03 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_04 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_04 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_05 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_05 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_06 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_06 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_07 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_07 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_08 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_08 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_09 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_09 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_10 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_10 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_11 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_11 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_2028_12 (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_2028_12 OWNER TO axiom_retention_job;

CREATE TABLE observability.call_log_default (
    id uuid CONSTRAINT call_log_id_not_null NOT NULL,
    at timestamptz CONSTRAINT call_log_at_not_null NOT NULL,
    site_id uuid CONSTRAINT call_log_site_id_not_null NOT NULL,
    account_id uuid CONSTRAINT call_log_account_id_not_null NOT NULL,
    workspace_id uuid CONSTRAINT call_log_workspace_id_not_null NOT NULL,
    key_id uuid CONSTRAINT call_log_key_id_not_null NOT NULL,
    end_user_hash text DEFAULT ''::text CONSTRAINT call_log_end_user_hash_not_null NOT NULL,
    ip inet CONSTRAINT call_log_ip_not_null NOT NULL,
    client_app text DEFAULT ''::text CONSTRAINT call_log_client_app_not_null NOT NULL,
    session_id text DEFAULT ''::text CONSTRAINT call_log_session_id_not_null NOT NULL,
    subagent_id text DEFAULT ''::text CONSTRAINT call_log_subagent_id_not_null NOT NULL,
    request_class text CONSTRAINT call_log_request_class_not_null NOT NULL,
    language text DEFAULT ''::text CONSTRAINT call_log_language_not_null NOT NULL,
    used_tools boolean CONSTRAINT call_log_used_tools_not_null NOT NULL,
    has_image boolean CONSTRAINT call_log_has_image_not_null NOT NULL,
    has_file boolean CONSTRAINT call_log_has_file_not_null NOT NULL,
    model text CONSTRAINT call_log_model_not_null NOT NULL,
    endpoint_id uuid CONSTRAINT call_log_endpoint_id_not_null NOT NULL,
    source_class text DEFAULT ''::text CONSTRAINT call_log_source_class_not_null NOT NULL,
    attempt_count smallint CONSTRAINT call_log_attempt_count_not_null NOT NULL,
    route jsonb DEFAULT '{}'::jsonb CONSTRAINT call_log_route_not_null NOT NULL,
    usage jsonb CONSTRAINT call_log_usage_not_null NOT NULL,
    service_tier text CONSTRAINT call_log_service_tier_not_null NOT NULL,
    price_nano bigint CONSTRAINT call_log_price_nano_not_null NOT NULL,
    cost_nano bigint CONSTRAINT call_log_cost_nano_not_null NOT NULL,
    currency text CONSTRAINT call_log_currency_not_null NOT NULL,
    free_of_charge boolean CONSTRAINT call_log_free_of_charge_not_null NOT NULL,
    latency_ms integer CONSTRAINT call_log_latency_ms_not_null NOT NULL,
    first_token_ms integer CONSTRAINT call_log_first_token_ms_not_null NOT NULL,
    finish_reason text DEFAULT ''::text CONSTRAINT call_log_finish_reason_not_null NOT NULL,
    error_kind text DEFAULT ''::text CONSTRAINT call_log_error_kind_not_null NOT NULL,
    consent_version text CONSTRAINT call_log_consent_version_not_null NOT NULL,
    training_allowed boolean CONSTRAINT call_log_training_allowed_not_null NOT NULL,
    egress text DEFAULT ''::text CONSTRAINT call_log_egress_not_null NOT NULL,
    price_version uuid,
    app_referer text DEFAULT ''::text CONSTRAINT call_log_app_referer_not_null NOT NULL,
    app_title text DEFAULT ''::text CONSTRAINT call_log_app_title_not_null NOT NULL,
    app_categories text DEFAULT ''::text CONSTRAINT call_log_app_categories_not_null NOT NULL,
    streamed boolean DEFAULT false CONSTRAINT call_log_streamed_not_null NOT NULL,
    physical_input bigint DEFAULT '-1'::integer CONSTRAINT call_log_physical_input_not_null NOT NULL,
    cache_hit boolean DEFAULT false CONSTRAINT call_log_cache_hit_not_null NOT NULL,
    billing_pending boolean DEFAULT false CONSTRAINT call_log_billing_pending_not_null NOT NULL,
    usage_rollup_live boolean DEFAULT true CONSTRAINT call_log_usage_rollup_live_not_null NOT NULL,
    CONSTRAINT call_log_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT call_log_client_app_check CHECK ((char_length(client_app) <= 64)),
    CONSTRAINT call_log_consent_version_check CHECK (((char_length(consent_version) >= 1) AND (char_length(consent_version) <= 64))),
    CONSTRAINT call_log_cost_nano_check CHECK ((cost_nano >= 0)),
    CONSTRAINT call_log_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT call_log_egress_check CHECK ((char_length(egress) <= 64)),
    CONSTRAINT call_log_end_user_hash_check CHECK ((end_user_hash ~ '^([0-9a-f]{64})?$'::text)),
    CONSTRAINT call_log_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT call_log_finish_reason_check CHECK ((char_length(finish_reason) <= 64)),
    CONSTRAINT call_log_first_token_ms_check CHECK ((first_token_ms >= 0)),
    CONSTRAINT call_log_language_check CHECK ((char_length(language) <= 16)),
    CONSTRAINT call_log_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT call_log_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 200))),
    CONSTRAINT call_log_physical_input_check CHECK ((physical_input >= '-1'::integer)),
    CONSTRAINT call_log_price_nano_check CHECK ((price_nano >= 0)),
    CONSTRAINT call_log_request_class_check CHECK ((request_class ~ '^[a-z][a-z0-9_.]{0,63}$'::text)),
    CONSTRAINT call_log_route_check CHECK (((jsonb_typeof(route) = 'object'::text) AND (octet_length((route)::text) <= 8192))),
    CONSTRAINT call_log_service_tier_check CHECK ((service_tier = ANY (ARRAY['standard'::text, 'flex'::text, 'priority'::text, 'fast'::text]))),
    CONSTRAINT call_log_session_id_check CHECK ((char_length(session_id) <= 128)),
    CONSTRAINT call_log_source_class_check CHECK ((char_length(source_class) <= 64)),
    CONSTRAINT call_log_subagent_id_check CHECK ((char_length(subagent_id) <= 128)),
    CONSTRAINT call_log_usage_check CHECK (((jsonb_typeof(usage) = 'object'::text) AND (octet_length((usage)::text) <= 8192)))
);



ALTER TABLE observability.call_log_default OWNER TO axiom_retention_job;

CREATE TABLE observability.channel_tests (
    channel_id text NOT NULL,
    destination text NOT NULL,
    receipt_id uuid,
    next_due timestamptz NOT NULL,
    reported_state text DEFAULT ''::text NOT NULL
);



ALTER TABLE observability.channel_tests OWNER TO axiom_owner;

CREATE TABLE observability.content_blobs (
    id uuid NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    digest bytea NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    created_at timestamptz NOT NULL,
    last_referenced_at timestamptz NOT NULL,
    expires_at timestamptz,
    CONSTRAINT content_blobs_check CHECK ((last_referenced_at >= created_at)),
    CONSTRAINT content_blobs_check1 CHECK (((expires_at IS NULL) OR (expires_at > created_at))),
    CONSTRAINT content_blobs_digest_check CHECK ((octet_length(digest) = 32)),
    CONSTRAINT content_blobs_object_key_check CHECK (((char_length(object_key) >= 1) AND (char_length(object_key) <= 512))),
    CONSTRAINT content_blobs_size_check CHECK (((size >= 1) AND (size <= 67108864)))
);



ALTER TABLE observability.content_blobs OWNER TO axiom_owner;

CREATE TABLE observability.content_exports (
    id uuid NOT NULL,
    approval_digest bytea NOT NULL,
    owner_key text NOT NULL,
    owner2_key text NOT NULL,
    recipient_key text NOT NULL,
    since timestamptz NOT NULL,
    until timestamptz NOT NULL,
    site_id uuid,
    account_id uuid,
    destination text NOT NULL,
    format text NOT NULL,
    status text NOT NULL,
    content_count bigint DEFAULT 0 NOT NULL,
    content_bytes bigint DEFAULT 0 NOT NULL,
    cache_count bigint DEFAULT 0 NOT NULL,
    cache_bytes bigint DEFAULT 0 NOT NULL,
    output_bytes bigint DEFAULT 0 NOT NULL,
    output_sha256 bytea,
    error_kind text DEFAULT ''::text NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    CONSTRAINT content_exports_approval_digest_check CHECK ((octet_length(approval_digest) = 32)),
    CONSTRAINT content_exports_cache_bytes_check CHECK ((cache_bytes >= 0)),
    CONSTRAINT content_exports_cache_count_check CHECK ((cache_count >= 0)),
    CONSTRAINT content_exports_check CHECK ((until > since)),
    CONSTRAINT content_exports_check1 CHECK (((status = 'completed'::text) = ((finished_at IS NOT NULL) AND (output_sha256 IS NOT NULL)))),
    CONSTRAINT content_exports_content_bytes_check CHECK ((content_bytes >= 0)),
    CONSTRAINT content_exports_content_count_check CHECK ((content_count >= 0)),
    CONSTRAINT content_exports_destination_check CHECK (((destination ~ '^/'::text) AND (char_length(destination) <= 1024))),
    CONSTRAINT content_exports_error_kind_check CHECK ((char_length(error_kind) <= 64)),
    CONSTRAINT content_exports_format_check CHECK (((char_length(format) >= 1) AND (char_length(format) <= 64))),
    CONSTRAINT content_exports_output_bytes_check CHECK ((output_bytes >= 0)),
    CONSTRAINT content_exports_output_sha256_check CHECK (((output_sha256 IS NULL) OR (octet_length(output_sha256) = 32))),
    CONSTRAINT content_exports_owner2_key_check CHECK ((owner2_key ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT content_exports_owner_key_check CHECK ((owner_key ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT content_exports_recipient_key_check CHECK ((recipient_key ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT content_exports_status_check CHECK ((status = ANY (ARRAY['started'::text, 'completed'::text, 'failed'::text])))
);



ALTER TABLE observability.content_exports OWNER TO axiom_owner;

CREATE TABLE observability.content_keys (
    account_id uuid NOT NULL,
    site_id uuid NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_key bytea NOT NULL,
    key_version bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT content_keys_ciphertext_check CHECK ((octet_length(ciphertext) = 48)),
    CONSTRAINT content_keys_key_version_check CHECK (((key_version >= 1) AND (key_version <= '4294967295'::bigint))),
    CONSTRAINT content_keys_nonce_check CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT content_keys_wrapped_key_check CHECK ((octet_length(wrapped_key) = 60))
);



ALTER TABLE observability.content_keys OWNER TO axiom_owner;

CREATE TABLE observability.content_refs (
    request_id uuid NOT NULL,
    part text NOT NULL,
    kind text NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    blob_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT content_refs_kind_check CHECK ((kind = ANY (ARRAY['request'::text, 'response'::text, 'attempt'::text]))),
    CONSTRAINT content_refs_part_check CHECK ((part ~ '^[a-z0-9][a-z0-9._-]{0,63}$'::text))
);



ALTER TABLE observability.content_refs OWNER TO axiom_owner;

CREATE TABLE observability.retention (
    singleton boolean DEFAULT true NOT NULL,
    body jsonb NOT NULL,
    CONSTRAINT retention_body_check CHECK (((jsonb_typeof(body) = 'object'::text) AND (octet_length((body)::text) <= 1024))),
    CONSTRAINT retention_singleton_check CHECK (singleton)
);



ALTER TABLE observability.retention OWNER TO axiom_owner;

CREATE TABLE observability.spend_anomalies (
    account_id uuid NOT NULL,
    kind text NOT NULL,
    CONSTRAINT spend_anomalies_kind_check CHECK (((char_length(kind) >= 1) AND (char_length(kind) <= 64)))
);



ALTER TABLE observability.spend_anomalies OWNER TO axiom_owner;

CREATE TABLE observability.spend_detection (
    singleton boolean DEFAULT true NOT NULL,
    body jsonb NOT NULL,
    CONSTRAINT spend_detection_body_check CHECK (((jsonb_typeof(body) = 'object'::text) AND (octet_length((body)::text) <= 4096))),
    CONSTRAINT spend_detection_singleton_check CHECK (singleton)
);



ALTER TABLE observability.spend_detection OWNER TO axiom_owner;

CREATE TABLE observability.usage_backfill (
    singleton boolean DEFAULT true NOT NULL,
    at timestamptz DEFAULT '0001-01-01 00:00:00+00'::timestamptz NOT NULL,
    id uuid DEFAULT '00000000-0000-0000-0000-000000000000'::uuid NOT NULL,
    until_at timestamptz NOT NULL,
    until_id uuid NOT NULL,
    done boolean DEFAULT false NOT NULL,
    CONSTRAINT usage_backfill_singleton_check CHECK (singleton)
);



ALTER TABLE observability.usage_backfill OWNER TO axiom_owner;

CREATE TABLE observability.usage_hourly (
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    model text NOT NULL,
    currency text NOT NULL,
    requests numeric NOT NULL,
    successes numeric NOT NULL,
    input numeric,
    output numeric,
    cache_read numeric,
    cache_write numeric,
    cache_rate_requests numeric NOT NULL,
    cache_rate_read numeric NOT NULL,
    cache_rate_input numeric NOT NULL,
    spend numeric NOT NULL
);



ALTER TABLE observability.usage_hourly OWNER TO axiom_owner;

CREATE TABLE observability.usage_journal (
    id bigint NOT NULL,
    site_id uuid NOT NULL,
    account_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    key_id uuid NOT NULL,
    model text NOT NULL,
    at timestamptz NOT NULL,
    currency text NOT NULL,
    success boolean NOT NULL,
    usage jsonb NOT NULL,
    physical_input bigint NOT NULL,
    price_nano bigint NOT NULL
);



ALTER TABLE observability.usage_journal OWNER TO axiom_owner;

ALTER TABLE observability.usage_journal ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME observability.usage_journal_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE observability.webhook_keys (
    id text NOT NULL,
    channel_id text NOT NULL,
    destination text NOT NULL,
    expires_at timestamptz,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    wrapped_dek bytea NOT NULL,
    key_version bigint NOT NULL
);



ALTER TABLE observability.webhook_keys OWNER TO axiom_owner;

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2026_10 FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2026_11 FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2026_12 FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_01 FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_02 FOR VALUES FROM ('2027-02-01 00:00:00+00') TO ('2027-03-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_03 FOR VALUES FROM ('2027-03-01 00:00:00+00') TO ('2027-04-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_04 FOR VALUES FROM ('2027-04-01 00:00:00+00') TO ('2027-05-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_05 FOR VALUES FROM ('2027-05-01 00:00:00+00') TO ('2027-06-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_06 FOR VALUES FROM ('2027-06-01 00:00:00+00') TO ('2027-07-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_07 FOR VALUES FROM ('2027-07-01 00:00:00+00') TO ('2027-08-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_08 FOR VALUES FROM ('2027-08-01 00:00:00+00') TO ('2027-09-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_09 FOR VALUES FROM ('2027-09-01 00:00:00+00') TO ('2027-10-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_10 FOR VALUES FROM ('2027-10-01 00:00:00+00') TO ('2027-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_11 FOR VALUES FROM ('2027-11-01 00:00:00+00') TO ('2027-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2027_12 FOR VALUES FROM ('2027-12-01 00:00:00+00') TO ('2028-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_01 FOR VALUES FROM ('2028-01-01 00:00:00+00') TO ('2028-02-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_02 FOR VALUES FROM ('2028-02-01 00:00:00+00') TO ('2028-03-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_03 FOR VALUES FROM ('2028-03-01 00:00:00+00') TO ('2028-04-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_04 FOR VALUES FROM ('2028-04-01 00:00:00+00') TO ('2028-05-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_05 FOR VALUES FROM ('2028-05-01 00:00:00+00') TO ('2028-06-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_06 FOR VALUES FROM ('2028-06-01 00:00:00+00') TO ('2028-07-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_07 FOR VALUES FROM ('2028-07-01 00:00:00+00') TO ('2028-08-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_08 FOR VALUES FROM ('2028-08-01 00:00:00+00') TO ('2028-09-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_09 FOR VALUES FROM ('2028-09-01 00:00:00+00') TO ('2028-10-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_10 FOR VALUES FROM ('2028-10-01 00:00:00+00') TO ('2028-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_11 FOR VALUES FROM ('2028-11-01 00:00:00+00') TO ('2028-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_2028_12 FOR VALUES FROM ('2028-12-01 00:00:00+00') TO ('2029-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_attempt ATTACH PARTITION observability.call_attempt_default DEFAULT;

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2026_10 FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2026_11 FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2026_12 FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_01 FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-02-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_02 FOR VALUES FROM ('2027-02-01 00:00:00+00') TO ('2027-03-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_03 FOR VALUES FROM ('2027-03-01 00:00:00+00') TO ('2027-04-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_04 FOR VALUES FROM ('2027-04-01 00:00:00+00') TO ('2027-05-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_05 FOR VALUES FROM ('2027-05-01 00:00:00+00') TO ('2027-06-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_06 FOR VALUES FROM ('2027-06-01 00:00:00+00') TO ('2027-07-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_07 FOR VALUES FROM ('2027-07-01 00:00:00+00') TO ('2027-08-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_08 FOR VALUES FROM ('2027-08-01 00:00:00+00') TO ('2027-09-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_09 FOR VALUES FROM ('2027-09-01 00:00:00+00') TO ('2027-10-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_10 FOR VALUES FROM ('2027-10-01 00:00:00+00') TO ('2027-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_11 FOR VALUES FROM ('2027-11-01 00:00:00+00') TO ('2027-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2027_12 FOR VALUES FROM ('2027-12-01 00:00:00+00') TO ('2028-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_01 FOR VALUES FROM ('2028-01-01 00:00:00+00') TO ('2028-02-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_02 FOR VALUES FROM ('2028-02-01 00:00:00+00') TO ('2028-03-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_03 FOR VALUES FROM ('2028-03-01 00:00:00+00') TO ('2028-04-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_04 FOR VALUES FROM ('2028-04-01 00:00:00+00') TO ('2028-05-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_05 FOR VALUES FROM ('2028-05-01 00:00:00+00') TO ('2028-06-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_06 FOR VALUES FROM ('2028-06-01 00:00:00+00') TO ('2028-07-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_07 FOR VALUES FROM ('2028-07-01 00:00:00+00') TO ('2028-08-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_08 FOR VALUES FROM ('2028-08-01 00:00:00+00') TO ('2028-09-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_09 FOR VALUES FROM ('2028-09-01 00:00:00+00') TO ('2028-10-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_10 FOR VALUES FROM ('2028-10-01 00:00:00+00') TO ('2028-11-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_11 FOR VALUES FROM ('2028-11-01 00:00:00+00') TO ('2028-12-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_2028_12 FOR VALUES FROM ('2028-12-01 00:00:00+00') TO ('2029-01-01 00:00:00+00');

ALTER TABLE ONLY observability.call_log ATTACH PARTITION observability.call_log_default DEFAULT;

INSERT INTO observability.alert_configuration (singleton, rev, body) VALUES (true, 2, '{"rules": [{"id": "write-lag", "kind": "observability.write_lag", "channel": "", "quiet_end": 0, "threshold": 4000, "quiet_start": 0, "repeat_seconds": 0, "duration_seconds": 60, "minimum_severity": "warning"}, {"id": "settlement", "kind": "billing.settlement_anomaly", "channel": "", "quiet_end": 0, "threshold": 1, "quiet_start": 0, "repeat_seconds": 0, "duration_seconds": 0, "minimum_severity": "warning"}, {"id": "discarded", "kind": "queue.discarded", "channel": "", "quiet_end": 0, "threshold": 1, "quiet_start": 0, "repeat_seconds": 0, "duration_seconds": 0, "minimum_severity": "warning"}, {"id": "records-lost", "kind": "observability.records_lost", "channel": "", "quiet_end": 0, "threshold": 1, "quiet_start": 0, "repeat_seconds": 0, "duration_seconds": 0, "minimum_severity": "warning"}, {"id": "spend-anomaly", "kind": "observability.spend_anomaly", "channel": "", "quiet_end": 0, "threshold": 1, "quiet_start": 0, "repeat_seconds": 0, "duration_seconds": 0, "minimum_severity": "warning"}], "channels": []}');
INSERT INTO observability.usage_backfill (singleton, at, id, until_at, until_id, done) VALUES (true, '0001-01-01 00:00:00+00', '00000000-0000-0000-0000-000000000000', '0001-01-01 00:00:00+00', '00000000-0000-0000-0000-000000000000', false);

ALTER TABLE ONLY observability.alert_configuration
    ADD CONSTRAINT alert_configuration_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY observability.alert_events
    ADD CONSTRAINT alert_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY observability.alert_receipts
    ADD CONSTRAINT alert_receipts_event_id_rule_id_sequence_key UNIQUE (event_id, rule_id, sequence);

ALTER TABLE ONLY observability.alert_receipts
    ADD CONSTRAINT alert_receipts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY observability.alert_windows
    ADD CONSTRAINT alert_windows_pkey PRIMARY KEY (event_id, rule_id);

ALTER TABLE ONLY observability.call_attempt
    ADD CONSTRAINT call_attempt_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2026_10
    ADD CONSTRAINT call_attempt_2026_10_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2026_11
    ADD CONSTRAINT call_attempt_2026_11_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2026_12
    ADD CONSTRAINT call_attempt_2026_12_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_01
    ADD CONSTRAINT call_attempt_2027_01_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_02
    ADD CONSTRAINT call_attempt_2027_02_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_03
    ADD CONSTRAINT call_attempt_2027_03_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_04
    ADD CONSTRAINT call_attempt_2027_04_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_05
    ADD CONSTRAINT call_attempt_2027_05_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_06
    ADD CONSTRAINT call_attempt_2027_06_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_07
    ADD CONSTRAINT call_attempt_2027_07_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_08
    ADD CONSTRAINT call_attempt_2027_08_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_09
    ADD CONSTRAINT call_attempt_2027_09_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_10
    ADD CONSTRAINT call_attempt_2027_10_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_11
    ADD CONSTRAINT call_attempt_2027_11_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2027_12
    ADD CONSTRAINT call_attempt_2027_12_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_01
    ADD CONSTRAINT call_attempt_2028_01_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_02
    ADD CONSTRAINT call_attempt_2028_02_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_03
    ADD CONSTRAINT call_attempt_2028_03_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_04
    ADD CONSTRAINT call_attempt_2028_04_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_05
    ADD CONSTRAINT call_attempt_2028_05_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_06
    ADD CONSTRAINT call_attempt_2028_06_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_07
    ADD CONSTRAINT call_attempt_2028_07_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_08
    ADD CONSTRAINT call_attempt_2028_08_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_09
    ADD CONSTRAINT call_attempt_2028_09_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_10
    ADD CONSTRAINT call_attempt_2028_10_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_11
    ADD CONSTRAINT call_attempt_2028_11_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_attempt_2028_12
    ADD CONSTRAINT call_attempt_2028_12_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE observability.call_attempt
    ADD CONSTRAINT call_attempt_attempt_seq_check CHECK (((attempt_seq >= 1) AND (attempt_seq <= 33))) NOT VALID;

ALTER TABLE ONLY observability.call_attempt_default
    ADD CONSTRAINT call_attempt_default_pkey PRIMARY KEY (request_id, at, attempt_seq);

ALTER TABLE ONLY observability.call_log
    ADD CONSTRAINT call_log_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2026_10
    ADD CONSTRAINT call_log_2026_10_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2026_11
    ADD CONSTRAINT call_log_2026_11_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2026_12
    ADD CONSTRAINT call_log_2026_12_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_01
    ADD CONSTRAINT call_log_2027_01_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_02
    ADD CONSTRAINT call_log_2027_02_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_03
    ADD CONSTRAINT call_log_2027_03_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_04
    ADD CONSTRAINT call_log_2027_04_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_05
    ADD CONSTRAINT call_log_2027_05_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_06
    ADD CONSTRAINT call_log_2027_06_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_07
    ADD CONSTRAINT call_log_2027_07_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_08
    ADD CONSTRAINT call_log_2027_08_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_09
    ADD CONSTRAINT call_log_2027_09_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_10
    ADD CONSTRAINT call_log_2027_10_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_11
    ADD CONSTRAINT call_log_2027_11_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2027_12
    ADD CONSTRAINT call_log_2027_12_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_01
    ADD CONSTRAINT call_log_2028_01_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_02
    ADD CONSTRAINT call_log_2028_02_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_03
    ADD CONSTRAINT call_log_2028_03_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_04
    ADD CONSTRAINT call_log_2028_04_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_05
    ADD CONSTRAINT call_log_2028_05_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_06
    ADD CONSTRAINT call_log_2028_06_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_07
    ADD CONSTRAINT call_log_2028_07_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_08
    ADD CONSTRAINT call_log_2028_08_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_09
    ADD CONSTRAINT call_log_2028_09_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_10
    ADD CONSTRAINT call_log_2028_10_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_11
    ADD CONSTRAINT call_log_2028_11_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.call_log_2028_12
    ADD CONSTRAINT call_log_2028_12_pkey PRIMARY KEY (id, at);

ALTER TABLE observability.call_log
    ADD CONSTRAINT call_log_app_categories_check CHECK ((char_length(app_categories) <= 64)) NOT VALID;

ALTER TABLE observability.call_log
    ADD CONSTRAINT call_log_app_referer_check CHECK ((char_length(app_referer) <= 2048)) NOT VALID;

ALTER TABLE observability.call_log
    ADD CONSTRAINT call_log_app_title_check CHECK ((char_length(app_title) <= 200)) NOT VALID;

ALTER TABLE ONLY observability.call_log_default
    ADD CONSTRAINT call_log_default_pkey PRIMARY KEY (id, at);

ALTER TABLE ONLY observability.channel_tests
    ADD CONSTRAINT channel_tests_pkey PRIMARY KEY (channel_id);

ALTER TABLE ONLY observability.content_blobs
    ADD CONSTRAINT content_blobs_account_id_digest_key UNIQUE (account_id, digest);

ALTER TABLE ONLY observability.content_blobs
    ADD CONSTRAINT content_blobs_id_site_id_account_id_key UNIQUE (id, site_id, account_id);

ALTER TABLE ONLY observability.content_blobs
    ADD CONSTRAINT content_blobs_object_key_key UNIQUE (object_key);

ALTER TABLE ONLY observability.content_blobs
    ADD CONSTRAINT content_blobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY observability.content_exports
    ADD CONSTRAINT content_exports_approval_digest_key UNIQUE (approval_digest);

ALTER TABLE ONLY observability.content_exports
    ADD CONSTRAINT content_exports_pkey PRIMARY KEY (id);

ALTER TABLE ONLY observability.content_keys
    ADD CONSTRAINT content_keys_pkey PRIMARY KEY (account_id);

ALTER TABLE ONLY observability.content_refs
    ADD CONSTRAINT content_refs_pkey PRIMARY KEY (request_id, kind, part);

ALTER TABLE ONLY observability.retention
    ADD CONSTRAINT retention_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY observability.spend_anomalies
    ADD CONSTRAINT spend_anomalies_pkey PRIMARY KEY (account_id, kind);

ALTER TABLE ONLY observability.spend_detection
    ADD CONSTRAINT spend_detection_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY observability.usage_backfill
    ADD CONSTRAINT usage_backfill_pkey PRIMARY KEY (singleton);

ALTER TABLE ONLY observability.usage_hourly
    ADD CONSTRAINT usage_hourly_pkey PRIMARY KEY (account_id, hour, site_id, workspace_id, key_id, model, currency);

ALTER TABLE ONLY observability.usage_journal
    ADD CONSTRAINT usage_journal_pkey PRIMARY KEY (id);

ALTER TABLE ONLY observability.webhook_keys
    ADD CONSTRAINT webhook_keys_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX alert_active ON observability.alert_events USING btree (kind, key) WHERE (resolved_at IS NULL);

CREATE INDEX alert_dispatch ON observability.alert_receipts USING btree (at, id) WHERE (state = 'pending'::text);

CREATE INDEX alert_evaluate ON observability.alert_events USING btree (checked_at, id) WHERE ((resolved_at IS NULL) OR pending_recovery);

CREATE INDEX alert_queued_receipts ON observability.alert_receipts USING btree (at, id) WHERE (state = 'queued'::text);

CREATE INDEX alert_receipt_page ON observability.alert_receipts USING btree (event_id, at DESC, id DESC);

CREATE INDEX alert_retention ON observability.alert_events USING btree (resolved_at, id) WHERE ((resolved_at IS NOT NULL) AND (NOT pending_recovery));

CREATE INDEX call_log_account_at ON ONLY observability.call_log USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_account_fallback_at ON ONLY observability.call_log USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2026_10_account_id_at_id_idx1 ON observability.call_log_2026_10 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_account_app_at ON ONLY observability.call_log USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_client_app_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_account_status_at ON ONLY observability.call_log USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_expr_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_account_key_at ON ONLY observability.call_log USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_key_id_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_account_model_at ON ONLY observability.call_log USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_model_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_account_workspace_at ON ONLY observability.call_log USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2026_10_account_id_workspace_id_at_id_idx ON observability.call_log_2026_10 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_at_account ON ONLY observability.call_log USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2026_10_at_account_id_idx ON observability.call_log_2026_10 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_admin_time ON ONLY observability.call_log USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2026_10_timezone_id_idx ON observability.call_log_2026_10 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_at_id_idx1 ON observability.call_log_2026_11 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2026_11_account_id_client_app_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_expr_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_key_id_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_model_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2026_11_account_id_workspace_id_at_id_idx ON observability.call_log_2026_11 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2026_11_at_account_id_idx ON observability.call_log_2026_11 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2026_11_timezone_id_idx ON observability.call_log_2026_11 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_at_id_idx1 ON observability.call_log_2026_12 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2026_12_account_id_client_app_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_expr_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_key_id_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_model_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2026_12_account_id_workspace_id_at_id_idx ON observability.call_log_2026_12 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2026_12_at_account_id_idx ON observability.call_log_2026_12 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2026_12_timezone_id_idx ON observability.call_log_2026_12 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_at_id_idx1 ON observability.call_log_2027_01 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_01_account_id_client_app_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_expr_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_key_id_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_model_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_01_account_id_workspace_id_at_id_idx ON observability.call_log_2027_01 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_01_at_account_id_idx ON observability.call_log_2027_01 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_01_timezone_id_idx ON observability.call_log_2027_01 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_at_id_idx1 ON observability.call_log_2027_02 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_02_account_id_client_app_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_expr_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_key_id_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_model_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_02_account_id_workspace_id_at_id_idx ON observability.call_log_2027_02 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_02_at_account_id_idx ON observability.call_log_2027_02 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_02_timezone_id_idx ON observability.call_log_2027_02 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_at_id_idx1 ON observability.call_log_2027_03 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_03_account_id_client_app_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_expr_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_key_id_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_model_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_03_account_id_workspace_id_at_id_idx ON observability.call_log_2027_03 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_03_at_account_id_idx ON observability.call_log_2027_03 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_03_timezone_id_idx ON observability.call_log_2027_03 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_at_id_idx1 ON observability.call_log_2027_04 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_04_account_id_client_app_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_expr_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_key_id_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_model_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_04_account_id_workspace_id_at_id_idx ON observability.call_log_2027_04 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_04_at_account_id_idx ON observability.call_log_2027_04 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_04_timezone_id_idx ON observability.call_log_2027_04 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_at_id_idx1 ON observability.call_log_2027_05 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_05_account_id_client_app_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_expr_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_key_id_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_model_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_05_account_id_workspace_id_at_id_idx ON observability.call_log_2027_05 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_05_at_account_id_idx ON observability.call_log_2027_05 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_05_timezone_id_idx ON observability.call_log_2027_05 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_at_id_idx1 ON observability.call_log_2027_06 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_06_account_id_client_app_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_expr_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_key_id_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_model_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_06_account_id_workspace_id_at_id_idx ON observability.call_log_2027_06 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_06_at_account_id_idx ON observability.call_log_2027_06 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_06_timezone_id_idx ON observability.call_log_2027_06 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_at_id_idx1 ON observability.call_log_2027_07 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_07_account_id_client_app_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_expr_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_key_id_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_model_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_07_account_id_workspace_id_at_id_idx ON observability.call_log_2027_07 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_07_at_account_id_idx ON observability.call_log_2027_07 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_07_timezone_id_idx ON observability.call_log_2027_07 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_at_id_idx1 ON observability.call_log_2027_08 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_08_account_id_client_app_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_expr_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_key_id_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_model_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_08_account_id_workspace_id_at_id_idx ON observability.call_log_2027_08 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_08_at_account_id_idx ON observability.call_log_2027_08 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_08_timezone_id_idx ON observability.call_log_2027_08 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_at_id_idx1 ON observability.call_log_2027_09 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_09_account_id_client_app_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_expr_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_key_id_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_model_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_09_account_id_workspace_id_at_id_idx ON observability.call_log_2027_09 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_09_at_account_id_idx ON observability.call_log_2027_09 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_09_timezone_id_idx ON observability.call_log_2027_09 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_at_id_idx1 ON observability.call_log_2027_10 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_10_account_id_client_app_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_expr_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_key_id_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_model_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_10_account_id_workspace_id_at_id_idx ON observability.call_log_2027_10 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_10_at_account_id_idx ON observability.call_log_2027_10 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_10_timezone_id_idx ON observability.call_log_2027_10 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_at_id_idx1 ON observability.call_log_2027_11 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_11_account_id_client_app_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_expr_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_key_id_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_model_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_11_account_id_workspace_id_at_id_idx ON observability.call_log_2027_11 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_11_at_account_id_idx ON observability.call_log_2027_11 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_11_timezone_id_idx ON observability.call_log_2027_11 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_at_id_idx1 ON observability.call_log_2027_12 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2027_12_account_id_client_app_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_expr_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_key_id_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_model_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2027_12_account_id_workspace_id_at_id_idx ON observability.call_log_2027_12 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2027_12_at_account_id_idx ON observability.call_log_2027_12 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2027_12_timezone_id_idx ON observability.call_log_2027_12 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_at_id_idx1 ON observability.call_log_2028_01 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_01_account_id_client_app_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_expr_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_key_id_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_model_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_01_account_id_workspace_id_at_id_idx ON observability.call_log_2028_01 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_01_at_account_id_idx ON observability.call_log_2028_01 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_01_timezone_id_idx ON observability.call_log_2028_01 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_at_id_idx1 ON observability.call_log_2028_02 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_02_account_id_client_app_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_expr_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_key_id_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_model_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_02_account_id_workspace_id_at_id_idx ON observability.call_log_2028_02 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_02_at_account_id_idx ON observability.call_log_2028_02 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_02_timezone_id_idx ON observability.call_log_2028_02 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_at_id_idx1 ON observability.call_log_2028_03 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_03_account_id_client_app_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_expr_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_key_id_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_model_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_03_account_id_workspace_id_at_id_idx ON observability.call_log_2028_03 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_03_at_account_id_idx ON observability.call_log_2028_03 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_03_timezone_id_idx ON observability.call_log_2028_03 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_at_id_idx1 ON observability.call_log_2028_04 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_04_account_id_client_app_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_expr_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_key_id_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_model_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_04_account_id_workspace_id_at_id_idx ON observability.call_log_2028_04 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_04_at_account_id_idx ON observability.call_log_2028_04 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_04_timezone_id_idx ON observability.call_log_2028_04 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_at_id_idx1 ON observability.call_log_2028_05 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_05_account_id_client_app_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_expr_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_key_id_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_model_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_05_account_id_workspace_id_at_id_idx ON observability.call_log_2028_05 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_05_at_account_id_idx ON observability.call_log_2028_05 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_05_timezone_id_idx ON observability.call_log_2028_05 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_at_id_idx1 ON observability.call_log_2028_06 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_06_account_id_client_app_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_expr_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_key_id_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_model_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_06_account_id_workspace_id_at_id_idx ON observability.call_log_2028_06 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_06_at_account_id_idx ON observability.call_log_2028_06 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_06_timezone_id_idx ON observability.call_log_2028_06 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_at_id_idx1 ON observability.call_log_2028_07 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_07_account_id_client_app_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_expr_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_key_id_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_model_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_07_account_id_workspace_id_at_id_idx ON observability.call_log_2028_07 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_07_at_account_id_idx ON observability.call_log_2028_07 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_07_timezone_id_idx ON observability.call_log_2028_07 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_at_id_idx1 ON observability.call_log_2028_08 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_08_account_id_client_app_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_expr_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_key_id_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_model_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_08_account_id_workspace_id_at_id_idx ON observability.call_log_2028_08 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_08_at_account_id_idx ON observability.call_log_2028_08 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_08_timezone_id_idx ON observability.call_log_2028_08 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_at_id_idx1 ON observability.call_log_2028_09 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_09_account_id_client_app_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_expr_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_key_id_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_model_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_09_account_id_workspace_id_at_id_idx ON observability.call_log_2028_09 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_09_at_account_id_idx ON observability.call_log_2028_09 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_09_timezone_id_idx ON observability.call_log_2028_09 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_at_id_idx1 ON observability.call_log_2028_10 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_10_account_id_client_app_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_expr_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_key_id_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_model_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_10_account_id_workspace_id_at_id_idx ON observability.call_log_2028_10 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_10_at_account_id_idx ON observability.call_log_2028_10 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_10_timezone_id_idx ON observability.call_log_2028_10 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_at_id_idx1 ON observability.call_log_2028_11 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_11_account_id_client_app_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_expr_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_key_id_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_model_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_11_account_id_workspace_id_at_id_idx ON observability.call_log_2028_11 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_11_at_account_id_idx ON observability.call_log_2028_11 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_11_timezone_id_idx ON observability.call_log_2028_11 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_at_id_idx1 ON observability.call_log_2028_12 USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_2028_12_account_id_client_app_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_expr_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_key_id_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_model_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_2028_12_account_id_workspace_id_at_id_idx ON observability.call_log_2028_12 USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_2028_12_at_account_id_idx ON observability.call_log_2028_12 USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_2028_12_timezone_id_idx ON observability.call_log_2028_12 USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX call_log_default_account_id_at_id_idx ON observability.call_log_default USING btree (account_id, at DESC, id DESC);

CREATE INDEX call_log_default_account_id_at_id_idx1 ON observability.call_log_default USING btree (account_id, at DESC, id DESC) WHERE (attempt_count > 1);

CREATE INDEX call_log_default_account_id_client_app_at_id_idx ON observability.call_log_default USING btree (account_id, client_app, at DESC, id DESC);

CREATE INDEX call_log_default_account_id_expr_at_id_idx ON observability.call_log_default USING btree (account_id, ((error_kind IS DISTINCT FROM ''::text)), at DESC, id DESC);

CREATE INDEX call_log_default_account_id_key_id_at_id_idx ON observability.call_log_default USING btree (account_id, key_id, at DESC, id DESC);

CREATE INDEX call_log_default_account_id_model_at_id_idx ON observability.call_log_default USING btree (account_id, model, at DESC, id DESC);

CREATE INDEX call_log_default_account_id_workspace_id_at_id_idx ON observability.call_log_default USING btree (account_id, workspace_id, at DESC, id DESC);

CREATE INDEX call_log_default_at_account_id_idx ON observability.call_log_default USING btree (at, account_id) WHERE (price_nano >= 0);

CREATE INDEX call_log_default_timezone_id_idx ON observability.call_log_default USING btree ((at AT TIME ZONE 'UTC'::text) DESC, id DESC);

CREATE INDEX content_blobs_account_age ON observability.content_blobs USING btree (account_id, last_referenced_at);

CREATE INDEX content_blobs_expiry ON observability.content_blobs USING btree (expires_at) WHERE (expires_at IS NOT NULL);

CREATE INDEX content_exports_started ON observability.content_exports USING btree (started_at, id);

CREATE INDEX content_refs_account_age ON observability.content_refs USING btree (account_id, created_at, request_id, kind, part);

CREATE INDEX content_refs_export ON observability.content_refs USING btree (created_at, account_id, request_id, kind, part);

CREATE UNIQUE INDEX webhook_current_key ON observability.webhook_keys USING btree (channel_id, destination) WHERE (expires_at IS NULL);

CREATE INDEX webhook_key_lookup ON observability.webhook_keys USING btree (channel_id, destination, expires_at);

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2026_10_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2026_11_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2026_12_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_01_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_02_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_03_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_04_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_05_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_06_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_07_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_08_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_09_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_10_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_11_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2027_12_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_01_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_02_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_03_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_04_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_05_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_06_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_07_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_08_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_09_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_10_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_11_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_2028_12_pkey;

ALTER INDEX observability.call_attempt_pkey ATTACH PARTITION observability.call_attempt_default_pkey;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2026_10_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2026_10_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2026_10_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2026_10_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2026_10_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2026_10_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2026_10_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2026_10_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2026_10_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2026_10_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2026_11_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2026_11_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2026_11_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2026_11_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2026_11_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2026_11_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2026_11_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2026_11_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2026_11_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2026_11_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2026_12_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2026_12_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2026_12_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2026_12_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2026_12_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2026_12_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2026_12_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2026_12_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2026_12_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2026_12_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_01_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_01_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_01_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_01_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_01_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_01_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_01_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_01_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_01_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_01_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_02_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_02_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_02_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_02_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_02_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_02_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_02_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_02_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_02_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_02_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_03_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_03_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_03_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_03_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_03_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_03_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_03_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_03_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_03_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_03_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_04_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_04_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_04_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_04_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_04_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_04_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_04_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_04_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_04_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_04_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_05_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_05_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_05_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_05_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_05_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_05_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_05_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_05_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_05_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_05_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_06_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_06_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_06_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_06_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_06_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_06_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_06_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_06_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_06_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_06_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_07_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_07_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_07_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_07_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_07_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_07_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_07_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_07_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_07_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_07_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_08_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_08_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_08_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_08_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_08_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_08_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_08_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_08_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_08_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_08_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_09_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_09_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_09_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_09_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_09_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_09_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_09_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_09_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_09_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_09_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_10_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_10_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_10_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_10_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_10_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_10_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_10_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_10_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_10_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_10_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_11_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_11_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_11_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_11_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_11_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_11_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_11_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_11_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_11_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_11_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2027_12_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2027_12_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2027_12_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2027_12_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2027_12_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2027_12_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2027_12_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2027_12_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2027_12_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2027_12_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_01_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_01_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_01_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_01_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_01_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_01_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_01_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_01_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_01_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_01_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_02_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_02_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_02_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_02_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_02_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_02_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_02_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_02_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_02_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_02_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_03_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_03_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_03_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_03_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_03_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_03_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_03_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_03_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_03_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_03_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_04_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_04_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_04_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_04_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_04_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_04_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_04_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_04_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_04_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_04_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_05_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_05_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_05_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_05_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_05_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_05_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_05_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_05_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_05_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_05_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_06_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_06_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_06_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_06_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_06_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_06_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_06_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_06_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_06_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_06_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_07_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_07_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_07_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_07_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_07_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_07_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_07_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_07_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_07_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_07_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_08_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_08_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_08_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_08_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_08_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_08_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_08_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_08_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_08_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_08_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_09_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_09_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_09_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_09_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_09_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_09_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_09_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_09_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_09_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_09_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_10_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_10_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_10_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_10_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_10_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_10_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_10_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_10_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_10_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_10_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_11_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_11_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_11_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_11_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_11_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_11_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_11_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_11_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_11_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_11_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_2028_12_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_2028_12_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_2028_12_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_2028_12_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_2028_12_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_2028_12_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_2028_12_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_2028_12_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_2028_12_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_2028_12_timezone_id_idx;

ALTER INDEX observability.call_log_account_at ATTACH PARTITION observability.call_log_default_account_id_at_id_idx;

ALTER INDEX observability.call_log_account_fallback_at ATTACH PARTITION observability.call_log_default_account_id_at_id_idx1;

ALTER INDEX observability.call_log_account_app_at ATTACH PARTITION observability.call_log_default_account_id_client_app_at_id_idx;

ALTER INDEX observability.call_log_account_status_at ATTACH PARTITION observability.call_log_default_account_id_expr_at_id_idx;

ALTER INDEX observability.call_log_account_key_at ATTACH PARTITION observability.call_log_default_account_id_key_id_at_id_idx;

ALTER INDEX observability.call_log_account_model_at ATTACH PARTITION observability.call_log_default_account_id_model_at_id_idx;

ALTER INDEX observability.call_log_account_workspace_at ATTACH PARTITION observability.call_log_default_account_id_workspace_id_at_id_idx;

ALTER INDEX observability.call_log_at_account ATTACH PARTITION observability.call_log_default_at_account_id_idx;

ALTER INDEX observability.call_log_pkey ATTACH PARTITION observability.call_log_default_pkey;

ALTER INDEX observability.call_log_admin_time ATTACH PARTITION observability.call_log_default_timezone_id_idx;

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON observability.content_keys FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON observability.webhook_keys FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');

CREATE TRIGGER journal_usage AFTER INSERT ON observability.call_log FOR EACH ROW EXECUTE FUNCTION observability.journal_usage();

ALTER TABLE ONLY observability.alert_receipts
    ADD CONSTRAINT alert_receipts_event_id_fkey FOREIGN KEY (event_id) REFERENCES observability.alert_events(id) ON DELETE CASCADE;

ALTER TABLE ONLY observability.alert_windows
    ADD CONSTRAINT alert_windows_event_id_fkey FOREIGN KEY (event_id) REFERENCES observability.alert_events(id) ON DELETE CASCADE;

ALTER TABLE ONLY observability.channel_tests
    ADD CONSTRAINT channel_tests_receipt_id_fkey FOREIGN KEY (receipt_id) REFERENCES observability.alert_receipts(id) ON DELETE SET NULL;

ALTER TABLE ONLY observability.content_refs
    ADD CONSTRAINT content_refs_blob_id_site_id_account_id_fkey FOREIGN KEY (blob_id, site_id, account_id) REFERENCES observability.content_blobs(id, site_id, account_id);




































































CREATE POLICY enqueue ON observability.usage_journal FOR INSERT WITH CHECK ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY platform_only ON observability.alert_configuration USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.alert_events USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.alert_receipts USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.alert_windows USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.channel_tests USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.content_blobs USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.content_exports USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.content_keys USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.content_refs USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.retention USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.spend_anomalies USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.spend_detection USING (platform.in_platform_scope());

CREATE POLICY platform_only ON observability.usage_journal USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY platform_only ON observability.webhook_keys USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY reader ON observability.usage_backfill FOR SELECT USING (( SELECT platform.in_any_scope() AS in_any_scope));




CREATE POLICY tenant ON observability.call_attempt USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2026_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2026_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2026_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_01 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_02 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_03 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_04 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_05 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_06 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_07 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_08 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_09 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2027_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_01 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_02 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_03 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_04 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_05 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_06 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_07 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_08 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_09 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_2028_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_attempt_default USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2026_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2026_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2026_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_01 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_02 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_03 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_04 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_05 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_06 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_07 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_08 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_09 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2027_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_01 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_02 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_03 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_04 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_05 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_06 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_07 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_08 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_09 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_10 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_11 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_2028_12 USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.call_log_default USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));

CREATE POLICY tenant ON observability.usage_hourly FOR SELECT USING ((( SELECT platform.in_platform_scope() AS in_platform_scope) OR (site_id = ANY (( SELECT platform.allowed_sites() AS allowed_sites)::uuid[])) OR (account_id = ANY (( SELECT platform.allowed_accounts() AS allowed_accounts)::uuid[]))));





CREATE POLICY writer ON observability.usage_backfill USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

CREATE POLICY writer ON observability.usage_hourly USING (( SELECT platform.in_platform_scope() AS in_platform_scope));

GRANT USAGE ON SCHEMA observability TO axiom_app;
GRANT ALL ON SCHEMA observability TO axiom_retention_job;

GRANT SELECT,UPDATE ON TABLE observability.alert_configuration TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.alert_events TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.alert_receipts TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.alert_windows TO axiom_app;

GRANT SELECT,INSERT ON TABLE observability.call_attempt TO axiom_app;

GRANT SELECT,INSERT ON TABLE observability.call_log TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.channel_tests TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.content_blobs TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE observability.content_exports TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.content_keys TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE observability.content_refs TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE observability.retention TO axiom_app;
GRANT SELECT ON TABLE observability.retention TO axiom_retention_job;

GRANT SELECT,INSERT,DELETE ON TABLE observability.spend_anomalies TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE observability.spend_detection TO axiom_app;

GRANT SELECT,UPDATE ON TABLE observability.usage_backfill TO axiom_app;

GRANT SELECT,INSERT,UPDATE ON TABLE observability.usage_hourly TO axiom_app;

GRANT SELECT,INSERT,DELETE ON TABLE observability.usage_journal TO axiom_app;

GRANT USAGE ON SEQUENCE observability.usage_journal_id_seq TO axiom_app;

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE observability.webhook_keys TO axiom_app;


-- The deployment creates the optional retention login before migration.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_retention') THEN
        EXECUTE 'GRANT axiom_retention_job TO axiom_retention WITH INHERIT TRUE, SET TRUE';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE ONLY observability.alert_configuration FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.alert_events FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.alert_receipts FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.alert_windows FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2026_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2026_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2026_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_01 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_02 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_03 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_04 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_05 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_06 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_07 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_08 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_09 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2027_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_01 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_02 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_03 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_04 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_05 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_06 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_07 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_08 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_09 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_2028_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_attempt_default FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2026_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2026_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2026_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_01 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_02 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_03 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_04 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_05 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_06 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_07 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_08 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_09 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2027_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_01 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_02 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_03 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_04 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_05 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_06 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_07 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_08 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_09 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_10 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_11 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_2028_12 FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.call_log_default FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.channel_tests FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.content_blobs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.content_exports FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.content_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.content_refs FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.retention FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.spend_anomalies FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.spend_detection FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.usage_backfill FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.usage_hourly FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.usage_journal FORCE ROW LEVEL SECURITY;
ALTER TABLE ONLY observability.webhook_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_configuration ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.alert_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2026_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2026_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2026_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_01 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_02 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_03 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_04 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_05 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_06 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_07 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_08 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_09 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2027_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_01 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_02 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_03 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_04 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_05 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_06 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_07 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_08 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_09 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_2028_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_attempt_default ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2026_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2026_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2026_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_01 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_02 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_03 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_04 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_05 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_06 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_07 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_08 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_09 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2027_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_01 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_02 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_03 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_04 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_05 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_06 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_07 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_08 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_09 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_10 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_11 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_2028_12 ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.call_log_default ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.channel_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.content_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.content_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.content_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.content_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.retention ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.spend_anomalies ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.spend_detection ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.usage_backfill ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.usage_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.usage_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE observability.webhook_keys ENABLE ROW LEVEL SECURITY;

CREATE INDEX call_log_account_sessions_at ON observability.call_log (account_id, at DESC, id DESC)
    INCLUDE (site_id, session_id, client_app, model, currency, price_nano, workspace_id, key_id, error_kind, attempt_count)
    WHERE session_id <> '' AND NOT billing_pending;
