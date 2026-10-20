package mail

import "uuid"

// Tenant is the site and the account a message is sent for: the tenant that owns the row in the outbox and whose scope
// the worker reads it in. A zero Account means the site itself, for the flows that run before a registration has an
// account, as platform.audit_log's site-only rows do.
type Tenant struct {
	Site    uuid.UUID
	Account uuid.UUID
}

// outboxLookup is the function that names a queued message's tenant. The worker carries the row's id and nothing else,
// so it has no tenant to build a scope from, and this lookup — SECURITY DEFINER, owned by axiom_lookup, granted only to
// the application — returns the row's two tenant columns and nothing else
// (internal/platform/pg/migrations/00008_mail_outbox.sql).
const outboxLookup = "platform.mail_tenant"

const (
	// insertOutbox queues one message and returns the id the queue carries in its place.
	insertOutbox = `INSERT INTO platform.mail_outbox (site_id, account_id, recipient, subject, text_body, html_body)
	VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`
	// readOutbox reads the queued message, in the scope of the tenant the lookup named.
	readOutbox = `SELECT recipient, subject, text_body, html_body FROM platform.mail_outbox WHERE id = $1`
	// deleteOutbox removes a message that has been sent or refused for good.
	deleteOutbox = `DELETE FROM platform.mail_outbox WHERE id = $1`
)
