-- One row per table: the table's name and a hash of everything in it, for the rehearsal's before/after comparison
-- (scripts/ops/rehearse.sh; docs/ops/backup-restore.md「恢复演练」).
--
-- Each row is hashed in its text form and the hashes are aggregated in hash order, so the result depends on the rows and
-- not on the order they happen to be stored in. Read as a superuser, or row security would hide rows and a table whose
-- rows changed would keep the same fingerprint. It reads every row of every table, which is fine on the rehearsal's
-- stack and is not a production tool.

SELECT format(
  'SELECT %L, md5(coalesce(string_agg(md5(t::text), %L ORDER BY md5(t::text)), %L)) FROM %I.%I t;',
  n.nspname || '.' || c.relname, '', 'empty', n.nspname, c.relname)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname <> 'information_schema'
  AND n.nspname NOT LIKE 'pg\_%'
ORDER BY 1
\gexec
