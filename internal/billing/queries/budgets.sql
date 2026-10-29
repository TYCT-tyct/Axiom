-- name: ReadBudgetUsage :one
WITH t AS MATERIALIZED (SELECT clock_timestamp() AS now)
SELECT coalesce(sum(s.spent), 0)::bigint AS spent,
    coalesce(sum(s.reserved), 0)::bigint AS reserved,
    CASE WHEN @reset::text = '' THEN 'epoch'::timestamptz ELSE billing.window_to(@reset::text, t.now) END::timestamptz AS resets_at
FROM t LEFT JOIN billing.spend s ON s.subject = @subject
    AND s.window_start >= billing.window_from(@reset::text, t.now)
    AND s.window_start < billing.window_to(@reset::text, t.now)
GROUP BY t.now;
