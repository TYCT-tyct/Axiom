-- +goose Up

-- What the admin's upstream pages read and write beyond the sync's tables (#948): where a credential's key came from and
-- what its last check found, each supplier's intake rule, the suppliers' to-do queue, and the indexes the daily
-- reconciliation reads by.

-- source is where the key came from: the sync made it, a person bound one the supplier lists, or a person pasted its
-- text. A row from before this column is NULL, except a pasted one, which always had its last four characters (hint).
ALTER TABLE suppliers.credentials
    ADD COLUMN source text CONSTRAINT credentials_source_check CHECK (source IN ('made', 'bound', 'pasted')),
    ADD COLUMN checked_at timestamptz,
    ADD COLUMN check_outcome text CONSTRAINT credentials_check_outcome_check CHECK (check_outcome IN ('passed', 'failed', 'inconclusive')),
    ADD COLUMN check_cause text CONSTRAINT credentials_check_cause_check CHECK (check_cause <> ''),
    ADD COLUMN check_status integer CONSTRAINT credentials_check_status_check CHECK (check_status BETWEEN 0 AND 999),
    ADD CONSTRAINT check_whole CHECK ((checked_at IS NULL) = (check_outcome IS NULL) AND (checked_at IS NULL) = (check_status IS NULL));

UPDATE suppliers.credentials SET source = 'pasted' WHERE hint IS NOT NULL;

GRANT UPDATE (checked_at, check_outcome, check_cause, check_status) ON TABLE suppliers.credentials TO axiom_app;

-- One rule a supplier; none is the default rule `listed`. rev is the revision a change is decided against.
CREATE TABLE suppliers.intake_rules (
    supplier_id uuid NOT NULL PRIMARY KEY REFERENCES suppliers.suppliers (id),
    mode text NOT NULL CONSTRAINT intake_rules_mode_check CHECK (mode IN ('listed', 'known', 'names')),
    include text[] NOT NULL DEFAULT '{}',
    exclude text[] NOT NULL DEFAULT '{}',
    rev bigint NOT NULL CONSTRAINT intake_rules_rev_check CHECK (rev >= 1),
    CONSTRAINT names_include CHECK ((mode = 'names') = (cardinality(include) > 0)),
    CONSTRAINT exclude_with_names CHECK (mode = 'names' OR cardinality(exclude) = 0),
    CONSTRAINT patterns_bounded CHECK (cardinality(include) <= 50 AND cardinality(exclude) <= 50)
);

ALTER TABLE suppliers.intake_rules OWNER TO axiom_owner;
ALTER TABLE suppliers.intake_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.intake_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.intake_rules USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = intake_rules.supplier_id))));
GRANT SELECT, INSERT ON TABLE suppliers.intake_rules TO axiom_app;
GRANT UPDATE (mode, include, exclude, rev) ON TABLE suppliers.intake_rules TO axiom_app;

-- An item of the queue is one condition of one supplier (kind and subject), kept while the condition holds and deleted
-- when it ends. detail holds what the kind shows, as text. ignored_at hides it until an observation finds it again.
CREATE TABLE suppliers.inbox_items (
    id uuid DEFAULT uuidv7() NOT NULL PRIMARY KEY,
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers (id),
    kind text NOT NULL CONSTRAINT inbox_items_kind_check CHECK (kind IN ('low_balance', 'ratio_changed', 'cost_mismatch',
        'group_added', 'unlisted_model_called', 'key_invalid', 'unknown_model')),
    subject text NOT NULL CONSTRAINT inbox_items_subject_check CHECK (subject <> ''),
    detail jsonb NOT NULL DEFAULT '{}' CONSTRAINT inbox_items_detail_check CHECK (jsonb_typeof(detail) = 'object'),
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    ignored_at timestamptz,
    CONSTRAINT inbox_items_condition UNIQUE (supplier_id, kind, subject)
);

ALTER TABLE suppliers.inbox_items OWNER TO axiom_owner;
ALTER TABLE suppliers.inbox_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY suppliers.inbox_items FORCE ROW LEVEL SECURITY;
CREATE POLICY supplier_visible ON suppliers.inbox_items USING ((EXISTS ( SELECT
   FROM suppliers.suppliers s
  WHERE (s.id = inbox_items.supplier_id))));
CREATE INDEX inbox_items_shown ON suppliers.inbox_items USING btree (last_seen_at DESC, id DESC) WHERE (ignored_at IS NULL);
GRANT SELECT, INSERT, DELETE ON TABLE suppliers.inbox_items TO axiom_app;
GRANT UPDATE (detail, last_seen_at, ignored_at) ON TABLE suppliers.inbox_items TO axiom_app;

-- The reconciliation is read by supplier and UTC day: what was estimated of the attempts, and what was charged.
CREATE INDEX attempt_costs_supplier_finished ON suppliers.attempt_costs USING btree (supplier_id, finished_at);
CREATE INDEX charge_records_supplier_occurred ON suppliers.charge_records USING btree (supplier_id, occurred_at);
-- The queue reads a supplier's ratio changes of the last week at every sync that reads the offer.
CREATE INDEX sync_events_ratio_changed ON suppliers.sync_events USING btree (supplier_id, created_at) WHERE (kind = 'ratio_changed');
