package accounts_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
)

// TestApplicationChangesOnlyUpdatableColumns: the application may update exactly the columns the README lists, and
// delete nothing except expired password reset data and dispatched notices; the other tables refuse the privilege, not just the rows.
func TestApplicationChangesOnlyUpdatableColumns(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	updatable := map[string][]string{
		"presets":                  {"name", "config", "updated_at", "deleted_at"},
		"notification_preferences": {"low_balance", "top_up", "factor_changed", "spend_anomaly", "threshold", "webhook_url", "language"},
		"user_notices":             {"email_done", "email_failed", "dispatched", "webhook_url", "webhook_state"},
		"announcements":            {"withdrawn_at"},
		"sites":                    {"name", "status", "key_prefix", "registration", "brand_color", "default_tier_id", "logo_hash", "login_domain"},
		"site_domains":             {"disabled_at"},
		"site_settings":            {"console_idle_timeout", "console_max_age", "admin_idle_timeout", "admin_max_age", "new_key_daily_limit", "model_prices_public", "ip_attempt_burst", "ip_attempts_per_minute", "email_sign_in_failures", "user_code_failures", "challenge_code_attempts", "max_keys_per_account", "key_creations_per_hour", "registration_human_check", "registration_human_check_bits", "registration_domain_burst", "registration_domain_per_hour", "registration_domain_allow", "registration_domain_deny", "passkeys_enabled", "max_workspaces_per_account"},
		"tiers":                    {"name", "rate_requests"},
		"roles":                    {"permissions", "additional_permissions"},
		"accounts":                 {"name", "status", "tier_id", "max_keys", "key_creations_per_hour", "rate_requests", "billing_preference", "max_workspaces"},
		"site_billing_preferences": {"preference"},
		"workspaces":               {"name", "status", "budget", "budget_reset", "deleted_at"},
		"users": {
			"password_hash", "factor_version", "status", "totp_ciphertext", "totp_nonce", "totp_wrapped_dek",
			"totp_key_version", "totp_last_step", "mfa_enabled_at",
		},
		"members":                 nil,
		"legal_documents":         nil,
		"legal_acceptances":       nil,
		"platform_grants":         {"revoked_at"},
		"api_keys":                {"last_used_at", "rate_requests", "updated_at", "management_window", "management_requests", "name", "status", "spend_limit", "limit_reset", "preset_id", "models", "ip_allow", "deleted_at"},
		"sessions":                {"last_seen_at", "mfa_attempts", "expires_at", "idle_timeout", "revoked_at", "recovery_codes_ciphertext", "recovery_codes_nonce"},
		"site_oauth_providers":    {"client_id", "client_secret_ciphertext", "client_secret_nonce", "client_secret_wrapped_dek", "client_secret_key_version", "updated_at"},
		"user_identities":         nil,
		"passkey_credentials":     {"sign_count", "last_used_at", "backup_state", "name"},
		"oauth_flows":             {"consumed_at"},
		"webauthn_flows":          {"consumed_at"},
		"recovery_codes":          {"used_at"},
		"security_failures":       {"started_at", "failures", "completed_peak", "sampled_at", "reported"},
		"password_reset_requests": {"id", "email", "domain", "language", "expires_at"},
		"password_reset_tokens":   {"used_at"},
		"registration_requests":   {"tier_id", "domain", "language", "expires_at", "token_hash"},
		"agent_credentials":       {"revoked_at", "last_used_at"},
		"agent_credential_tokens": {"retires_at"},
	}
	assertUpdatableColumns(t, db, updatable)
	assertDeniedDeletes(t, db, updatable)
}

// assertUpdatableColumns checks the application may update exactly the columns the README lists.
func assertUpdatableColumns(t *testing.T, db *pg.DB, updatable map[string][]string) {
	t.Helper()
	// Generated columns are left out: the database refuses to set them before it looks at privileges.
	for _, column := range texts(t, db, platform, `SELECT table_name || ' ' || column_name FROM information_schema.columns
		WHERE table_schema = 'accounts' AND is_generated = 'NEVER'`) {
		table, name, _ := strings.Cut(column, " ")
		_, err := exec(t, db, platform, "UPDATE accounts."+table+" SET "+name+" = "+name+" WHERE false")
		if want := slices.Contains(updatable[table], name); want != (err == nil) || !want && state(err) != "42501" {
			t.Errorf("updating %s.%s: %v, want allowed: %t", table, name, err, want)
		}
	}
}

