-- What the admin API reads of the ledger.

-- name: ReadCreditBatch :many
SELECT id, site_id, account_id, amount, expires_at, created_at FROM billing.credit_batches WHERE id = @id;
