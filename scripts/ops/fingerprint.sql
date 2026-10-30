-- One row per table: the table's name, how many rows it has, and a hash of everything in it, for the rehearsal's
-- before/after comparison (scripts/ops/rehearse.sh; docs/ops/backup-restore.md「恢复演练」).
--
-- The row count is its own column so that a caller can tell an empty table from a full one: the hash of an empty table is
-- the hash of the empty string, which would otherwise look like data. Each row is hashed in its text form and the hashes
-- are aggregated in hash order, so the result depends on the rows and not on the order they happen to be stored in. Read
-- as a superuser, or row security would hide rows and a table whose rows changed would keep the same fingerprint. It
-- reads every row of every table, which is fine on the rehearsal's stack and is not a production tool. The one column
-- left out is billing.price_policies.confirmed_at: the publication job rewrites it with the current time on every start,
-- while the policy's rev and multipliers must come back from the set.
SELECT format(
  'SELECT %L, count(*), md5(coalesce(string_agg(md5((to_jsonb(t) - ''confirmed_at'')::text), %L ORDER BY md5((to_jsonb(t) - ''confirmed_at'')::text)), %L)) FROM %I.%I t;',
  n.nspname || '.' || c.relname, '', 'empty', n.nspname, c.relname)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname <> 'information_schema'
  AND n.nspname NOT LIKE 'pg\_%'
ORDER BY 1
\gexec