// assertDeniedDeletes checks every table outside deletable refuses the privilege, not just the rows.
func assertDeniedDeletes(t *testing.T, db *pg.DB, updatable map[string][]string) {
	t.Helper()
	for table := range updatable {
		if deletable(table) {
			continue
		}
		if _, err := exec(t, db, platform, "DELETE FROM accounts."+table+" WHERE false"); state(err) != "42501" {
			t.Errorf("deleting from %s: %v, want permission denied", table, err)
		}
	}
}

// deletable is the tables a site's data may delete from: reset data, dispatched notices, and the records of a
// credential or a one-time flow. Everything else must refuse DELETE.
func deletable(table string) bool {
	return strings.HasPrefix(table, "password_reset_") || table == "registration_requests" || table == "user_notices" ||
		table == "site_oauth_providers" || table == "user_identities" || table == "passkey_credentials" ||
		table == "oauth_flows" || table == "webauthn_flows" || table == "site_billing_preferences"
}

// TestOnePolicyPerCommand: every table has exactly one policy for everyone for each of SELECT, INSERT and UPDATE,
// and none for ALL; only reset tables and user notices allow DELETE of expired rows. Members has none for UPDATE. Policies are OR-ed,
// so a second UPDATE or a FOR ALL policy would let through what a terminal-state policy refuses.
func TestOnePolicyPerCommand(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	got := texts(t, db, platform, `SELECT c.relname || ': ' || coalesce(string_agg(p.cmd, ', ' ORDER BY p.cmd), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_policies p ON p.schemaname = n.nspname AND p.tablename = c.relname AND p.roles = '{public}'
		WHERE n.nspname = 'accounts' AND c.relkind = 'r' GROUP BY c.relname ORDER BY c.relname`)
	var want []string
	for _, table := range []string{
		"accounts", "agent_credential_tokens", "agent_credentials", "announcements", "api_keys", "legal_acceptances", "legal_documents", "members", "notification_preferences", "oauth_flows", "passkey_credentials",
		"password_reset_requests", "password_reset_tokens", "platform_grants", "presets", "recovery_codes",
		"registration_requests", "roles", "security_failures", "sessions", "site_billing_preferences", "site_domains", "site_logos",
		"site_oauth_providers", "site_settings", "sites", "tiers", "user_identities", "user_notices", "users",
		"webauthn_flows", "workspaces",
	} {
		want = append(want, table+": "+policyCommands(table))
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("policies for everyone (-want +got):\n%s", diff)
	}
}

// policyCommands is the one policy each command a table's policies may cover.
func policyCommands(table string) string {
	switch {
	case table == "user_identities":
		return "DELETE, INSERT, SELECT"
	case table == "members" || table == "site_logos" || table == "legal_documents" || table == "legal_acceptances":
		return "INSERT, SELECT"
	case deletable(table):
		return "DELETE, INSERT, SELECT, UPDATE"
	default:
		return "INSERT, SELECT, UPDATE"
	}
}

// TestSessionsHoldNoKeyRingEnvelope: the codes a link leaves for the console are encrypted under the session token, not
// the key ring. An older node's startup inventory check scans for *_wrapped_dek and must find nothing here, or a
// migration would keep that node from starting or rolling back (#530 finding 3).
func TestSessionsHoldNoKeyRingEnvelope(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	var found int
	if err := db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'accounts' AND table_name = 'sessions'
			  AND column_name LIKE '%\_wrapped_dek' ESCAPE '\'`).Scan(&found)
	}); err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Fatalf("%d key-ring envelope columns on accounts.sessions: an older node's keyring check would refuse to start", found)
	}
}
