-- +goose Up

-- A model-side loop samples the supplier once per hop. Keep every supplier request's cost and reconciliation key
-- separately, while the gateway attempt remains the unit billing charges and attempt_entries refers to.
ALTER TABLE suppliers.attempt_costs ADD COLUMN hop smallint NOT NULL DEFAULT 0;
ALTER TABLE suppliers.charge_records ADD COLUMN attempt_hop smallint NOT NULL DEFAULT 0;
GRANT UPDATE (attempt_hop) ON suppliers.charge_records TO axiom_app;

-- The charge FK depends on the old attempt primary key, so remove it before replacing that key.
ALTER TABLE suppliers.charge_records DROP CONSTRAINT IF EXISTS charge_records_attempt_request_id_attempt_seq_fkey;
ALTER TABLE suppliers.charge_records DROP CONSTRAINT IF EXISTS charge_records_attempt_request_id_attempt_seq_key;
ALTER TABLE suppliers.attempt_costs DROP CONSTRAINT attempt_costs_pkey;
ALTER TABLE suppliers.attempt_costs ADD CONSTRAINT attempt_costs_pkey PRIMARY KEY (request_id, attempt_seq, hop);
ALTER TABLE suppliers.attempt_costs ADD CONSTRAINT attempt_costs_hop_check CHECK (hop BETWEEN 0 AND 255);

-- A charge is matched to the hop that produced it, not to the attempt's last receipt.
ALTER TABLE suppliers.charge_records ADD CONSTRAINT charge_records_attempt_request_id_attempt_seq_hop_key
    UNIQUE (attempt_request_id, attempt_seq, attempt_hop);
ALTER TABLE suppliers.charge_records ADD CONSTRAINT charge_records_attempt_request_id_attempt_seq_hop_fkey
    FOREIGN KEY (attempt_request_id, attempt_seq, attempt_hop)
    REFERENCES suppliers.attempt_costs (request_id, attempt_seq, hop);
