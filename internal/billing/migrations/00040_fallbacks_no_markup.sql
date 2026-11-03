-- +goose Up

-- The fallback rows lose the markup (#232): it priced a correction of a charge by a supplier's actual cost, and M1 has
-- no corrections (docs/domains/billing.md「用量来源与暂估」), so no code applied it. The row's parser is a closed set, so
-- the member must leave the stored text before the field leaves the code; Axiom has never been deployed to production,
-- so one release does both instead of migrating in one and dropping the field in the next.
--
-- The body is canonical (fallback.go fallbacksJSON), so the member appears as `"markup":"<decimal>"` with a comma on one
-- side. A reservation keeps the fallback text it locked — those rows are immutable sale inputs and the application has
-- no UPDATE grant on that column — and parseLockedFallbacks ignores the member, so old calls settle at the prices they
-- were admitted at.
UPDATE billing.fallbacks
SET body = regexp_replace(regexp_replace(body, ',"markup":"[^"]*"', '', 'g'), '"markup":"[^"]*",', '', 'g'),
    updated_at = clock_timestamp()
WHERE body LIKE '%"markup"%';
