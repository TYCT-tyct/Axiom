package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/codes"
)

// TenantTx runs fn in a transaction limited to the tenant that the lookup function finds for key, and reports whether
// the lookup found the key at all. It is how a background job reaches a tenant's row: the job carries an id and belongs
// to no tenant, so it can neither build a scope nor read the row through a policy of its own, and the lookup — a
// SECURITY DEFINER function owned by axiom_lookup, runnable only by the application, that returns the site_id and
// account_id of one row and nothing else — is how the tenant is learned (internal/platform/README.md「任务队列」, after
// internal/accounts/migrations/00002_lookup.sql). The transaction calls the lookup with the empty scope, so nothing of
// the tenant's is readable before the lookup names it, sets the tenant's scope, and only then runs fn. When the lookup
// has no row for key, found is false and fn does not run.
//
// lookup is the "schema.function" of a lookup that takes the key and returns those two columns, account_id NULL when
// the row belongs to the site itself.
func (db *DB) TenantTx(ctx context.Context, lookup string, key uuid.UUID, fn func(Tx) error) (found bool, err error) {
	query, err := tenantLookup(lookup)
	if err != nil {
		return false, err
	}
	ctx, span := startSpan(ctx, db.tracer)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "transaction failed")
		}
		span.End()
	}()
	err = pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if found, err = lookTenant(ctx, tx, query, key); err != nil || !found {
			return err
		}
		return fn(Tx{tx})
	})
	return found, err
}

// tenantLookup is the statement that calls the lookup for one key. The name comes from the module rather than from data,
// and Sanitize quotes both parts, so a mistaken name fails as a missing function instead of widening into SQL.
func tenantLookup(lookup string) (string, error) {
	schema, function, ok := strings.Cut(lookup, ".")
	if !ok || schema == "" || function == "" {
		return "", fmt.Errorf("pg: the tenant lookup %q is not schema.function", lookup)
	}
	return "SELECT site_id::text, coalesce(account_id::text, '') FROM " +
		pgx.Identifier{schema, function}.Sanitize() + "($1)", nil
}

// lookTenant calls the lookup with the empty scope and, when it named a tenant, limits the rest of the transaction to
// that tenant. The two columns come back as text, which reads the same whether or not the row has an account.
func lookTenant(ctx context.Context, tx pgx.Tx, query string, key uuid.UUID) (bool, error) {
	if err := applyScope(ctx, tx, Scope{}); err != nil {
		return false, fmt.Errorf("set the scope: %w", err)
	}
	var site, account string
	err := tx.QueryRow(ctx, query, key).Scan(&site, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up the tenant: %w", err)
	}
	if err := setTenantScope(ctx, tx, site, account); err != nil {
		return false, err
	}
	return true, nil
}

// setTenantScope limits the transaction to the tenant the lookup named: the account's rows when the row belongs to an
// account, the site's rows otherwise. The narrower of the two is enough, because the policies of the tables that carry
// these columns compare them with the arrays read by platform.allowed_sites and platform.allowed_accounts.
func setTenantScope(ctx context.Context, tx pgx.Tx, site, account string) error {
	siteID, err := uuid.Parse(site)
	if err != nil {
		return fmt.Errorf("the lookup returned the site %q: %w", site, err)
	}
	scope := Scope{sites: []uuid.UUID{siteID}}
	if account != "" {
		accountID, err := uuid.Parse(account)
		if err != nil {
			return fmt.Errorf("the lookup returned the account %q: %w", account, err)
		}
		scope = Scope{accounts: []uuid.UUID{accountID}}
	}
	if err := applyScope(ctx, tx, scope); err != nil {
		return fmt.Errorf("set the tenant's scope: %w", err)
	}
	return nil
}
