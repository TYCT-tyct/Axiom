-- +goose Up

-- An endpoint's attempts of the last 24 hours, which the break-even of a per-request endpoint is worked out from
-- (getSupplierEndpointCost): without it each read scans every attempt ever settled.
CREATE INDEX attempt_costs_endpoint_finished ON suppliers.attempt_costs (endpoint_id, finished_at);
