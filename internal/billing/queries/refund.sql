-- name: AddFrozen :exec
-- The money writer owns the balance row lock; replay only writes the batch cache.
UPDATE billing.balances SET frozen = frozen + @delta::bigint WHERE account_id = @account_id;
