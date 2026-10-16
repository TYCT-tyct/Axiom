-- +goose Up

-- A site's accounting currency: the currency of the Money its API shows (balance, spend limits) and of the amounts
-- its calls are priced in. It is set when the site is made and never changes, so no UPDATE is granted on it: a
-- currency change would reprice every balance. A site made before this column keeps USD, the one currency used so
-- far; `axiom site init` always states it.
ALTER TABLE accounts.sites ADD COLUMN currency text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$');
